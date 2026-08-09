package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

var ErrDeviceNotFound = errors.New("device not found")

const powerRecoveryScanTimeout = 5 * time.Second

type Manager struct {
	mu            sync.RWMutex
	scanMu        sync.Mutex
	devices       map[DeviceID]Device
	metadata      map[DeviceID]DeviceMetadata
	providers     []DiscoveryProvider
	factories     []DeviceFactory
	store         StateStore
	events        chan DeviceMetadata
	showDisplays  bool
	showConsoles  bool
	showComputers bool
	showOffline   bool
}

func NewManager(store StateStore, providers []DiscoveryProvider, factories []DeviceFactory) (*Manager, error) {
	if store == nil {
		return nil, errors.New("state store is required")
	}
	m := &Manager{
		devices:       make(map[DeviceID]Device),
		metadata:      make(map[DeviceID]DeviceMetadata),
		providers:     providers,
		factories:     factories,
		store:         store,
		events:        make(chan DeviceMetadata, 32),
		showDisplays:  true,
		showConsoles:  false,
		showComputers: true,
		showOffline:   true,
	}
	loaded, err := store.LoadDevices(context.Background())
	if err != nil {
		return nil, err
	}
	for _, md := range loaded {
		md = NormalizeMetadata(md)
		// A saved record proves that this device was known, not that it is
		// reachable right now. Start every daemon with remembered devices in an
		// honest offline state; the initial scan promotes live responses to
		// online. This keeps sleeping TVs, computers, and Raspberry
		// Pis visible immediately after a restart instead of briefly claiming
		// they are online because the previous process saw them.
		md.Online = false
		m.metadata[md.ID] = md
		// Recreate adapters from persisted metadata as well as fresh scan
		// results. This is important for devices that are currently asleep:
		// Wake-on-LAN must still be executable when discovery cannot reach them.
		item := DiscoveredDevice{Metadata: md, Source: md.Discovery}
		for _, factory := range factories {
			if !factory.Supports(item) {
				continue
			}
			if device, createErr := factory.Create(context.Background(), item); createErr == nil {
				m.devices[md.ID] = device
				md.Capabilities = device.Capabilities()
				m.metadata[md.ID] = md
			}
			break
		}
	}
	return m, nil
}

func (m *Manager) Events() <-chan DeviceMetadata { return m.events }

func (m *Manager) SetInventoryVisibility(settings InventoryVisibility) {
	m.mu.Lock()
	m.showDisplays = settings.ShowDisplayDevices
	// Consoles are intentionally out of the current inventory scope. Keep the
	// field for config/API compatibility with older installations, but never
	// expose console records to the user-facing inventory.
	m.showConsoles = false
	m.showComputers = settings.ShowComputerDevices
	m.showOffline = settings.ShowOfflineDevices
	m.mu.Unlock()
}

func (m *Manager) List() []DeviceMetadata {
	m.mu.RLock()
	defer m.mu.RUnlock()
	visibility := InventoryVisibility{ShowDisplayDevices: m.showDisplays, ShowConsoleDevices: m.showConsoles, ShowComputerDevices: m.showComputers, ShowOfflineDevices: m.showOffline}
	byIdentity := make(map[string]DeviceMetadata)
	byIP := make(map[string]DeviceMetadata)
	var withoutIP []DeviceMetadata
	for _, md := range m.metadata {
		if !visibleCategory(visibility, md) {
			continue
		}
		if !visibility.ShowOfflineDevices && !md.Online {
			continue
		}
		if ip := net.ParseIP(md.IP); ip != nil && (ip.IsMulticast() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || isBroadcastIP(ip)) {
			// Ignore stale broadcast/multicast and link-local records that may
			// have been written by older ARP scans.
			continue
		}
		if key := metadataIdentityKey(md); key != "" {
			if current, exists := byIdentity[key]; !exists {
				byIdentity[key] = md
			} else {
				byIdentity[key] = mergeMetadata(current, md)
			}
			continue
		}
		if md.IP == "" {
			withoutIP = append(withoutIP, md)
			continue
		}
		current, exists := byIP[md.IP]
		if !exists || metadataRank(md) > metadataRank(current) || (metadataRank(md) == metadataRank(current) && md.LastSeen.After(current.LastSeen)) {
			byIP[md.IP] = md
		}
	}
	for _, md := range byIdentity {
		if md.IP == "" {
			withoutIP = append(withoutIP, md)
			continue
		}
		current, exists := byIP[md.IP]
		if !exists || metadataRank(md) > metadataRank(current) || (metadataRank(md) == metadataRank(current) && md.LastSeen.After(current.LastSeen)) {
			byIP[md.IP] = md
		}
	}
	result := append([]DeviceMetadata(nil), withoutIP...)
	for _, md := range byIP {
		result = append(result, md)
	}
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if DisplayName(result[j]) < DisplayName(result[i]) {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	return collapseMetadata(result)
}

func collapseMetadata(items []DeviceMetadata) []DeviceMetadata {
	result := append([]DeviceMetadata(nil), items...)
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); {
			if sameDeviceIdentity(result[i], result[j]) {
				result[i] = mergeMetadata(result[i], result[j])
				result = append(result[:j], result[j+1:]...)
				continue
			}
			j++
		}
	}
	return result
}

