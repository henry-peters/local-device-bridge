package discovery

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/local-device-bridge/local-device-bridge/internal/core"
)

func TestMDNSTXTPromotesFriendlySamsungTV(t *testing.T) {
	device := makeMDNSDeviceWithTXT("_airplay._tcp", "Living Room", "192.0.2.55", "", []string{"model=Samsung QN90", "deviceid=AA:BB:CC:DD:EE:FF"})
	if device.Metadata.Manufacturer != "Samsung" || device.Metadata.Category != core.CategoryTVDisplay || device.Metadata.MAC != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("TXT metadata = %+v", device.Metadata)
	}
}

func TestMDNSDoesNotPresentCastSpeakerAsDisplay(t *testing.T) {
	device := makeMDNSDeviceWithTXT("_googlecast._tcp", "Kitchen speaker", "192.0.2.56", "", []string{"model=Nest Audio", "manufacturer=Google"})
	if device.Metadata.Kind != core.DeviceKindUnknown || device.Metadata.Category != core.CategoryOther {
		t.Fatalf("Cast speaker was presented as a display: %+v", device.Metadata)
	}
}

func TestMDNSIdentifiesMacComputer(t *testing.T) {
	if got := classifyMDNSKind("_device-info._tcp", "Example Mac mini"); got != core.DeviceKindComputer {
		t.Fatalf("kind = %q, want computer", got)
	}
	manufacturer, model := mdnsIdentity("_device-info._tcp", "Example Mac mini", core.DeviceKindComputer)
	if manufacturer != "Apple" || model != "macOS" {
		t.Fatalf("identity = %q %q, want Apple macOS", manufacturer, model)
	}
}

func TestMDNSIdentifiesRaspberryPi(t *testing.T) {
	device := makeMDNSDevice("_ssh._tcp", "raspberrypi", "192.0.2.60", "")
	if device.Metadata.Kind != core.DeviceKindComputer || device.Metadata.Category != core.CategoryComputer || device.Metadata.Platform != "Raspberry Pi" {
		t.Fatalf("Raspberry Pi metadata = %+v", device.Metadata)
	}
}

func TestMDNSIdentifiesSFTPServiceAsMacComputer(t *testing.T) {
	manufacturer, model := mdnsIdentity("_sftp-ssh._tcp", "ISHMBAM4-YK4WCY", core.DeviceKindComputer)
	if manufacturer != "Apple" || model != "macOS" {
		t.Fatalf("identity = %q %q, want Apple macOS", manufacturer, model)
	}
}

func TestMDNSCompanionLinkMacNameIsNotMobile(t *testing.T) {
	if got := classifyMDNSKind("_companion-link._tcp", "Example Mac mini"); got != core.DeviceKindComputer {
		t.Fatalf("kind = %q, want computer", got)
	}
	manufacturer, model := mdnsIdentity("_companion-link._tcp", "Example Mac mini", core.DeviceKindComputer)
	if manufacturer != "Apple" || model != "macOS" {
		t.Fatalf("identity = %q %q, want Apple macOS", manufacturer, model)
	}
}

func TestMDNSProviderNetworkDiagnostics(t *testing.T) {
	if os.Getenv("LDB_NETWORK_TEST") == "" {
		t.Skip("set LDB_NETWORK_TEST=1 to inspect the local Bonjour network")
	}
	started := time.Now()
	devices, err := (&MDNSProvider{Timeout: 1500 * time.Millisecond}).Discover(context.Background())
	t.Logf("mDNS duration=%s err=%v devices=%+v", time.Since(started), err, devices)
}

func TestMDNSExtractsAppleMACFromServiceName(t *testing.T) {
	name := "ISHMBAM4-CQ0FV2 [68:5e:dd:42:d1:16]"
	if got := mdnsMAC(name); got != "68:5e:dd:42:d1:16" {
		t.Fatalf("mdnsMAC = %q", got)
	}
	if got := mdnsDisplayName(name); got != "ISHMBAM4-CQ0FV2" {
		t.Fatalf("mdnsDisplayName = %q", got)
	}
	manufacturer, model := mdnsIdentity("_workstation._tcp", name, core.DeviceKindComputer, mdnsMAC(name))
	if manufacturer != "Apple" || model != "macOS" {
		t.Fatalf("identity = %q %q, want Apple macOS", manufacturer, model)
	}
}

