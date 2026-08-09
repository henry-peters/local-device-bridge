package discovery

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/local-device-bridge/local-device-bridge/internal/core"
)

type SSDPProvider struct {
	Timeout    time.Duration
	HTTPClient *http.Client
	Interfaces []string
}

type ssdpCandidate struct {
	location *url.URL
	headers  map[string]string
}

func (p *SSDPProvider) Name() string { return "ssdp" }

func (p *SSDPProvider) Discover(ctx context.Context) ([]core.DiscoveredDevice, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	message := "M-SEARCH * HTTP/1.1\r\n"
	message += "HOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\nST: ssdp:all\r\n\r\n"
	target := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
	// A wildcard UDP socket follows the system route, which is unreliable on
	// Macs and PCs with VPNs, Ethernet adapters, or a Wi-Fi interface that is
	// not the first interface returned by the OS. Send and receive on every
	// selected directly-connected interface instead.
	networks := LocalIPv4Networks(p.Interfaces)
	if len(networks) == 0 {
		networks = []LocalNetwork{{}}
	}
	type interfaceResult struct {
		candidates map[string]ssdpCandidate
		err        error
	}
	results := make(chan interfaceResult, len(networks))
	for _, network := range networks {
		go func(address net.IP) {
			found, err := discoverSSDPOnInterface(ctx, timeout, message, target, address)
			results <- interfaceResult{candidates: found, err: err}
		}(network.Address)
	}
	candidates := map[string]ssdpCandidate{}
	var firstErr error
	for range networks {
		found := <-results
		if found.err != nil && firstErr == nil {
			firstErr = found.err
		}
		for key, candidate := range found.candidates {
			candidates[key] = candidate
		}
	}
	if len(candidates) == 0 && firstErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	byIP := make(map[string][]core.DiscoveredDevice)
	for _, candidate := range candidates {
		ip := candidate.location.Hostname()
		generic := genericSSDP(ip, candidate.headers)
		generic = p.enrichDescription(ctx, candidate.location, generic)
		byIP[ip] = append(byIP[ip], generic)
	}
	var result []core.DiscoveredDevice
	for ip, devices := range byIP {
		// Public, vendor-documented endpoints provide a stronger identity than
		// a generic MediaRenderer response.
		if device, ok := p.probeSamsung(ctx, ip); ok {
			result = append(result, device)
			continue
		}
		if device, ok := probeRokuAPI(ctx, ip, p.HTTPClient); ok {
			result = append(result, device)
			continue
		}
		best := devices[0]
		for _, candidate := range devices[1:] {
			if discoveryIdentityRank(candidate.Metadata) > discoveryIdentityRank(best.Metadata) {
				best = candidate
			}
		}
		result = append(result, best)
	}
	return result, nil
}

func discoverSSDPOnInterface(ctx context.Context, timeout time.Duration, message string, target *net.UDPAddr, address net.IP) (map[string]ssdpCandidate, error) {
	local := &net.UDPAddr{IP: net.IPv4zero, Port: 0}
	if address != nil && address.To4() != nil {
		local.IP = address.To4()
	}
	conn, err := net.ListenUDP("udp4", local)
	if err != nil {
		return nil, fmt.Errorf("open SSDP socket: %w", err)
	}
	defer conn.Close()
	// Home devices frequently miss a single multicast packet while waking or
	// while their Wi-Fi radio is in power-save mode. Two queries are enough to
	// improve reliability without turning the bridge into a noisy scanner.
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := conn.WriteToUDP([]byte(message), target); err != nil {
			return nil, fmt.Errorf("send SSDP query: %w", err)
		}
	}
	deadline := time.Now().Add(timeout)
	_ = conn.SetReadDeadline(deadline)
	candidates := map[string]ssdpCandidate{}
	for {
		select {
		case <-ctx.Done():
			return candidates, ctx.Err()
		default:
		}
		buf := make([]byte, 8192)
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				return candidates, nil
			}
			return candidates, err
		}
		headers := parseHeaders(string(buf[:n]))
		location := headers["location"]
		if location == "" {
			continue
		}
		u, err := url.Parse(location)
		if err != nil || u.Hostname() == "" {
			continue
		}
		candidate := ssdpCandidate{location: u, headers: headers}
		// A physical TV normally answers with several UPnP services. Keeping only
		// the first reply loses the manufacturer-bearing device description.
		key := strings.ToLower(location + "|" + headers["usn"] + "|" + headers["st"])
		candidates[key] = candidate
	}
}