func isBroadcastIP(ip net.IP) bool {
	ip = ip.To4()
	return ip != nil && (ip.Equal(net.IPv4bcast) || ip[3] == 255)
}

func metadataRank(md DeviceMetadata) int {
	if md.Discovery == "host" {
		return 6
	}
	if md.Manufacturer != "" && md.Kind == DeviceKindTV {
		return 5
	}
	if md.Manufacturer != "" {
		return 4
	}
	if md.Kind != DeviceKindUnknown {
		return 3
	}
	return 1
}

func metadataIdentityKey(md DeviceMetadata) string {
	md = NormalizeMetadata(md)
	if md.Kind == DeviceKindComputer || md.Kind == DeviceKindConsole {
		if identity := normalizeIdentity(md.MAC); identity != "" {
			return string(md.Category) + "-mac:" + identity
		}
		name := normalizeDeviceName(md.Name)
		if name != "" && !isGenericInventoryName(name) && md.IP != "" {
			return string(md.Category) + "-name-ip:" + name + "@" + md.IP
		}
		if md.IP != "" {
			return string(md.Category) + "-ip:" + md.IP
		}
		return ""
	}
	if md.Kind != DeviceKindTV {
		return ""
	}
	if identity := normalizeIdentity(md.DUID); identity != "" {
		return "tv-duid:" + identity
	}
	if identity := normalizeIdentity(md.MAC); identity != "" {
		return "tv-mac:" + identity
	}
	// A display name is not a device identity: households commonly have two
	// TVs can share a friendly name such as “Living Room”. Only use a name together
	// with its address when Samsung has not supplied a stable DUID/MAC.
	name := normalizeDeviceName(md.Name)
	if name != "" && name != "samsung tv" && !strings.HasPrefix(name, "upnp device ") && isSamsungTVMetadata(md) {
		return "tv-name:" + name
	}
	if name != "" && name != "samsung tv" && !strings.HasPrefix(name, "upnp device ") && md.IP != "" {
		return "tv-name-ip:" + name + "@" + md.IP
	}
	return ""
}

func isGenericInventoryName(name string) bool {
	return name == "" || strings.HasPrefix(name, "lan device ") || strings.HasPrefix(name, "windows computer ") || strings.HasPrefix(name, "raspberry pi ")
}

func normalizeIdentity(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, ":", "")
	value = strings.ReplaceAll(value, "-", "")
	return value
}

func normalizeDeviceName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastSpace := false
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			builder.WriteRune(character)
			lastSpace = false
			continue
		}
		if !lastSpace {
			builder.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(builder.String())
}

func isSamsungTVMetadata(md DeviceMetadata) bool {
	return md.Kind == DeviceKindTV && strings.EqualFold(md.Manufacturer, "Samsung")
}

// sameDeviceIdentity joins records from SSDP, mDNS, ARP, and the persisted
// inventory without relying on a provider-specific ID. Samsung records can
// change IDs when the provider changes, and a TV can advertise a generic
// AirPlay/mDNS record alongside its Samsung /api/v2/ record.
func sameDeviceIdentity(a, b DeviceMetadata) bool {
	a = NormalizeMetadata(a)
	b = NormalizeMetadata(b)
	if a.Category != b.Category || (a.Category != CategoryTVDisplay && a.Category != CategoryComputer && a.Category != CategoryConsole) {
		return false
	}
	if left, right := normalizeIdentity(a.DUID), normalizeIdentity(b.DUID); left != "" && left == right {
		return true
	}
	if left, right := normalizeIdentity(a.DUID), normalizeIdentity(b.DUID); left != "" && right != "" && left != right {
		return false
	}
	if left, right := normalizeIdentity(a.MAC), normalizeIdentity(b.MAC); left != "" && left == right {
		return true
	}
	if left, right := normalizeIdentity(a.MAC), normalizeIdentity(b.MAC); left != "" && right != "" && left != right {
		return false
	}
	leftName, rightName := normalizeDeviceName(a.Name), normalizeDeviceName(b.Name)
	if leftName != "" && leftName != "samsung tv" && !strings.HasPrefix(leftName, "upnp device ") && leftName == rightName && (a.IP == "" || b.IP == "" || a.IP == b.IP || isSamsungTVMetadata(a) || isSamsungTVMetadata(b)) {
		return true
	}
	return a.IP != "" && a.IP == b.IP
}

