package discovery

import (
	"bufio"
	"context"
	"io"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/mdns"
	"github.com/local-device-bridge/local-device-bridge/internal/core"
)

type MDNSProvider struct {
	Timeout    time.Duration
	Services   []string
	Interfaces []string
}

func (p *MDNSProvider) Name() string { return "mdns" }

func (p *MDNSProvider) Discover(ctx context.Context) ([]core.DiscoveredDevice, error) {
	if runtime.GOOS == "darwin" {
		// dns-sd is the most complete browser on macOS, but the Go Bonjour
		// client can still see services that dns-sd misses (and vice versa).
		// Always merge both views instead of returning as soon as one browser
		// reports a result. This is important for quiet Macs, iPhones, TVs, and
		// displays that advertise only one of several Bonjour service types.
		type result struct {
			devices []core.DiscoveredDevice
			err     error
		}
		results := make(chan result, 2)
		go func() {
			devices, err := p.discoverDarwin(ctx)
			results <- result{devices: devices, err: err}
		}()
		go func() {
			devices, err := p.discoverHashicorp(ctx)
			results <- result{devices: devices, err: err}
		}()
		var merged []core.DiscoveredDevice
		var firstErr error
		for range 2 {
			found := <-results
			if found.err != nil && firstErr == nil {
				firstErr = found.err
			}
			merged = mergeMDNSResults(merged, found.devices)
		}
		if len(merged) > 0 {
			return merged, nil
		}
		if firstErr != nil {
			return nil, firstErr
		}
	}
	return p.discoverHashicorp(ctx)
}

func mergeMDNSResults(existing []core.DiscoveredDevice, incoming []core.DiscoveredDevice) []core.DiscoveredDevice {
	positions := make(map[core.DeviceID]int, len(existing))
	for index, device := range existing {
		positions[device.Metadata.ID] = index
	}
	for _, device := range incoming {
		existing = mergeMDNSDevice(existing, positions, device)
	}
	return existing
}

func (p *MDNSProvider) discoverHashicorp(ctx context.Context) ([]core.DiscoveredDevice, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	services := p.Services
	if len(services) == 0 {
		// Apple identity services go first so a Mac is not reduced to a
		// generic workstation record when the same hostname advertises more
		// than one Bonjour service.
		// QueryParam.Service is a DNS-SD service type, not a short label.
		// Using the complete names matters on macOS: bare values such as
		// "device-info" query device-info.local instead of
		// _device-info._tcp.local and return no Macs.
		services = defaultMDNSServices()
	}
	interfaces := mdnsInterfaces(p.Interfaces)
	positions := map[core.DeviceID]int{}
	var result []core.DiscoveredDevice
	type queryResult struct {
		service string
		entries []*mdns.ServiceEntry
	}
	queryCount := len(services) * len(interfaces)
	if queryCount == 0 {
		queryCount = len(services)
	}
	results := make(chan queryResult, queryCount)
	var queries sync.WaitGroup
	for _, service := range services {
		for _, iface := range interfaces {
			queries.Add(1)
			go func(service string, iface *net.Interface) {
				defer queries.Done()
				entries := make(chan *mdns.ServiceEntry, 64)
				params := &mdns.QueryParam{Service: service, Domain: "local", Interface: iface, Entries: entries, Timeout: timeout}
				_ = mdns.Query(params)
				var found []*mdns.ServiceEntry
				for {
					select {
					case entry := <-entries:
						if entry != nil {
							found = append(found, entry)
						}
					default:
						results <- queryResult{service: service, entries: found}
						return
					}
				}
			}(service, iface)
		}
	}
	go func() {
		queries.Wait()
		close(results)
	}()
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case query, ok := <-results:
			if !ok {
				return result, nil
			}
			for _, entry := range query.entries {
				if entry.AddrV4 == nil {
					continue
				}
				name := strings.TrimSuffix(entry.Name, ".")
				mac := mdnsMAC(name)
				name = mdnsDisplayName(name)
				fields := append(append([]string(nil), entry.InfoFields...), strings.Fields(entry.Info)...)
				result = mergeMDNSDevice(result, positions, makeMDNSDeviceWithTXT(query.service, name, entry.AddrV4.String(), mac, fields))
			}
		}
	}
}

