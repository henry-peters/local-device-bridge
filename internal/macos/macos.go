package macos

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/local-device-bridge/local-device-bridge/internal/core"
)

var macCapabilities = []core.Capability{core.CapabilityStatus, core.CapabilityPower, core.CapabilityWakeOnLAN}

type commandRunner func(context.Context, string, ...string) ([]byte, error)

type Device struct {
	metadata core.DeviceMetadata
	runner   commandRunner
}

func New(info core.DiscoveredDevice) *Device {
	return &Device{metadata: info.Metadata, runner: runCommand}
}

func (d *Device) Metadata() core.DeviceMetadata { return d.metadata }

func (d *Device) Capabilities() []core.Capability {
	if d.isLocalHost() {
		return []core.Capability{core.CapabilityStatus}
	}
	return append([]core.Capability(nil), macCapabilities...)
}

func (d *Device) isLocalHost() bool { return d.metadata.Discovery == "host" }

func (d *Device) Pair(ctx context.Context) error {
	return d.PairWith(ctx, core.PairOptions{Username: d.metadata.RemoteUser})
}

func (d *Device) PairWith(ctx context.Context, options core.PairOptions) error {
	if d.isLocalHost() {
		return nil
	}
	username := strings.TrimSpace(options.Username)
	if username == "" {
		username = strings.TrimSpace(d.metadata.RemoteUser)
	}
	if username == "" {
		return errors.New("Mac setup required: enable Remote Login and enter the Mac username")
	}
	if !validRemoteUsername(username) {
		return errors.New("Mac username may contain only letters, numbers, dots, underscores, and hyphens")
	}
	if d.metadata.IP == "" {
		return errors.New("Mac has no network address")
	}
	if _, err := d.ssh(ctx, username, "/usr/bin/sudo -n /usr/bin/pmset -g ps"); err != nil {
		return fmt.Errorf("Mac pairing failed: enable Remote Login, use an SSH key, and allow passwordless sudo for pmset: %w", err)
	}
	d.metadata.RemoteUser = username
	d.metadata.Paired = true
	return nil
}

func (d *Device) Unpair(_ context.Context) error {
	if d.isLocalHost() {
		return errors.New("the bridge host is always paired locally")
	}
	d.metadata.RemoteUser = ""
	d.metadata.Paired = false
	return nil
}

func (d *Device) State(ctx context.Context) (core.DeviceState, error) {
	state := core.DeviceState{DeviceID: d.metadata.ID, Updated: time.Now().UTC()}
	if !d.isLocalHost() && !d.metadata.Paired {
		state.Online = d.metadata.Online
		state.Error = "Mac pairing is required before sleep status can be read"
		return state, errors.New(state.Error)
	}
	var output []byte
	var err error
	if d.isLocalHost() {
		output, err = d.runner(ctx, "/usr/bin/pmset", "-g", "ps")
	} else {
		output, err = d.ssh(ctx, d.metadata.RemoteUser, "/usr/bin/sudo -n /usr/bin/pmset -g ps")
	}
	if err != nil {
		state.Error = err.Error()
		return state, err
	}
	state.Online = true
	state.Power = "on"
	if strings.Contains(strings.ToLower(string(output)), "sleeping") {
		state.Power = "sleeping"
	}
	return state, nil
}