func mergeMetadata(current, incoming DeviceMetadata) DeviceMetadata {
	current = NormalizeMetadata(current)
	incoming = NormalizeMetadata(incoming)
	winner := current
	if metadataRank(incoming) > metadataRank(current) || (metadataRank(incoming) == metadataRank(current) && incoming.LastSeen.After(current.LastSeen)) {
		winner = incoming
	}
	// A generic AirPlay/UPnP record can be newer than the last Samsung API
	// response. Keep the Samsung adapter identity, but use the newest address
	// so the next command does not target a stale DHCP lease.
	latest := current
	if incoming.LastSeen.After(current.LastSeen) {
		latest = incoming
	}
	if latest.IP != "" {
		winner.IP = latest.IP
		winner.Online = latest.Online
		winner.LastSeen = latest.LastSeen
	}
	if isSamsungTVMetadata(current) || isSamsungTVMetadata(incoming) {
		identity := current
		if !isSamsungTVMetadata(identity) {
			identity = incoming
		}
		winner.ID = identity.ID
		winner.Kind = DeviceKindTV
		winner.Manufacturer = "Samsung"
		winner.Name = firstNonEmpty(identity.Name, current.Name, incoming.Name)
		winner.Alias = firstNonEmpty(current.Alias, incoming.Alias)
		winner.Model = firstNonEmpty(identity.Model, current.Model, incoming.Model)
		winner.MAC = firstNonEmpty(identity.MAC, current.MAC, incoming.MAC)
		winner.DUID = firstNonEmpty(identity.DUID, current.DUID, incoming.DUID)
		winner.Paired = current.Paired || incoming.Paired
		winner.ControlVerified = current.ControlVerified || incoming.ControlVerified
		if len(identity.Capabilities) > 0 && !containsUnsupportedOnly(identity.Capabilities) {
			winner.Capabilities = identity.Capabilities
		}
	}
	// Keep stable access state and the best identity fields when a computer or
	// console is seen through more than one provider (for example host + mDNS,
	// or ARP + a Windows service probe).
	winner.Paired = current.Paired || incoming.Paired
	winner.ControlVerified = current.ControlVerified || incoming.ControlVerified
	winner.Name = firstNonEmpty(winner.Name, current.Name, incoming.Name)
	winner.Alias = firstNonEmpty(current.Alias, incoming.Alias)
	winner.Model = firstNonEmpty(winner.Model, current.Model, incoming.Model)
	winner.Manufacturer = firstNonEmpty(winner.Manufacturer, current.Manufacturer, incoming.Manufacturer)
	winner.Platform = firstNonEmpty(winner.Platform, current.Platform, incoming.Platform)
	winner.MAC = firstNonEmpty(winner.MAC, current.MAC, incoming.MAC)
	winner.DUID = firstNonEmpty(winner.DUID, current.DUID, incoming.DUID)
	if containsUnsupportedOnly(winner.Capabilities) {
		if len(current.Capabilities) > 0 && !containsUnsupportedOnly(current.Capabilities) {
			winner.Capabilities = current.Capabilities
		} else if len(incoming.Capabilities) > 0 && !containsUnsupportedOnly(incoming.Capabilities) {
			winner.Capabilities = incoming.Capabilities
		}
	}
	return NormalizeMetadata(winner)
}

func containsUnsupportedOnly(capabilities []Capability) bool {
	return len(capabilities) == 1 && capabilities[0] == CapabilityUnsupported
}

func (m *Manager) Scan(ctx context.Context) ([]DiscoveredDevice, error) {
	// Discovery providers use shared UDP sockets and OS service browsers. Keep
	// automatic scans, manual scans, and pairing refreshes from racing each
	// other and overwriting a freshly discovered address with an older result.
	m.scanMu.Lock()
	defer m.scanMu.Unlock()

	allByID := make(map[DeviceID]DiscoveredDevice)
	var firstErr error
	seen := make(map[DeviceID]bool)
	visibility := m.visibility()
	type providerResult struct {
		name  string
		found []DiscoveredDevice
		err   error
	}
	results := make(chan providerResult, len(m.providers))
	var queries sync.WaitGroup
	for _, provider := range m.providers {
		queries.Add(1)
		go func(provider DiscoveryProvider) {
			defer queries.Done()
			found, err := provider.Discover(ctx)
			results <- providerResult{name: provider.Name(), found: found, err: err}
		}(provider)
	}
	go func() {
		queries.Wait()
		close(results)
	}()
	for providerResult := range results {
		found, err := providerResult.found, providerResult.err
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s discovery: %w", providerResult.name, err)
		}
		for _, item := range found {
			if item.Metadata.ID == "" {
				continue
			}
			item.Metadata = NormalizeMetadata(item.Metadata)
			item = m.reconcileDiscovery(item)
			item.Metadata.LastSeen = time.Now().UTC()
			item.Metadata.Online = true
			seen[item.Metadata.ID] = true
			m.register(ctx, item)
			// register may replace discovery defaults with the capabilities and
			// pairing state of the concrete adapter. Return that normalized
			// metadata to API/CLI callers as well as persisting it.
			m.mu.RLock()
			if current, ok := m.metadata[item.Metadata.ID]; ok {
				item.Metadata = current
			}
			m.mu.RUnlock()
			if !visibleCategory(visibility, item.Metadata) {
				continue
			}
			allByID[item.Metadata.ID] = item
		}
	}
	m.markUnseen(ctx, seen)
	all := make([]DiscoveredDevice, 0, len(allByID))
	for _, item := range allByID {
		all = append(all, item)
	}
	if len(all) == 0 && firstErr != nil {
		// Discovery can fail transiently while a remembered inventory is still
		// useful. Return that inventory instead of making the dashboard appear
		// empty; only return the provider error when there is no known device at
		// all.
		if inventory := m.inventoryResults(); len(inventory) > 0 {
			return inventory, nil
		}
		return nil, firstErr
	}
	// A scan is a refresh of the inventory, not a replacement for it. Include
	// remembered offline records in the response so the CLI and dashboard show
	// the same complete list immediately after a scan.
	return m.inventoryResults(), nil
}

