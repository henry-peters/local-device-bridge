// Package console provides the deliberately small, vendor-neutral console
// adapter. Consoles do not share a safe universal LAN remote protocol: the
// bridge therefore exposes inventory, status, and Wake-on-LAN when discovery
// supplies a MAC address, while refusing to pretend that account-backed
// PlayStation/Xbox/Nintendo commands are universally available.
package console

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/local-device-bridge/local-device-bridge/internal/core"
)

type Factory struct{}

func (Factory) Supports(item core.DiscoveredDevice) bool {
	return core.NormalizeMetadata(item.Metadata).Kind == core.DeviceKindConsole
}

func (Factory) Create(_ context.Context, item core.DiscoveredDevice) (core.Device, error) {
	item.Metadata = core.NormalizeMetadata(item.Metadata)
	return &Device{metadata: item.Metadata}, nil
}

type Device struct {
	metadata core.DeviceMetadata
}

func (d *Device) Metadata() core.DeviceMetadata { return d.metadata }

func (d *Device) Capabilities() []core.Capability {
	capabilities := []core.Capability{core.CapabilityStatus}
	if strings.TrimSpace(d.metadata.MAC) != "" {
		capabilities = append(capabilities, core.CapabilityWakeOnLAN)
	}
	return capabilities
}

func (d *Device) Pair(context.Context) error {
	return errors.New("consoles do not use local-device-bridge pairing; enable the console's network wake setting instead")
}

func (d *Device) State(_ context.Context) (core.DeviceState, error) {
	return core.DeviceState{
		DeviceID: d.metadata.ID,
		Online:   d.metadata.Online,
		Updated:  time.Now().UTC(),
	}, nil
}

func (d *Device) Execute(ctx context.Context, command core.Command) (core.CommandResult, error) {
	switch command.Action {
	case core.ActionStatus:
		state, err := d.State(ctx)
		return core.CommandResult{Message: "Console status reflects the latest LAN discovery result", State: &state}, err
	case core.ActionPowerOn:
		if strings.TrimSpace(d.metadata.MAC) == "" {
			return core.CommandResult{}, errors.New("console wake is unavailable because discovery did not report a MAC address; run a scan while the console is online")
		}
		if err := wakeOnLAN(d.metadata.MAC); err != nil {
			return core.CommandResult{}, fmt.Errorf("console Wake-on-LAN failed: %w", err)
		}
		return core.CommandResult{Message: "Console Wake-on-LAN packet sent; rediscover it after it wakes"}, nil
	case core.ActionPowerOff:
		return core.CommandResult{}, errors.New("console power-off is not available through a safe universal LAN API; use the official PlayStation, Xbox, or Nintendo control method")
	default:
		return core.CommandResult{}, fmt.Errorf("console action %q is not supported; use status or wake", command.Action)
	}
}

func wakeOnLAN(rawMAC string) error {
	packet, _, err := wakePacket(rawMAC)
	if err != nil {
		return err
	}
	connection, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4bcast, Port: 9})
	if err != nil {
		return err
	}
	defer connection.Close()
	if _, err := connection.Write(packet); err != nil {
		return err
	}
	return nil
}

func wakePacket(rawMAC string) ([]byte, net.HardwareAddr, error) {
	mac, err := net.ParseMAC(strings.TrimSpace(rawMAC))
	if err != nil || len(mac) != 6 {
		return nil, nil, fmt.Errorf("invalid console MAC address %q", rawMAC)
	}
	packet := make([]byte, 6+16*len(mac))
	for index := 0; index < 6; index++ {
		packet[index] = 0xff
	}
	for offset := 6; offset < len(packet); offset += len(mac) {
		copy(packet[offset:], mac)
	}
	return packet, mac, nil
}
