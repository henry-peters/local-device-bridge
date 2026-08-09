package discovery

import (
	"bufio"
	"context"
	"encoding/binary"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/local-device-bridge/local-device-bridge/internal/core"
)

// ARPProvider exposes nearby LAN peers as internal discovery evidence and
// probes only vendor-documented Samsung and Roku control endpoints. It never
// attempts arbitrary computer control or scans arbitrary ports.
type ARPProvider struct {
	// Interfaces is optional. An empty list means every active directly
	// connected IPv4 interface. It is wired to discovery.interfaces so a
	// laptop with VPNs, Docker bridges, and Wi-Fi can choose the real LAN.
	Interfaces []string
}

func (p *ARPProvider) Name() string { return "arp" }

func (p *ARPProvider) Discover(ctx context.Context) ([]core.DiscoveredDevice, error) {
	networks := LocalIPv4Networks(p.Interfaces)
	// The ARP table is a cache, not a discovery protocol. Populate it with a
	// bounded local-subnet sweep first so quiet devices can populate it. The
	// sweep only targets the bridge's directly connected IPv4 networks.
	sweepLocalNetworks(ctx, networks)

	// -n prevents reverse-DNS lookups. On macOS, plain `arp -a` can block for
	// roughly ten seconds when a LAN hostname does not resolve, even though the
	// ARP table itself is already available.
	arpArgs := []string{"-an"}
	if runtime.GOOS == "windows" {
		arpArgs = []string{"-a"}
	}
	cmd := exec.CommandContext(ctx, "arp", arpArgs...)
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	ipRE := regexp.MustCompile(`\(([^)]+)\) at ([0-9a-fA-F:.-]+)`)
	windowsIPRE := regexp.MustCompile(`(?m)^\s*([0-9]+(?:\.[0-9]+){3})\s+([0-9a-fA-F-]{11,17})\s+(?:dynamic|static)\s*$`)
	var result []core.DiscoveredDevice
	var activeIPs []string
	macByIP := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		match := ipRE.FindStringSubmatch(line)
		if len(match) != 3 && runtime.GOOS == "windows" {
			match = windowsIPRE.FindStringSubmatch(line)
		}
		if len(match) != 3 {
			continue
		}
		ip := net.ParseIP(match[1])
		mac := normalizeMAC(match[2])
		if ip == nil || ip.IsMulticast() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || isBroadcastARPIP(ip) || mac == "" || mac == "?" {
			continue
		}
		if len(networks) > 0 && !ipOnLocalNetworks(ip, networks) {
			continue
		}
		activeIPs = append(activeIPs, match[1])
		macByIP[match[1]] = mac
		id := "lan-" + strings.ReplaceAll(mac, ":", "")
		kind := core.DeviceKindUnknown
		manufacturer := ""
		model := ""
		name := "LAN device " + match[1]
		if isRaspberryPiMAC(mac) {
			// Raspberry Pi OUIs are a reliable enough identity for an inventory
			// record even when the Pi is quiet on mDNS. Do not do this for Apple
			// OUIs because those are also used by iPhones and iPads.
			kind = core.DeviceKindComputer
			manufacturer = "Raspberry Pi"
			model = "Raspberry Pi"
			name = "Raspberry Pi " + match[1]
		}
		result = append(result, core.DiscoveredDevice{Source: p.Name(), Metadata: core.DeviceMetadata{
			ID: core.DeviceID(id), Kind: kind, Manufacturer: manufacturer, Model: model, Name: name, IP: match[1], MAC: mac,
			Capabilities: []core.Capability{core.CapabilityUnsupported}, Online: true, LastSeen: time.Now().UTC(),
		}})
	}
	// Directly probe the documented local APIs across the connected subnet, not
	// only addresses that answered ICMP. Many TVs and consoles ignore ping but
	// still expose their control service. This is the key difference between an
	// ARP-cache refresh and a useful device discovery scan.
	candidates := append([]string(nil), activeIPs...)
	for _, network := range networks {
		candidates = append(candidates, IPv4Hosts(network.Network, network.Address)...)
	}
	for _, device := range probeKnownDeviceIPs(ctx, candidates) {
		if mac := macByIP[device.Metadata.IP]; mac != "" && device.Metadata.MAC == "" {
			device.Metadata.MAC = mac
		}
		result = append(result, device)
	}
	return result, nil
}

func ipOnLocalNetworks(ip net.IP, networks []LocalNetwork) bool {
	for _, network := range networks {
		if network.Network != nil && network.Network.Contains(ip) {
			return true
		}
	}
	return false
}

func probeKnownDeviceIPs(ctx context.Context, ips []string) []core.DiscoveredDevice {
	seen := make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		if net.ParseIP(ip) == nil {
			continue
		}
		seen[ip] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	sem := make(chan struct{}, 16)
	results := make(chan core.DiscoveredDevice, len(seen))
	var wait sync.WaitGroup
	for ip := range seen {
		wait.Add(1)
		go func(ip string) {
			defer wait.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			probeCtx, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
			defer cancel()
			if device, ok := probeSamsungAPI(probeCtx, ip, nil); ok {
				device.Source = "lan-probe"
				device.Metadata.Discovery = "lan-probe"
				results <- device
				return
			}
			if device, ok := probeRokuAPI(probeCtx, ip, nil); ok {
				device.Source = "lan-probe"
				device.Metadata.Discovery = "lan-probe"
				results <- device
				return
			}
			if device, ok := probeWindowsHost(probeCtx, ip); ok {
				device.Source = "lan-probe"
				device.Metadata.Discovery = "lan-probe"
				results <- device
			}
		}(ip)
	}
	wait.Wait()
	close(results)
	var found []core.DiscoveredDevice
	for device := range results {
		found = append(found, device)
	}
	return found
}