func (m *Manager) inventoryResults() []DiscoveredDevice {
	items := m.List()
	result := make([]DiscoveredDevice, 0, len(items))
	for _, md := range items {
		result = append(result, DiscoveredDevice{Metadata: md, Source: md.Discovery})
	}
	return result
}

func deduplicateScanResults(items []DiscoveredDevice) []DiscoveredDevice {
	byIdentity := make(map[string]DiscoveredDevice)
	byIP := make(map[string]DiscoveredDevice)
	var withoutIP []DiscoveredDevice
	for _, item := range items {
		if key := metadataIdentityKey(item.Metadata); key != "" {
			if current, exists := byIdentity[key]; !exists {
				byIdentity[key] = item
			} else {
				merged := mergeMetadata(current.Metadata, item.Metadata)
				current.Metadata = merged
				byIdentity[key] = current
			}
			continue
		}
		if item.Metadata.IP == "" {
			withoutIP = append(withoutIP, item)
			continue
		}
		if current, exists := byIP[item.Metadata.IP]; !exists || metadataRank(item.Metadata) > metadataRank(current.Metadata) || (metadataRank(item.Metadata) == metadataRank(current.Metadata) && item.Metadata.LastSeen.After(current.Metadata.LastSeen)) {
			byIP[item.Metadata.IP] = item
		}
	}
	for _, item := range byIdentity {
		if item.Metadata.IP == "" {
			continue
		}
		if current, exists := byIP[item.Metadata.IP]; !exists || metadataRank(item.Metadata) > metadataRank(current.Metadata) || (metadataRank(item.Metadata) == metadataRank(current.Metadata) && item.Metadata.LastSeen.After(current.Metadata.LastSeen)) {
			byIP[item.Metadata.IP] = item
		}
	}
	result := append([]DiscoveredDevice(nil), withoutIP...)
	for _, item := range byIP {
		result = append(result, item)
	}
	return collapseDiscoveredResults(result)
}

func collapseDiscoveredResults(items []DiscoveredDevice) []DiscoveredDevice {
	result := append([]DiscoveredDevice(nil), items...)
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); {
			if sameDeviceIdentity(result[i].Metadata, result[j].Metadata) {
				result[i].Metadata = mergeMetadata(result[i].Metadata, result[j].Metadata)
				result = append(result[:j], result[j+1:]...)
				continue
			}
			j++
		}
	}
	return result
}

func (m *Manager) visibility() InventoryVisibility {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return InventoryVisibility{ShowDisplayDevices: m.showDisplays, ShowConsoleDevices: m.showConsoles, ShowComputerDevices: m.showComputers, ShowOfflineDevices: m.showOffline}
}

func visibleCategory(settings InventoryVisibility, md DeviceMetadata) bool {
	md = NormalizeMetadata(md)
	switch md.Category {
	case CategoryTVDisplay:
		return settings.ShowDisplayDevices
	case CategoryComputer:
		return settings.ShowComputerDevices
	case CategoryConsole:
		// Consoles, speakers, phones, generic Linux hosts, and anonymous LAN
		// peers are intentionally not part of this release's inventory.
		return false
	default:
		// Unknown ARP peers, audio devices, phones, and tablets remain internal
		// discovery evidence; they are not presented as user-facing devices.
		return false
	}
}

