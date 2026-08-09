package discovery

import (
	"encoding/binary"
	"net"
	"sort"
	"strings"
)

// LocalNetwork is one directly-connected IPv4 network that the bridge can
// legitimately inspect. Discovery providers use this shared snapshot instead
// of each provider inventing its own idea of the active interface.
type LocalNetwork struct {
	Interface net.Interface
	Address   net.IP
	Network   *net.IPNet
}

// LocalIPv4Networks returns active, non-loopback IPv4 networks. If names is
// non-empty, only those interface names are used. This makes multi-homed
// machines deterministic while keeping the default useful for Wi-Fi, Ethernet,
// and Raspberry Pi installations.
func LocalIPv4Networks(names []string) []LocalNetwork {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		if value := strings.TrimSpace(name); value != "" {
			wanted[value] = struct{}{}
		}
	}

	result := make([]LocalNetwork, 0, len(interfaces))
	seen := make(map[string]struct{})
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(wanted) > 0 {
			if _, ok := wanted[iface.Name]; !ok {
				continue
			}
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, network, err := net.ParseCIDR(address.String())
			if err != nil || ip.To4() == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			network.IP = network.IP.To4()
			network.Mask = net.IPMask(network.Mask)
			maskBits, bits := network.Mask.Size()
			if bits != 32 || maskBits < 16 || maskBits >= 31 {
				// Avoid accidental scans of huge corporate networks and point-to-
				// point interfaces. /16 is the largest supported inventory scan.
				continue
			}
			key := iface.Name + "|" + network.String()
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, LocalNetwork{Interface: iface, Address: ip.To4(), Network: network})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Interface.Name != result[j].Interface.Name {
			return result[i].Interface.Name < result[j].Interface.Name
		}
		return result[i].Network.String() < result[j].Network.String()
	})
	return result
}

// IPv4Hosts returns every usable address on a supported local network. It is
// used only for bounded probes of documented device APIs, never for arbitrary
// port scanning or computer command execution.
func IPv4Hosts(network *net.IPNet, own net.IP) []string {
	if network == nil {
		return nil
	}
	base := network.IP.To4()
	maskBits, bits := network.Mask.Size()
	if base == nil || bits != 32 || maskBits < 16 || maskBits >= 31 {
		return nil
	}
	hostCount := uint64(1) << uint(32-maskBits)
	if hostCount > 1024 {
		return nil
	}
	start := binary.BigEndian.Uint32(base)
	end := start + uint32(hostCount) - 1
	ownValue := uint32(0)
	if own != nil && own.To4() != nil {
		ownValue = binary.BigEndian.Uint32(own.To4())
	}
	result := make([]string, 0, hostCount-2)
	for value := start + 1; value < end; value++ {
		if value == ownValue {
			continue
		}
		candidate := make(net.IP, net.IPv4len)
		binary.BigEndian.PutUint32(candidate, value)
		result = append(result, candidate.String())
	}
	return result
}

func networkBroadcast(network *net.IPNet) net.IP {
	if network == nil || network.IP.To4() == nil {
		return nil
	}
	ip := network.IP.To4()
	mask := network.Mask
	return net.IPv4(ip[0]|^mask[0], ip[1]|^mask[1], ip[2]|^mask[2], ip[3]|^mask[3]).To4()
}