func TestMDNSServiceLabel(t *testing.T) {
	if got := mdnsServiceLabel("_companion-link._tcp.local."); got != "companion-link" {
		t.Fatalf("service label = %q, want companion-link", got)
	}
}

func TestDNSParsers(t *testing.T) {
	browse := "20:35:09.572 Add 2 15 local. _workstation._tcp. ISHMBAM4-CQ0FV2 [68:5e:dd:42:d1:16]\n"
	names := parseDNSBrowse(browse)
	if len(names) != 1 || names[0] != "ISHMBAM4-CQ0FV2 [68:5e:dd:42:d1:16]" {
		t.Fatalf("browse names = %#v", names)
	}
	lookup := "ISHMBAM4-CQ0FV2._workstation._tcp.local. can be reached at ISHMBAM4-CQ0FV2.local.:9 (interface 15)"
	if got := parseDNSLookupHost(lookup); got != "ISHMBAM4-CQ0FV2.local" {
		t.Fatalf("lookup host = %q", got)
	}
}

func TestMDNSIdentifiesAppleMobileSeparately(t *testing.T) {
	if got := classifyMDNSKind("_companion-link._tcp", "Example iPad"); got != core.DeviceKindMobile {
		t.Fatalf("kind = %q, want mobile", got)
	}
	manufacturer, model := mdnsIdentity("_companion-link._tcp", "Example iPad", core.DeviceKindMobile)
	if manufacturer != "Apple" || model != "iOS" {
		t.Fatalf("identity = %q %q, want Apple iOS", manufacturer, model)
	}
}

func TestMDNSAppleMobileServiceWithoutModelIsStillAppleMobile(t *testing.T) {
	if got := classifyMDNSKind("_apple-mobdev2._tcp", "A1B2C3"); got != core.DeviceKindMobile {
		t.Fatalf("kind = %q, want mobile", got)
	}
	manufacturer, model := mdnsIdentity("_apple-mobdev2._tcp", "A1B2C3", core.DeviceKindMobile)
	if manufacturer != "Apple" || model != "iOS" {
		t.Fatalf("identity = %q %q, want Apple iOS", manufacturer, model)
	}
}

func TestMDNSIdentifiesAndroidMobileSeparately(t *testing.T) {
	if got := classifyMDNSKind("_device-info._tcp", "Jules Pixel 9"); got != core.DeviceKindMobile {
		t.Fatalf("kind = %q, want mobile", got)
	}
	manufacturer, model := mdnsIdentity("_device-info._tcp", "Jules Pixel 9", core.DeviceKindMobile)
	if manufacturer != "Google" || model != "Android" {
		t.Fatalf("identity = %q %q, want Google Android", manufacturer, model)
	}
}

func TestMDNSDoesNotMislabelSonosAsComputer(t *testing.T) {
	if got := classifyMDNSKind("_sonos._tcp", "RINCON living room"); got != core.DeviceKindUnknown {
		t.Fatalf("kind = %q, want unknown audio device", got)
	}
}

func TestMDNSMobileIdentityWinsWhenServicesShareAName(t *testing.T) {
	computer := makeMDNSDevice("_workstation._tcp", "Example iPhone", "192.0.2.50", "")
	mobile := makeMDNSDevice("_companion-link._tcp", "Example iPhone", "192.0.2.50", "")
	devices := mergeMDNSDevice(nil, map[core.DeviceID]int{}, computer)
	positions := map[core.DeviceID]int{computer.Metadata.ID: 0}
	devices = mergeMDNSDevice(devices, positions, mobile)
	if len(devices) != 1 || devices[0].Metadata.Kind != core.DeviceKindMobile {
		t.Fatalf("merged device = %#v, want one mobile record", devices)
	}
}

func TestMDNSResultsMergeBothBrowsers(t *testing.T) {
	first := makeMDNSDevice("_workstation._tcp", "Mac mini", "192.0.2.10", "")
	second := makeMDNSDevice("_companion-link._tcp", "iPad", "192.0.2.11", "")
	merged := mergeMDNSResults([]core.DiscoveredDevice{first}, []core.DiscoveredDevice{second})
	if len(merged) != 2 {
		t.Fatalf("merged %d devices, want 2", len(merged))
	}
}