func (p *MDNSProvider) discoverDarwin(ctx context.Context) ([]core.DiscoveredDevice, error) {
	services := p.Services
	if len(services) == 0 {
		services = defaultMDNSServices()
	}
	timeout := p.Timeout
	if timeout <= 0 || timeout > 2*time.Second {
		timeout = 2 * time.Second
	}
	type browseResult struct {
		service string
		names   []string
	}
	results := make(chan browseResult, len(services))
	var queries sync.WaitGroup
	for _, service := range services {
		queries.Add(1)
		go func(service string) {
			defer queries.Done()
			queryCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			output, _ := exec.CommandContext(queryCtx, "dns-sd", "-B", service, "local").CombinedOutput()
			results <- browseResult{service: service, names: parseDNSBrowse(string(output))}
		}(service)
	}
	go func() {
		queries.Wait()
		close(results)
	}()

	type resolveRequest struct {
		service string
		name    string
	}
	var requests []resolveRequest
	for browse := range results {
		for _, name := range browse.names {
			requests = append(requests, resolveRequest{service: browse.service, name: name})
		}
	}
	type resolveResult struct {
		service string
		name    string
		ip      string
	}
	resolved := make(chan resolveResult, len(requests))
	var resolvers sync.WaitGroup
	for _, request := range requests {
		resolvers.Add(1)
		go func(request resolveRequest) {
			defer resolvers.Done()
			host := lookupDNSService(ctx, timeout, request.name, request.service)
			if host == "" {
				return
			}
			ip := resolveDNSHost(ctx, host, timeout)
			if ip != "" {
				resolved <- resolveResult{service: request.service, name: request.name, ip: ip}
			}
		}(request)
	}
	go func() {
		resolvers.Wait()
		close(resolved)
	}()
	positions := map[core.DeviceID]int{}
	var devices []core.DiscoveredDevice
	for item := range resolved {
		mac := mdnsMAC(item.name)
		cleanName := mdnsDisplayName(strings.TrimSuffix(item.name, "."))
		devices = mergeMDNSDevice(devices, positions, makeMDNSDevice(item.service, cleanName, item.ip, mac))
	}
	return devices, nil
}

func defaultMDNSServices() []string {
	return []string{
		"_device-info._tcp", "_companion-link._tcp", "_workstation._tcp",
		"_ssh._tcp", "_sftp-ssh._tcp", "_smb._tcp", "_http._tcp",
		"_airplay._tcp", "_raop._tcp", "_rfb._tcp", "_screen-sharing._tcp",
		"_googlecast._tcp", "_webostv._tcp",
		"_ps4._tcp", "_playstation._tcp", "_xbox._tcp",
	}
}

func mdnsInterfaces(names []string) []*net.Interface {
	networks := LocalIPv4Networks(names)
	result := make([]*net.Interface, 0, len(networks))
	seen := make(map[string]struct{})
	for _, network := range networks {
		iface := network.Interface
		if _, ok := seen[iface.Name]; ok {
			continue
		}
		seen[iface.Name] = struct{}{}
		copy := iface
		result = append(result, &copy)
	}
	if len(result) == 0 {
		// Let the library choose the system default if the host has no usable
		// IPv4 interface or the caller supplied an unavailable interface name.
		return []*net.Interface{nil}
	}
	return result
}

func mergeMDNSDevice(devices []core.DiscoveredDevice, positions map[core.DeviceID]int, device core.DiscoveredDevice) []core.DiscoveredDevice {
	id := device.Metadata.ID
	if index, ok := positions[id]; ok {
		if mdnsDevicePriority(device) > mdnsDevicePriority(devices[index]) {
			devices[index] = device
		}
		return devices
	}
	positions[id] = len(devices)
	return append(devices, device)
}

func mdnsDevicePriority(device core.DiscoveredDevice) int {
	quality := 0
	if device.Metadata.Manufacturer != "" {
		quality += 3
	}
	if device.Metadata.MAC != "" {
		quality += 2
	}
	if device.Metadata.Model != "" && device.Metadata.Model != "airplay" && device.Metadata.Model != "Bonjour service" {
		quality++
	}
	if device.Metadata.Kind == core.DeviceKindMobile {
		return 50 + quality
	}
	if device.Metadata.Kind == core.DeviceKindComputer && strings.EqualFold(device.Metadata.Manufacturer, "Apple") {
		return 40 + quality
	}
	if device.Metadata.Kind == core.DeviceKindComputer {
		return 30 + quality
	}
	if device.Metadata.Kind == core.DeviceKindTV || device.Metadata.Kind == core.DeviceKindMonitor {
		return 20 + quality
	}
	return 10 + quality
}