func discoveryIdentityRank(md core.DeviceMetadata) int {
	rank := 0
	if md.Kind != core.DeviceKindUnknown {
		rank += 4
	}
	if md.Manufacturer != "" {
		rank += 3
	}
	if md.Name != "" && !strings.HasPrefix(md.Name, "UPnP device ") {
		rank += 2
	}
	if md.Model != "" {
		rank++
	}
	return rank
}

func ssdpCandidateRank(candidate ssdpCandidate) int {
	if looksSamsungSSDP(candidate.location, candidate.headers) {
		return 3
	}
	text := strings.ToLower(candidate.headers["server"] + " " + candidate.headers["st"] + " " + candidate.headers["usn"])
	if strings.Contains(text, "tv") || strings.Contains(text, "airplay") || strings.Contains(text, "display") || strings.Contains(text, "mediarenderer") {
		return 2
	}
	return 1
}

func looksSamsungSSDP(location *url.URL, headers map[string]string) bool {
	if location.Port() == "8001" {
		return true
	}
	for _, value := range []string{headers["server"], headers["usn"], headers["st"]} {
		if strings.Contains(strings.ToLower(value), "samsung") {
			return true
		}
	}
	return false
}

func genericSSDP(ip string, headers map[string]string) core.DiscoveredDevice {
	description := headers["server"]
	if description == "" {
		description = headers["st"]
	}
	name := headers["usn"]
	if name == "" {
		name = "UPnP device " + ip
	}
	kind := core.DeviceKindUnknown
	manufacturer := ""
	text := strings.ToLower(description + " " + headers["st"] + " " + name)
	switch {
	case strings.Contains(text, "playstation") || strings.Contains(text, "ps5") || strings.Contains(text, "ps4"):
		kind = core.DeviceKindConsole
		manufacturer = "Sony"
	case strings.Contains(text, "xbox"):
		kind = core.DeviceKindConsole
		manufacturer = "Microsoft"
	case strings.Contains(text, "nintendo") || strings.Contains(text, "switch"):
		kind = core.DeviceKindConsole
		manufacturer = "Nintendo"
	case strings.Contains(text, "roku"):
		kind = core.DeviceKindTV
		manufacturer = "Roku"
	case strings.Contains(text, "sonos") || strings.Contains(text, "linux upnp"):
		// A Linux UPnP stack does not make a speaker or appliance a computer.
		// Keep it outside the focused inventory unless another service supplies
		// an actual computer identity.
		kind = core.DeviceKindUnknown
		manufacturer = "Sonos"
	case strings.Contains(text, "android") || strings.Contains(text, "iphone") || strings.Contains(text, "ipad") || strings.Contains(text, "mobile") || strings.Contains(text, "phone"):
		kind = core.DeviceKindMobile
	case strings.Contains(text, "monitor") || strings.Contains(text, "display"):
		kind = core.DeviceKindMonitor
	case strings.Contains(text, "windows") || strings.Contains(text, "win32") || strings.Contains(text, "microsoft"):
		kind = core.DeviceKindComputer
	case strings.Contains(text, "television") || strings.Contains(text, " smart tv") || strings.Contains(text, "tv device") || strings.Contains(text, "cast") || strings.Contains(text, "airplay"):
		kind = core.DeviceKindTV
	case strings.Contains(text, "computer") || strings.Contains(text, "workstation") || strings.Contains(text, "linux") || strings.Contains(text, "macos"):
		kind = core.DeviceKindComputer
	}
	id := "ssdp-" + strings.ReplaceAll(ip, ".", "-")
	paired := false
	capabilities := []core.Capability{core.CapabilityUnsupported}
	if manufacturer == "Roku" {
		// The USN is stable across DHCP changes and is more useful than an IP
		// address for Roku records. Roku does not have a user pairing token;
		// the TV's “Control by mobile apps” setting is its access gate.
		id = "roku-" + stableSSDPID(name, ip)
		paired = true
		capabilities = []core.Capability{core.CapabilityStatus, core.CapabilityPower, core.CapabilityVolume, core.CapabilityMute, core.CapabilityPlayback, core.CapabilityNavigation, core.CapabilitySource, core.CapabilityChannel}
	}
	device := core.DiscoveredDevice{Source: "ssdp", Metadata: core.DeviceMetadata{
		ID: core.DeviceID(id), Kind: kind, Manufacturer: manufacturer, Name: name, Model: description,
		IP: ip, Capabilities: capabilities, Paired: paired, Online: true, LastSeen: time.Now().UTC(),
	}}
	device.Metadata = core.NormalizeMetadata(device.Metadata)
	return device
}

