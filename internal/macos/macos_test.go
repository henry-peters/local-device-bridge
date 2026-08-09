package macos

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/local-device-bridge/local-device-bridge/internal/core"
)

func TestMagicPacket(t *testing.T) {
	packet := magicPacket(net.HardwareAddr{0, 1, 2, 3, 4, 5})
	if len(packet) != 102 || packet[0] != 0xff || packet[5] != 0xff {
		t.Fatalf("unexpected packet header/length: %d %x", len(packet), packet[:6])
	}
	for i := 0; i < 16; i++ {
		for j, want := range []byte{0, 1, 2, 3, 4, 5} {
			if packet[6+i*6+j] != want {
				t.Fatalf("copy %d byte %d = %x", i, j, packet[6+i*6+j])
			}
		}
	}
}

func TestBridgeHostIsStatusOnly(t *testing.T) {
	device := &Device{metadata: core.DeviceMetadata{ID: "host-mac", Kind: core.DeviceKindComputer, Manufacturer: "Apple", Model: "macOS", Discovery: "host", Paired: true}}
	if got := device.Capabilities(); len(got) != 1 || got[0] != core.CapabilityStatus {
		t.Fatalf("host capabilities = %v, want status only", got)
	}
	for _, action := range []core.Action{core.ActionPowerOn, core.ActionPowerOff} {
		if _, err := device.Execute(context.Background(), core.Command{Action: action}); err == nil {
			t.Fatalf("host action %q was accepted", action)
		}
	}
}

func TestRemoteMacPairRequiresSSHSetup(t *testing.T) {
	device := &Device{metadata: core.DeviceMetadata{ID: "macbook", Kind: core.DeviceKindComputer, Manufacturer: "Apple", Model: "macOS", IP: "192.0.2.10"}, runner: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("ssh unavailable")
	}}
	if err := device.PairWith(context.Background(), core.PairOptions{}); err == nil {
		t.Fatal("expected remote Mac pairing setup error")
	}
}

func TestValidRemoteUsernameRejectsSSHArgumentInjection(t *testing.T) {
	for _, username := range []string{"", "alex@host", "alex;whoami", "-oProxyCommand=sh", "alex user"} {
		if validRemoteUsername(username) {
			t.Fatalf("username %q was accepted", username)
		}
	}
	for _, username := range []string{"alex", "alex.smith", "alex_smith", "alex-smith", "A1"} {
		if !validRemoteUsername(username) {
			t.Fatalf("username %q was rejected", username)
		}
	}
}