func parseDNSBrowse(output string) []string {
	var names []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 || fields[1] != "Add" {
			continue
		}
		name := strings.Join(fields[6:], " ")
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func parseDNSLookupHost(output string) string {
	for _, line := range strings.Split(output, "\n") {
		marker := " can be reached at "
		index := strings.Index(line, marker)
		if index < 0 {
			continue
		}
		value := strings.TrimSpace(line[index+len(marker):])
		if end := strings.IndexAny(value, " \t"); end >= 0 {
			value = value[:end]
		}
		if colon := strings.LastIndex(value, ":"); colon > 0 {
			return strings.TrimSuffix(value[:colon], ".")
		}
	}
	return ""
}

func resolveDNSHost(ctx context.Context, host string, timeout time.Duration) string {
	return runDNSCommand(ctx, timeout, []string{"-G", "v4", host}, func(line string) (string, bool) {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[1] != "Add" {
			return "", false
		}
		candidate := net.ParseIP(fields[5]).To4()
		if candidate != nil && !candidate.IsLoopback() && !candidate.IsLinkLocalUnicast() {
			return candidate.String(), true
		}
		return "", false
	})
}

func lookupDNSService(ctx context.Context, timeout time.Duration, name, service string) string {
	return runDNSCommand(ctx, timeout, []string{"-L", name, service, "local"}, func(line string) (string, bool) {
		host := parseDNSLookupHost(line)
		return host, host != ""
	})
}

func runDNSCommand(ctx context.Context, timeout time.Duration, args []string, parse func(string) (string, bool)) string {
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(queryCtx, "dns-sd", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ""
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return ""
	}
	if err := cmd.Start(); err != nil {
		return ""
	}
	lines := make(chan string, 64)
	readLines := func(reader io.ReadCloser) {
		defer reader.Close()
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-queryCtx.Done():
				return
			}
		}
	}
	go readLines(stdout)
	go readLines(stderr)
	for {
		select {
		case line := <-lines:
			if value, ok := parse(line); ok {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return value
			}
		case <-queryCtx.Done():
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return ""
		}
	}
}

func makeMDNSDevice(service, name, ip, mac string) core.DiscoveredDevice {
	return makeMDNSDeviceWithTXT(service, name, ip, mac, nil)
}

func makeMDNSDeviceWithTXT(service, name, ip, mac string, fields []string) core.DiscoveredDevice {
	txt := mdnsTXT(fields)
	if mac == "" {
		mac = normalizeMAC(firstTXT(txt, "deviceid", "device-id", "mac", "macaddress"))
	}
	kind := classifyMDNSKind(service, name)
	identityText := strings.Join([]string{name, txt["manufacturer"], txt["model"], txt["md"]}, " ")
	manufacturer, model := mdnsIdentity(service, identityText, kind, mac)
	if advertised := firstTXT(txt, "model", "md"); advertised != "" {
		model = advertised
	}
	if advertised := txt["manufacturer"]; advertised != "" {
		manufacturer = advertised
	}
	identityLower := strings.ToLower(strings.Join([]string{name, model, manufacturer}, " "))
	if strings.Contains(strings.ToLower(service), "googlecast") && containsAnyDiscovery(identityLower, "speaker", "speaker group", "google home", "nest audio", "home mini", "audio group") {
		kind = core.DeviceKindUnknown
	}
	id := "mdns-" + strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	if mac != "" {
		id += "-" + strings.ReplaceAll(mac, ":", "")
	}
	device := core.DiscoveredDevice{Source: "mdns", Metadata: core.DeviceMetadata{ID: core.DeviceID(id), Kind: kind, Manufacturer: manufacturer, Name: name, Model: model, IP: ip, MAC: mac, Capabilities: []core.Capability{core.CapabilityUnsupported}, Online: true, LastSeen: time.Now().UTC()}}
	device.Metadata = core.NormalizeMetadata(device.Metadata)
	return device
}