// enrichDescription reads the standard UPnP device description. Headers alone
// often say only MediaRenderer, which made real TVs appear as unidentified LAN
// devices. The XML normally carries friendlyName, manufacturer, and modelName.
func (p *SSDPProvider) enrichDescription(ctx context.Context, location *url.URL, device core.DiscoveredDevice) core.DiscoveredDevice {
	if location == nil {
		return device
	}
	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
	if err != nil {
		return device
	}
	resp, err := client.Do(req)
	if err != nil {
		return device
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return device
	}
	var payload struct {
		Device struct {
			FriendlyName string `xml:"friendlyName"`
			Manufacturer string `xml:"manufacturer"`
			ModelName    string `xml:"modelName"`
			ModelNumber  string `xml:"modelNumber"`
			UDN          string `xml:"UDN"`
		} `xml:"device"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return device
	}
	if payload.Device.FriendlyName != "" {
		device.Metadata.Name = payload.Device.FriendlyName
	}
	if payload.Device.Manufacturer != "" {
		device.Metadata.Manufacturer = payload.Device.Manufacturer
	}
	if payload.Device.ModelName != "" {
		device.Metadata.Model = payload.Device.ModelName
	} else if payload.Device.ModelNumber != "" {
		device.Metadata.Model = payload.Device.ModelNumber
	}
	if payload.Device.UDN != "" {
		device.Metadata.DUID = payload.Device.UDN
	}
	device.Metadata = core.NormalizeMetadata(device.Metadata)
	return device
}

func stableSSDPID(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			builder.WriteRune(character)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	value = strings.Trim(builder.String(), "-")
	if value == "" {
		value = strings.ReplaceAll(fallback, ".", "-")
	}
	return value
}

func parseSamsungMetadata(b []byte, ip string) (core.DiscoveredDevice, bool) {
	var payload struct {
		Device struct {
			Name        string `json:"name"`
			Model       string `json:"modelName"`
			Type        string `json:"type"`
			Description string `json:"description"`
			OS          string `json:"OS"`
			DUID        string `json:"duid"`
			WiFiMAC     string `json:"wifiMac"`
			Firmware    string `json:"firmwareVersion"`
		} `json:"device"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return core.DiscoveredDevice{}, false
	}
	identityText := strings.ToLower(strings.Join([]string{payload.Device.Name, payload.Device.Model, payload.Device.Type, payload.Device.Description, payload.Device.OS}, " "))
	if !containsAnyDiscovery(identityText, "samsung", "tizen") {
		return core.DiscoveredDevice{}, false
	}
	if payload.Device.Model == "" && payload.Device.DUID == "" && payload.Device.WiFiMAC == "" {
		return core.DiscoveredDevice{}, false
	}
	name := payload.Device.Name
	if name == "" {
		name = "Samsung TV"
	}
	id := payload.Device.DUID
	if id == "" {
		id = normalizeMAC(payload.Device.WiFiMAC)
	}
	if id == "" {
		id = "samsung-" + strings.ReplaceAll(ip, ".", "-")
	}
	md := core.DeviceMetadata{
		ID:   core.DeviceID("samsung-" + strings.ToLower(strings.ReplaceAll(id, ":", ""))),
		Kind: core.DeviceKindTV, Manufacturer: "Samsung", Model: payload.Device.Model,
		Name: name, IP: ip, MAC: normalizeMAC(payload.Device.WiFiMAC), DUID: payload.Device.DUID,
		Firmware: payload.Device.Firmware, Capabilities: samsungCapabilities(), ControlVerified: true, Online: true, LastSeen: time.Now().UTC(),
	}
	return core.DiscoveredDevice{Metadata: md, Source: "ssdp"}, true
}