func sweepLocalNetworks(ctx context.Context, networks []LocalNetwork) {
	for _, network := range networks {
		sweepIPv4Network(ctx, network.Network, network.Address)
	}
}

func sweepIPv4Network(ctx context.Context, network *net.IPNet, ownIP net.IP) {
	base := network.IP.To4()
	maskBits, bits := network.Mask.Size()
	if base == nil || bits != 32 || maskBits < 16 || maskBits >= 31 {
		return
	}
	hostCount := uint64(1) << uint(32-maskBits)
	// A /16 already contains 65,534 addresses. Do not turn a broad corporate
	// network into a noisy scan; /24 home and lab networks are the intended
	// default.
	if hostCount > 1024 {
		return
	}
	start := binary.BigEndian.Uint32(base)
	end := start + uint32(hostCount) - 1
	own := binary.BigEndian.Uint32(ownIP)
	sem := make(chan struct{}, 64)
	var wait sync.WaitGroup
	for value := start + 1; value < end; value++ {
		if value == own {
			continue
		}
		candidate := make(net.IP, net.IPv4len)
		binary.BigEndian.PutUint32(candidate, value)
		wait.Add(1)
		go func(ip string) {
			defer wait.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			pingCtx, cancel := context.WithTimeout(ctx, 450*time.Millisecond)
			args := []string{"-c", "1", "-W", "1", ip}
			if runtime.GOOS == "darwin" {
				args = []string{"-c", "1", "-W", "250", ip}
			} else if runtime.GOOS == "windows" {
				args = []string{"-n", "1", "-w", "400", ip}
			}
			_ = exec.CommandContext(pingCtx, "ping", args...).Run()
			cancel()
		}(candidate.String())
	}
	wait.Wait()
}

func probeWindowsHost(ctx context.Context, ip string) (core.DiscoveredDevice, bool) {
	// RDP and WinRM are Windows-specific enough to identify a computer without
	// pretending that every SMB server is Windows. No credentials are sent.
	for _, port := range []string{"3389", "5985", "5986"} {
		dialer := net.Dialer{Timeout: 250 * time.Millisecond}
		connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, port))
		if err != nil {
			continue
		}
		_ = connection.Close()
		return core.DiscoveredDevice{Metadata: core.NormalizeMetadata(core.DeviceMetadata{
			ID: core.DeviceID("windows-" + strings.ReplaceAll(ip, ".", "-")), Kind: core.DeviceKindComputer,
			Manufacturer: "Microsoft", Model: "Windows computer", Name: "Windows computer " + ip,
			IP: ip, Capabilities: []core.Capability{core.CapabilityUnsupported}, Online: true, LastSeen: time.Now().UTC(),
		}), Source: "lan-probe"}, true
	}
	return core.DiscoveredDevice{}, false
}

func isBroadcastARPIP(ip net.IP) bool {
	ip = ip.To4()
	return ip != nil && (ip.Equal(net.IPv4bcast) || ip[3] == 255)
}

// Keep bufio referenced in older platform builds where arp output adapters are extended.
var _ = bufio.ErrInvalidUnreadByte

var appleOUIs = map[string]struct{}{
	"00:03:93": {}, "00:05:02": {}, "00:0a:27": {}, "00:0a:95": {}, "00:0d:93": {},
	"00:11:24": {}, "00:14:51": {}, "00:16:cb": {}, "00:17:f2": {}, "00:19:e3": {},
	"00:1b:63": {}, "00:1c:b3": {}, "00:1e:52": {}, "00:21:e9": {}, "00:22:41": {},
	"00:23:12": {}, "00:23:32": {}, "00:25:00": {}, "00:25:bc": {}, "00:26:08": {},
	"2c:f0:5d": {}, "3c:06:30": {}, "3c:15:c2": {}, "40:30:04": {}, "44:2a:60": {},
	"68:5e:dd": {},
	"48:43:7c": {}, "4c:57:ca": {}, "50:32:37": {}, "58:b0:35": {}, "60:03:08": {},
	"64:20:0c": {}, "68:9c:70": {}, "70:56:81": {}, "74:e2:f5": {}, "78:31:c1": {},
	"7c:6d:62": {}, "80:e6:50": {}, "84:38:35": {}, "84:78:8b": {}, "88:66:a5": {},
	"8c:85:90": {}, "90:72:40": {}, "98:5a:eb": {}, "a4:83:e7": {}, "a8:51:ab": {},
	"ac:bc:32": {}, "b0:34:95": {}, "b4:f6:1c": {}, "b8:17:c2": {}, "bc:54:36": {},
	"c0:84:7d": {}, "c8:2a:14": {}, "cc:08:8d": {}, "d0:03:4b": {}, "d4:61:9d": {},
	"d8:30:62": {}, "dc:2b:2a": {}, "e0:ac:cb": {}, "e0:b9:ba": {}, "f0:18:98": {},
	"f0:99:b6": {}, "f4:5c:89": {}, "f8:1e:df": {}, "f8:ff:c2": {}, "fc:fc:48": {},
}

var raspberryPiOUIs = map[string]struct{}{
	"b8:27:eb": {}, "dc:a6:32": {}, "e4:5f:01": {}, "28:cd:c1": {},
	"2c:cf:67": {}, "d8:3a:dd": {}, "98:de:d0": {},
}

func isAppleMAC(mac string) bool {
	mac = strings.ToLower(normalizeMAC(mac))
	if len(mac) < 8 {
		return false
	}
	_, ok := appleOUIs[mac[:8]]
	return ok
}

func isRaspberryPiMAC(mac string) bool {
	mac = strings.ToLower(normalizeMAC(mac))
	if len(mac) < 8 {
		return false
	}
	_, ok := raspberryPiOUIs[mac[:8]]
	return ok
}