func containsAnyDiscovery(value string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func mdnsTXT(fields []string) map[string]string {
	result := make(map[string]string)
	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		result[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return result
}

func firstTXT(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if values[key] != "" {
			return values[key]
		}
	}
	return ""
}

func mdnsIdentity(service, name string, kind core.DeviceKind, mac ...string) (string, string) {
	text := strings.ToLower(service + " " + name)
	appleMAC := len(mac) > 0 && isAppleMAC(mac[0])
	if strings.Contains(text, "raspberry pi") || strings.Contains(text, "raspberrypi") || strings.Contains(text, "raspbian") {
		return "Raspberry Pi", "Raspberry Pi"
	}
	if strings.Contains(text, "bravia") || strings.Contains(text, "sony tv") {
		return "Sony", "BRAVIA"
	}
	if strings.Contains(text, "webos") || strings.Contains(text, "lg tv") || strings.Contains(text, "lg oled") {
		return "LG", "webOS"
	}
	if strings.Contains(text, "samsung") || strings.Contains(text, "tizen") {
		return "Samsung", "Tizen TV"
	}
	if strings.Contains(text, "playstation") || strings.Contains(text, "ps5") || strings.Contains(text, "ps4") {
		return "Sony", "PlayStation"
	}
	if strings.Contains(text, "xbox") {
		return "Microsoft", "Xbox"
	}
	if strings.Contains(text, "nintendo") || strings.Contains(text, "switch") {
		return "Nintendo", "Switch"
	}
	if kind == core.DeviceKindMobile && (strings.Contains(text, "ipad") || strings.Contains(text, "iphone") || strings.Contains(text, "ios") || strings.Contains(text, "apple-mobdev2")) {
		return "Apple", "iOS"
	}
	if kind == core.DeviceKindMobile && looksAndroid(text) {
		return "Google", "Android"
	}
	if strings.Contains(text, "ipad") || strings.Contains(text, "iphone") || strings.Contains(text, "ios") {
		return "Apple", "iOS"
	}
	if kind == core.DeviceKindComputer && (appleMAC || strings.Contains(text, "companion") || strings.Contains(text, "apple") || strings.Contains(text, "mac") || strings.Contains(text, "sftp-ssh") || strings.Contains(text, "screen-sharing") || strings.Contains(text, "apple-mobdev2")) {
		return "Apple", "macOS"
	}
	return "", mdnsServiceLabel(service)
}

func mdnsMAC(name string) string {
	start := strings.LastIndex(name, "[")
	end := strings.LastIndex(name, "]")
	if start < 0 || end <= start {
		return ""
	}
	mac := normalizeMAC(name[start+1 : end])
	if len(strings.ReplaceAll(mac, ":", "")) != 12 {
		return ""
	}
	return mac
}

func mdnsDisplayName(name string) string {
	if start := strings.LastIndex(name, "["); start > 0 && strings.HasSuffix(name, "]") {
		if mdnsMAC(name) != "" {
			return strings.TrimSpace(name[:start])
		}
	}
	return name
}

func mdnsServiceLabel(service string) string {
	service = strings.TrimSuffix(strings.TrimSpace(service), ".")
	service = strings.TrimSuffix(service, ".local")
	service = strings.TrimSuffix(service, "._tcp")
	service = strings.TrimSuffix(service, "._udp")
	service = strings.TrimPrefix(service, "_")
	if service == "" {
		return "Bonjour service"
	}
	return service
}

func classifyMDNSKind(service, name string) core.DeviceKind {
	text := strings.ToLower(service + " " + name)
	switch {
	case strings.Contains(text, "playstation") || strings.Contains(text, "ps5") || strings.Contains(text, "ps4") || strings.Contains(text, "xbox") || strings.Contains(text, "nintendo switch"):
		return core.DeviceKindConsole
	case looksMobile(text):
		return core.DeviceKindMobile
	case strings.Contains(text, "sonos") || strings.Contains(text, "rincon"):
		return core.DeviceKindUnknown
	case strings.Contains(text, "monitor") || strings.Contains(text, "display"):
		return core.DeviceKindMonitor
	case strings.Contains(text, "workstation") || strings.Contains(text, "ssh") || strings.Contains(text, "smb") || strings.Contains(text, "companion") || strings.Contains(text, "device-info") || strings.Contains(text, "mac") || strings.Contains(text, "linux") || strings.Contains(text, "raspberry") || strings.Contains(text, "laptop") || strings.Contains(text, "notebook"):
		return core.DeviceKindComputer
	case strings.Contains(text, "tv") || strings.Contains(text, "cast") || strings.Contains(text, "airplay"):
		return core.DeviceKindTV
	default:
		return core.DeviceKindUnknown
	}
}

func looksMobile(text string) bool {
	for _, marker := range []string{"android", "pixel", "galaxy", "oneplus", "xiaomi", "redmi", "huawei", "oppo", "vivo", "iphone", "ipad", "ios", "mobile", "phone", "apple-mobdev2"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func looksAndroid(text string) bool {
	for _, marker := range []string{"android", "pixel", "galaxy", "oneplus", "xiaomi", "redmi", "huawei", "oppo", "vivo"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