func (d *Device) Execute(ctx context.Context, cmd core.Command) (core.CommandResult, error) {
	switch cmd.Action {
	case core.ActionStatus:
		state, err := d.State(ctx)
		return core.CommandResult{Message: "Mac power status read", State: &state}, err
	case core.ActionPowerOn:
		if d.isLocalHost() {
			return core.CommandResult{}, errors.New("bridge host is status-only; it cannot be woken by local-device-bridge")
		}
		if d.metadata.MAC == "" {
			return core.CommandResult{}, errors.New("Mac has no MAC address for Wake-on-LAN")
		}
		if err := wakeOnLANForDevice(d.metadata.MAC, d.metadata.IP); err != nil {
			return core.CommandResult{}, err
		}
		return core.CommandResult{Message: "Wake-on-LAN packet sent; enable Wake for network access on the Mac because power-on cannot be confirmed"}, nil
	case core.ActionPowerOff:
		if d.isLocalHost() {
			return core.CommandResult{}, errors.New("bridge host is status-only; it cannot be put to sleep by local-device-bridge")
		}
		if !d.isLocalHost() && !d.metadata.Paired {
			return core.CommandResult{}, errors.New("Mac is not paired; enable Remote Login and pair it before sending sleep")
		}
		var err error
		if d.isLocalHost() {
			_, err = d.runner(ctx, "/usr/bin/pmset", "sleepnow")
		} else {
			_, err = d.ssh(ctx, d.metadata.RemoteUser, "/usr/bin/sudo -n /usr/bin/pmset sleepnow")
		}
		if err != nil {
			return core.CommandResult{}, fmt.Errorf("Mac sleep failed; check Remote Login and passwordless sudo for pmset: %w", err)
		}
		return core.CommandResult{Message: "Mac sleep command accepted"}, nil
	default:
		return core.CommandResult{}, fmt.Errorf("unsupported Mac action %q; only status, power_on, and power_off are available", cmd.Action)
	}
}

func (d *Device) ssh(ctx context.Context, username, remoteCommand string) ([]byte, error) {
	return d.runner(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=4", "-o", "StrictHostKeyChecking=accept-new", username+"@"+d.metadata.IP, remoteCommand)
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func validRemoteUsername(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func (f Factory) Supports(info core.DiscoveredDevice) bool {
	return info.Metadata.Kind == core.DeviceKindComputer && strings.EqualFold(info.Metadata.Manufacturer, "Apple") && strings.EqualFold(info.Metadata.Model, "macOS")
}

type Factory struct{}

func (f Factory) Create(_ context.Context, info core.DiscoveredDevice) (core.Device, error) {
	return New(info), nil
}

func wakeOnLANForDevice(mac, ip string) error {
	hw, err := net.ParseMAC(strings.ReplaceAll(mac, "-", ":"))
	if err != nil || len(hw) != 6 {
		return fmt.Errorf("invalid Mac MAC address %q", mac)
	}
	targets := []net.IP{net.IPv4bcast}
	if target := net.ParseIP(ip).To4(); target != nil {
		targets = append(targets, target)
		for _, address := range localBroadcasts(target) {
			targets = append(targets, address)
		}
	}
	packet := magicPacket(hw)
	seen := map[string]bool{}
	sent := false
	for attempt := 0; attempt < 3; attempt++ {
		for _, target := range targets {
			if seen[target.String()] {
				continue
			}
			seen[target.String()] = true
			for _, port := range []int{9, 7} {
				conn, dialErr := net.DialUDP("udp4", nil, &net.UDPAddr{IP: target, Port: port})
				if dialErr != nil {
					continue
				}
				if _, writeErr := conn.Write(packet); writeErr == nil {
					sent = true
				}
				_ = conn.Close()
			}
		}
		if attempt < 2 {
			time.Sleep(250 * time.Millisecond)
		}
	}
	if !sent {
		return errors.New("send Mac Wake-on-LAN packet failed")
	}
	return nil
}

func magicPacket(mac net.HardwareAddr) []byte {
	packet := make([]byte, 102)
	for i := 0; i < 6; i++ {
		packet[i] = 0xff
	}
	for i := 1; i <= 16; i++ {
		copy(packet[i*6:i*6+6], mac)
	}
	return packet
}

func localBroadcasts(target net.IP) []net.IP {
	var result []net.IP
	interfaces, _ := net.Interfaces()
	for _, iface := range interfaces {
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			ip, network, err := net.ParseCIDR(address.String())
			if err != nil || ip.To4() == nil || !network.Contains(target) {
				continue
			}
			ip4, mask := ip.To4(), network.Mask
			result = append(result, net.IPv4(ip4[0]|^mask[0], ip4[1]|^mask[1], ip4[2]|^mask[2], ip4[3]|^mask[3]).To4())
		}
	}
	return result
}