func isMobileMetadata(md DeviceMetadata) bool {
	if md.Kind == DeviceKindMobile {
		return true
	}
	if md.Kind == DeviceKindTV || md.Kind == DeviceKindMonitor {
		return false
	}
	text := strings.ToLower(strings.Join([]string{md.Manufacturer, md.Model, md.Name, md.Discovery}, " "))
	for _, marker := range []string{"iphone", "ipad", "android phone", "android mobile", "pixel", "galaxy", "oneplus", "xiaomi", "redmi", "mobile phone"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func isMacOSMetadata(md DeviceMetadata) bool {
	text := strings.ToLower(strings.Join([]string{md.Manufacturer, md.Model, md.Name, md.Discovery}, " "))
	return md.Discovery != "arp" && md.Kind == DeviceKindComputer && (strings.EqualFold(md.Manufacturer, "Apple") || strings.EqualFold(md.Model, "macOS") || strings.Contains(text, "macbook") || strings.Contains(text, "mac mini") || strings.Contains(text, "mac-mini") || strings.Contains(text, "sftp-ssh") || strings.Contains(text, "screen-sharing"))
}

func isLinuxMetadata(md DeviceMetadata) bool {
	text := strings.ToLower(strings.Join([]string{md.Manufacturer, md.Model, md.Name, md.Discovery}, " "))
	return md.Kind == DeviceKindComputer && (strings.Contains(text, "raspberry pi") || strings.Contains(text, "raspberrypi") || strings.Contains(text, "raspbian"))
}

func isRaspberryPiMetadata(md DeviceMetadata) bool { return isLinuxMetadata(md) }

func isWindowsLaptopMetadata(md DeviceMetadata) bool {
	if md.Kind != DeviceKindComputer {
		return false
	}
	text := strings.ToLower(strings.Join([]string{md.Manufacturer, md.Model, md.Name, md.Discovery}, " "))
	if !strings.Contains(text, "windows") && !strings.Contains(text, "win32") && !strings.Contains(text, "microsoft") {
		return false
	}
	for _, marker := range []string{"laptop", "notebook", "thinkpad", "latitude", "xps", "surface laptop", "surface pro", "vivobook", "zenbook", "ideapad", "elitebook", "spectre", "gram", "swift", "yoga"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func isWindowsMetadata(md DeviceMetadata) bool {
	if md.Kind != DeviceKindComputer {
		return false
	}
	text := strings.ToLower(strings.Join([]string{md.Manufacturer, md.Model, md.Name, md.Discovery}, " "))
	return strings.Contains(text, "windows") || strings.Contains(text, "win32") || strings.Contains(text, "microsoft")
}

func (m *Manager) reconcileDiscovery(item DiscoveredDevice) DiscoveredDevice {
	if item.Metadata.IP == "" {
		return item
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var candidate DeviceMetadata
	var candidateID DeviceID
	for id, existing := range m.metadata {
		if sameDeviceIdentity(existing, item.Metadata) {
			if candidateID != "" && (candidate.Paired && !existing.Paired || metadataRank(existing) < metadataRank(candidate) || (metadataRank(existing) == metadataRank(candidate) && existing.LastSeen.Before(candidate.LastSeen))) {
				continue
			}
			candidate = existing
			candidateID = id
		}
	}
	if candidateID == "" {
		return item
	}
	discoveredIP := item.Metadata.IP
	item.Metadata.ID = candidateID
	if isSamsungTVMetadata(item.Metadata) {
		item.Metadata.Paired = item.Metadata.Paired || candidate.Paired
		item.Metadata.ControlVerified = item.Metadata.ControlVerified || candidate.ControlVerified
		item.Metadata.MAC = firstNonEmpty(item.Metadata.MAC, candidate.MAC)
		item.Metadata.DUID = firstNonEmpty(item.Metadata.DUID, candidate.DUID)
		item.Metadata.Firmware = firstNonEmpty(item.Metadata.Firmware, candidate.Firmware)
		if item.Metadata.Name == "" {
			item.Metadata.Name = candidate.Name
		}
		item.Metadata.Alias = candidate.Alias
		if item.Metadata.Model == "" {
			item.Metadata.Model = candidate.Model
		}
		if len(item.Metadata.Capabilities) == 0 || containsUnsupportedOnly(item.Metadata.Capabilities) {
			item.Metadata.Capabilities = candidate.Capabilities
		}
		return item
	}
	item.Metadata = mergeMetadata(candidate, item.Metadata)
	item.Metadata.ID = candidateID
	if discoveredIP != "" {
		item.Metadata.IP = discoveredIP
		item.Metadata.Online = true
	}
	item.Metadata.Kind = candidate.Kind
	item.Metadata.Category = candidate.Category
	item.Metadata.Manufacturer = firstNonEmpty(candidate.Manufacturer, item.Metadata.Manufacturer)
	item.Metadata.Model = firstNonEmpty(candidate.Model, item.Metadata.Model)
	item.Metadata.MAC = firstNonEmpty(item.Metadata.MAC, candidate.MAC)
	item.Metadata.DUID = firstNonEmpty(item.Metadata.DUID, candidate.DUID)
	item.Metadata.Firmware = firstNonEmpty(item.Metadata.Firmware, candidate.Firmware)
	item.Metadata.Paired = item.Metadata.Paired || candidate.Paired
	item.Metadata.ControlVerified = item.Metadata.ControlVerified || candidate.ControlVerified
	if len(item.Metadata.Capabilities) == 0 || containsUnsupportedOnly(item.Metadata.Capabilities) {
		item.Metadata.Capabilities = candidate.Capabilities
	}
	if candidate.Name != "" {
		item.Metadata.Name = candidate.Name
	}
	if candidate.Discovery == "host" {
		item.Metadata.Discovery = "host"
	}
	return item
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (m *Manager) hasFactory(item DiscoveredDevice) bool {
	for _, factory := range m.factories {
		if factory.Supports(item) {
			return true
		}
	}
	return false
}

func (m *Manager) markUnseen(ctx context.Context, seen map[DeviceID]bool) {
	m.mu.Lock()
	var stale []DeviceMetadata
	for id, md := range m.metadata {
		if md.Online && !seen[id] {
			md.Online = false
			m.metadata[id] = md
			stale = append(stale, md)
		}
	}
	m.mu.Unlock()
	for _, md := range stale {
		_ = m.store.SaveDevice(ctx, md)
	}
}

func (m *Manager) register(ctx context.Context, item DiscoveredDevice) {
	m.mu.Lock()
	old, exists := m.metadata[item.Metadata.ID]
	if item.Metadata.Discovery == "" {
		item.Metadata.Discovery = item.Source
	}
	if exists {
		item.Metadata.Paired = item.Metadata.Paired || old.Paired
		item.Metadata.ControlVerified = item.Metadata.ControlVerified || old.ControlVerified
	}
	// A single TV may have a Samsung, mDNS, and ARP record at the same time.
	// Carry the canonical pairing state across those records before creating a
	// fresh adapter. Explicit unpairing/invalid-token handling clears every
	// matching record through setPairingState, so this does not resurrect a
	// pairing that was deliberately invalidated.
	for id, existing := range m.metadata {
		if id != item.Metadata.ID && existing.Paired && sameDeviceIdentity(existing, item.Metadata) {
			item.Metadata.Paired = true
			break
		}
	}
	if item.Source == "host" && item.Metadata.Manufacturer == "Apple" && item.Metadata.Model == "macOS" {
		item.Metadata.Paired = true
	}
	if old.Name != "" && item.Metadata.Name == "" {
		item.Metadata.Name = old.Name
	}
	if old.Alias != "" && item.Metadata.Alias == "" {
		item.Metadata.Alias = old.Alias
	}
	m.metadata[item.Metadata.ID] = item.Metadata
	for _, factory := range m.factories {
		if !factory.Supports(item) {
			continue
		}
		if device, err := factory.Create(ctx, item); err == nil {
			item.Metadata.Capabilities = device.Capabilities()
			m.metadata[item.Metadata.ID] = item.Metadata
			m.devices[item.Metadata.ID] = device
		}
		break
	}
	m.mu.Unlock()
	_ = m.store.SaveDevice(ctx, item.Metadata)
	if !exists || old.IP != item.Metadata.IP || old.Online != item.Metadata.Online {
		select {
		case m.events <- item.Metadata:
		default:
		}
	}
}

func (m *Manager) device(id DeviceID) (Device, DeviceMetadata, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.devices[id]
	md := m.metadata[id]
	if !ok {
		return nil, md, ErrDeviceNotFound
	}
	return d, md, nil
}

// ResolveDeviceReference accepts a stable device ID, a saved alias, or the
// original discovered name. Names must resolve uniquely so a natural-language
// agent command can never target the wrong device by accident.
func (m *Manager) ResolveDeviceReference(reference string) (DeviceID, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return "", errors.New("device reference is required")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for id := range m.metadata {
		if string(id) == reference || strings.EqualFold(string(id), reference) {
			return id, nil
		}
	}
	normalized := normalizeDeviceName(reference)
	var match DeviceID
	for id, md := range m.metadata {
		for _, candidate := range []string{md.Alias, md.Name} {
			if strings.EqualFold(strings.TrimSpace(candidate), reference) || (normalized != "" && normalizeDeviceName(candidate) == normalized) {
				if match != "" && match != id {
					return "", fmt.Errorf("device reference %q is ambiguous; use its ID or a unique alias", reference)
				}
				match = id
			}
		}
	}
	if match == "" {
		return "", fmt.Errorf("device %q not found; scan the network and list devices again", reference)
	}
	return match, nil
}

// Rename stores a friendly operator name without changing the provider ID or
// the original discovery name. The alias must be unique across the inventory.
func (m *Manager) Rename(ctx context.Context, reference, alias string) error {
	id, err := m.ResolveDeviceReference(reference)
	if err != nil {
		return err
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return errors.New("device name cannot be empty")
	}
	if len([]rune(alias)) > 64 {
		return errors.New("device name must be 64 characters or fewer")
	}
	normalized := normalizeDeviceName(alias)
	m.mu.Lock()
	for otherID, md := range m.metadata {
		if otherID == id {
			continue
		}
		for _, candidate := range []string{md.Alias, md.Name} {
			if normalized != "" && normalizeDeviceName(candidate) == normalized {
				m.mu.Unlock()
				return fmt.Errorf("device name %q is already used by %s", alias, DisplayName(md))
			}
		}
	}
	md, ok := m.metadata[id]
	if !ok {
		m.mu.Unlock()
		return ErrDeviceNotFound
	}
	md.Alias = alias
	m.metadata[id] = md
	m.mu.Unlock()
	return m.store.SaveDevice(ctx, md)
}

func (m *Manager) Pair(ctx context.Context, id DeviceID, options ...PairOptions) error {
	d, md, err := m.device(id)
	refreshed := false
	if err != nil {
		if md.ID != "" && isSamsungTVMetadata(md) {
			// A CLI user may have enabled the TV setting immediately before
			// pairing. Refresh once here so pairing is self-healing instead of
			// requiring a separate discover command.
			_, _ = m.Scan(ctx)
			refreshed = true
			if refreshed, refreshedMetadata, refreshedErr := m.device(id); refreshedErr == nil {
				d, md, err = refreshed, refreshedMetadata, nil
			}
			if err != nil {
				return errors.New("Samsung local control was not verified; wake the TV, enable its network remote setting, and scan again before pairing")
			}
		}
		if err != nil {
			return err
		}
	}
	if isSamsungTVMetadata(md) && !refreshed {
		// Always refresh a Samsung record immediately before pairing. This
		// reconciles DHCP changes and verifies that the TV's local service is
		// reachable in the same operation, instead of pairing against a stale
		// address left by an earlier scan.
		_, _ = m.Scan(ctx)
		if refreshed, refreshedMetadata, refreshedErr := m.device(id); refreshedErr == nil {
			d, md = refreshed, refreshedMetadata
		}
		if !md.ControlVerified {
			return errors.New("Samsung local control was not verified; wake the TV, enable its network remote setting, and scan again before pairing")
		}
	}
	pairErr := m.pairDevice(ctx, d, options...)
	if pairErr != nil && shouldRediscoverForPair(pairErr) {
		// A TV can keep its identity while receiving a new DHCP address. Refresh
		// discovery once before reporting a stale-IP failure to the user.
		_, _ = m.Scan(ctx)
		if refreshed, refreshedMetadata, refreshedErr := m.device(id); refreshedErr == nil {
			d, md, pairErr = refreshed, refreshedMetadata, m.pairDevice(ctx, refreshed, options...)
		}
	}
	if pairErr != nil {
		if shouldRediscoverForPair(pairErr) {
			return formatPairReachabilityError(md, pairErr)
		}
		return pairErr
	}
	md = d.Metadata()
	md.Paired = true
	md.Capabilities = d.Capabilities()
	return m.setPairingState(ctx, md, true)
}

func (m *Manager) pairDevice(ctx context.Context, d Device, options ...PairOptions) error {
	if pairer, ok := d.(Pairer); ok {
		var opts PairOptions
		if len(options) > 0 {
			opts = options[0]
		}
		return pairer.PairWith(ctx, opts)
	}
	return d.Pair(ctx)
}

func shouldRediscoverForPair(err error) bool {
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"no route to host", "network is unreachable", "connection refused", "i/o timeout", "timeout awaiting response"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func formatPairReachabilityError(md DeviceMetadata, err error) error {
	message := strings.ToLower(err.Error())
	if md.Manufacturer == "Samsung" && md.Kind == DeviceKindTV {
		switch {
		case strings.Contains(message, "connection refused"):
			return fmt.Errorf("Samsung TV at %s rejected the local pairing connection; turn the TV on and enable its mobile/device connection or network remote setting, then pair again: %w", md.IP, err)
		case strings.Contains(message, "no route to host"), strings.Contains(message, "network is unreachable"):
			return fmt.Errorf("Samsung TV at %s is unreachable from the bridge; confirm the TV and bridge share the same non-guest LAN, then run Scan network and pair again: %w", md.IP, err)
		default:
			return fmt.Errorf("Samsung TV at %s did not answer pairing; confirm it is on, network remote access is enabled, and the address is current: %w", md.IP, err)
		}
	}
	return fmt.Errorf("device %q at %s is unreachable from the bridge; confirm it is on the same LAN and run Scan network again: %w", DisplayName(md), md.IP, err)
}

func (m *Manager) Unpair(ctx context.Context, id DeviceID) error {
	d, md, err := m.device(id)
	if err != nil {
		return err
	}
	unpairer, ok := d.(Unpairer)
	if !ok {
		return fmt.Errorf("device %q does not support pairing", id)
	}
	if err := unpairer.Unpair(ctx); err != nil {
		return err
	}
	md = d.Metadata()
	md.Paired = false
	md.Capabilities = d.Capabilities()
	return m.setPairingState(ctx, md, false)
}

func (m *Manager) State(ctx context.Context, id DeviceID) (DeviceState, error) {
	d, _, err := m.device(id)
	if err != nil {
		return DeviceState{DeviceID: id, Error: err.Error()}, err
	}
	state, stateErr := d.State(ctx)
	if stateErr == nil || !shouldRediscoverForPair(stateErr) {
		return state, stateErr
	}
	// A saved TV record can outlive its DHCP address. Reconcile discovery once
	// before declaring the device unavailable; this also recreates the adapter
	// with the current address for the next command.
	if _, scanErr := m.Scan(ctx); scanErr != nil {
		return state, stateErr
	}
	if refreshed, _, refreshErr := m.device(id); refreshErr == nil {
		return refreshed.State(ctx)
	}
	return state, stateErr
}

func (m *Manager) Execute(ctx context.Context, cmd Command) (CommandResult, error) {
	d, _, err := m.device(cmd.DeviceID)
	if err != nil {
		_ = m.store.Audit(ctx, cmd, false, err.Error())
		return CommandResult{}, err
	}
	if cmd.Action == ActionPowerOn {
		// Wake-on-LAN is sent to the last known address. Refresh first so a
		// DHCP change does not silently wake the wrong address and then leave
		// the adapter using stale metadata.
		if _, scanErr := m.scanForPowerRecovery(ctx); scanErr == nil {
			if refreshed, _, refreshErr := m.device(cmd.DeviceID); refreshErr == nil {
				d = refreshed
			}
		}
	}
	result, err := d.Execute(ctx, cmd)
	if err != nil && !(cmd.Action == ActionPowerOn && isWakeConfirmationError(err)) && shouldRediscoverForPair(err) {
		// Retry once after a scan so commands follow a TV that changed DHCP
		// address. The retry is limited to reachability failures and never
		// repeats a command after an authorization or validation error.
		if _, scanErr := m.Scan(ctx); scanErr == nil {
			if refreshed, _, refreshErr := m.device(cmd.DeviceID); refreshErr == nil {
				d = refreshed
				result, err = d.Execute(ctx, cmd)
			}
		}
	}
	m.syncAdapterMetadata(ctx, d)
	auditMessage := result.Message
	if err != nil {
		// Failed commands often have no CommandResult. Preserve the actual
		// failure in the command center instead of recording an empty event.
		auditMessage = err.Error()
	}
	_ = m.store.Audit(ctx, cmd, err == nil, auditMessage)
	return result, err
}

func isWakeConfirmationError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "wake-on-lan was sent, but the tv did not become reachable")
}

func (m *Manager) scanForPowerRecovery(ctx context.Context) ([]DiscoveredDevice, error) {
	scanCtx, cancel := context.WithTimeout(ctx, powerRecoveryScanTimeout)
	defer cancel()
	return m.Scan(scanCtx)
}

func (m *Manager) syncAdapterMetadata(ctx context.Context, d Device) {
	md := d.Metadata()
	if md.ID == "" {
		return
	}
	_ = m.setPairingState(ctx, md, md.Paired)
}

// setPairingState is the single write path for pairing state. It updates all
// provider records that identify the same Samsung TV, which prevents a later
// background discovery scan from replacing an explicit unpair or expired-token
// result with an older duplicate record.
func (m *Manager) setPairingState(ctx context.Context, md DeviceMetadata, paired bool) error {
	md.Paired = paired
	updates := make([]DeviceMetadata, 0, 2)
	m.mu.Lock()
	if existing, exists := m.metadata[md.ID]; exists && md.Alias == "" {
		md.Alias = existing.Alias
	}
	for id, existing := range m.metadata {
		if id != md.ID && !sameDeviceIdentity(existing, md) {
			continue
		}
		if id == md.ID {
			existing = md
		} else {
			existing.Paired = paired
		}
		m.metadata[id] = existing
		updates = append(updates, existing)
	}
	if _, exists := m.metadata[md.ID]; !exists {
		m.metadata[md.ID] = md
		updates = append(updates, md)
	}
	m.mu.Unlock()

	var firstErr error
	for _, update := range updates {
		if err := m.store.SaveDevice(ctx, update); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *Manager) Audit(ctx context.Context, limit int) ([]AuditEvent, error) {
	reader, ok := m.store.(AuditReader)
	if !ok {
		return []AuditEvent{}, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return reader.ListAudit(ctx, limit)
}
