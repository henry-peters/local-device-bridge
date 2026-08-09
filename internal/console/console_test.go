package console

import (
	"context"
	"strings"
	"testing"

	"github.com/local-device-bridge/local-device-bridge/internal/core"
)

func TestFactoryCreatesConsoleWithHonestCapabilities(t *testing.T) {
	device, err := (Factory{}).Create(context.Background(), core.DiscoveredDevice{Metadata: core.DeviceMetadata{
		ID: "console-1", Kind: core.DeviceKindConsole, Platform: "PlayStation", Name: "PlayStation 5",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := device.Capabilities(); len(got) != 1 || got[0] != core.CapabilityStatus {
		t.Fatalf("capabilities without MAC = %#v, want status only", got)
	}
	_, err = device.Execute(context.Background(), core.Command{Action: core.ActionPowerOn})
	if err == nil || !strings.Contains(err.Error(), "MAC address") {
		t.Fatalf("wake without MAC error = %v", err)
	}
}

func TestFactoryAddsWakeCapabilityWhenMACIsKnown(t *testing.T) {
	device, err := (Factory{}).Create(context.Background(), core.DiscoveredDevice{Metadata: core.DeviceMetadata{
		ID: "console-1", Kind: core.DeviceKindConsole, Platform: "Xbox", MAC: "aa:bb:cc:dd:ee:ff",
	}})
	if err != nil {
		t.Fatal(err)
	}
	capabilities := device.Capabilities()
	if len(capabilities) != 2 || capabilities[1] != core.CapabilityWakeOnLAN {
		t.Fatalf("capabilities with MAC = %#v, want status and Wake-on-LAN", capabilities)
	}
}

func TestWakePacket(t *testing.T) {
	packet, mac, err := wakePacket("aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatal(err)
	}
	if len(packet) != 102 || len(mac) != 6 {
		t.Fatalf("packet length = %d, mac length = %d", len(packet), len(mac))
	}
	for index := 0; index < 6; index++ {
		if packet[index] != 0xff {
			t.Fatalf("packet prefix byte %d = %#x", index, packet[index])
		}
	}
	for repeat := 0; repeat < 16; repeat++ {
		for offset := range mac {
			if packet[6+repeat*len(mac)+offset] != mac[offset] {
				t.Fatalf("packet MAC mismatch at repeat %d offset %d", repeat, offset)
			}
		}
	}
}
