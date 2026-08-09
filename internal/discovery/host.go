package discovery

import (
	"context"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/local-device-bridge/local-device-bridge/internal/core"
)

// LocalHostProvider adds the computer running the bridge to the inventory.
// It is informational in v1: the bridge never executes arbitrary commands on
// computers discovered on the LAN.
type LocalHostProvider struct{}

func (p *LocalHostProvider) Name() string { return "host" }

func (p *LocalHostProvider) Discover(_ context.Context) ([]core.DiscoveredDevice, error) {
	name, _ := os.Hostname()
	name = strings.TrimSuffix(strings.TrimSpace(name), ".local")
	if name == "" {
		name = "Bridge host"
	}
	ip, mac := localHostAddress()
	manufacturer, model := detectHostIdentity(runtime.GOOS)
	paired := true
	id := "host-" + strings.ToLower(strings.NewReplacer(" ", "-", ".", "-", "_", "-").Replace(name))
	return []core.DiscoveredDevice{{Source: p.Name(), Metadata: core.DeviceMetadata{
		ID: core.DeviceID(id), Kind: core.DeviceKindComputer, Manufacturer: manufacturer, Model: model,
		Name: name, IP: ip, MAC: mac, Discovery: p.Name(), Paired: paired,
		Capabilities: []core.Capability{core.CapabilityUnsupported}, Online: true, LastSeen: time.Now().UTC(),
	}}}, nil
}

func hostIdentity(goos string) (string, string) {
	switch goos {
	case "darwin":
		return "Apple", "macOS"
	case "linux":
		return "Linux", "Linux"
	case "windows":
		return "Microsoft", "Windows"
	default:
		return "", goos
	}
}

func detectHostIdentity(goos string) (string, string) {
	manufacturer, model := hostIdentity(goos)
	if goos != "linux" {
		return manufacturer, model
	}
	for _, path := range []string{"/proc/device-tree/model", "/sys/firmware/devicetree/base/model"} {
		if raw, err := os.ReadFile(path); err == nil {
			value := strings.TrimSpace(strings.TrimRight(string(raw), "\x00"))
			if strings.Contains(strings.ToLower(value), "raspberry pi") {
				return "Raspberry Pi", value
			}
		}
	}
	return manufacturer, model
}

func localHostAddress() (string, string) {
	// Ask the kernel which interface it would use for ordinary IPv4 traffic.
	// This avoids advertising a Docker, VPN, or Thunderbolt address as the
	// bridge URL when Wi-Fi/Ethernet is the actual LAN path.
	if connection, err := net.Dial("udp4", "1.1.1.1:53"); err == nil {
		if address, ok := connection.LocalAddr().(*net.UDPAddr); ok && address.IP.To4() != nil {
			ip := address.IP.To4()
			_ = connection.Close()
			if iface, ok := interfaceForIP(ip); ok {
				return ip.String(), normalizeMAC(iface.HardwareAddr.String())
			}
			return ip.String(), ""
		}
		_ = connection.Close()
	}
	for _, network := range LocalIPv4Networks(nil) {
		return network.Address.String(), normalizeMAC(network.Interface.HardwareAddr.String())
	}
	return "", ""
}

func interfaceForIP(target net.IP) (net.Interface, bool) {
	for _, iface := range LocalIPv4Networks(nil) {
		if iface.Network.Contains(target) {
			return iface.Interface, true
		}
	}
	return net.Interface{}, false
}