func (p *SSDPProvider) probeSamsung(ctx context.Context, ip string) (core.DiscoveredDevice, bool) {
	return probeSamsungAPI(ctx, ip, p.HTTPClient)
}

// probeSamsungAPI is also used by the ARP-backed LAN sweep. Samsung TVs can
// expose /api/v2/ while failing to answer SSDP, so discovery must not depend
// on a single broadcast protocol.
func probeSamsungAPI(ctx context.Context, ip string, client *http.Client) (core.DiscoveredDevice, bool) {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	}
	for _, endpoint := range []string{
		"http://" + net.JoinHostPort(ip, "8001") + "/api/v2/",
		"https://" + net.JoinHostPort(ip, "8002") + "/api/v2/",
	} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_ = resp.Body.Close()
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
		_ = resp.Body.Close()
		if err != nil {
			continue
		}
		return parseSamsungMetadata(b, ip)
	}
	return core.DiscoveredDevice{}, false
}

func probeRokuAPI(ctx context.Context, ip string, client *http.Client) (core.DiscoveredDevice, bool) {
	if client == nil {
		client = &http.Client{Timeout: 1500 * time.Millisecond}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(ip, "8060")+"/query/device-info", nil)
	if err != nil {
		return core.DiscoveredDevice{}, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return core.DiscoveredDevice{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return core.DiscoveredDevice{}, false
	}
	var payload struct {
		UDN         string `xml:"udn"`
		DeviceID    string `xml:"device-id"`
		VendorName  string `xml:"vendor-name"`
		ModelName   string `xml:"model-name"`
		ModelNumber string `xml:"model-number"`
		UserName    string `xml:"user-device-name"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil || !strings.EqualFold(payload.VendorName, "Roku") {
		return core.DiscoveredDevice{}, false
	}
	identity := firstDiscoveryValue(payload.DeviceID, payload.UDN, ip)
	name := firstDiscoveryValue(payload.UserName, payload.ModelName, "Roku "+payload.ModelNumber)
	md := core.NormalizeMetadata(core.DeviceMetadata{
		ID: core.DeviceID("roku-" + stableSSDPID(identity, ip)), Kind: core.DeviceKindTV,
		Manufacturer: "Roku", Model: firstDiscoveryValue(payload.ModelName, payload.ModelNumber), Name: name,
		IP: ip, Paired: true, Online: true, LastSeen: time.Now().UTC(),
		Capabilities: []core.Capability{core.CapabilityStatus, core.CapabilityPower, core.CapabilityVolume, core.CapabilityMute, core.CapabilityPlayback, core.CapabilityNavigation, core.CapabilitySource, core.CapabilityChannel},
	})
	return core.DiscoveredDevice{Metadata: md, Source: "roku-probe"}, true
}

func firstDiscoveryValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parseHeaders(raw string) map[string]string {
	result := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 2)
		if len(parts) == 2 {
			result[strings.ToLower(strings.TrimSpace(parts[0]))] = strings.TrimSpace(parts[1])
		}
	}
	return result
}

func normalizeMAC(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, "-", ":")
	parts := strings.Split(value, ":")
	if len(parts) == 6 {
		for i := range parts {
			if len(parts[i]) == 1 {
				parts[i] = "0" + parts[i]
			}
			if len(parts[i]) != 2 {
				return ""
			}
		}
		return strings.Join(parts, ":")
	}
	if len(value) == 12 {
		var b strings.Builder
		for i := 0; i < len(value); i += 2 {
			if b.Len() > 0 {
				b.WriteByte(':')
			}
			b.WriteString(value[i : i+2])
		}
		return b.String()
	}
	return value
}

func samsungCapabilities() []core.Capability {
	return []core.Capability{core.CapabilityStatus, core.CapabilityPower, core.CapabilityVolume, core.CapabilityMute, core.CapabilityPlayback, core.CapabilityNavigation, core.CapabilitySource, core.CapabilityChannel, core.CapabilityWakeOnLAN}
}
