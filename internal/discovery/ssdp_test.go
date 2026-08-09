package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/local-device-bridge/local-device-bridge/internal/core"
)

func TestParseHeaders(t *testing.T) {
	headers := parseHeaders("HTTP/1.1 200 OK\r\nLOCATION: http://192.0.2.20:8001/device.xml\r\nST: urn:schemas-upnp-org:device:MediaRenderer:1\r\n")
	if headers["location"] != "http://192.0.2.20:8001/device.xml" {
		t.Fatalf("unexpected location: %#v", headers)
	}
	if headers["st"] == "" {
		t.Fatal("missing ST header")
	}
}

func TestSSDPDescriptionPromotesGenericTVVendor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<root><device><friendlyName>Family Room</friendlyName><manufacturer>LG Electronics</manufacturer><modelName>webOS TV OLED</modelName><UDN>uuid:lg-tv</UDN></device></root>`))
	}))
	defer server.Close()
	location, err := url.Parse(server.URL + "/description.xml")
	if err != nil {
		t.Fatal(err)
	}
	provider := &SSDPProvider{HTTPClient: server.Client()}
	device := provider.enrichDescription(context.Background(), location, genericSSDP("192.0.2.44", map[string]string{"st": "urn:schemas-upnp-org:device:MediaRenderer:1"}))
	if device.Metadata.Manufacturer != "LG" || device.Metadata.Category != core.CategoryTVDisplay || device.Metadata.Name != "Family Room" {
		t.Fatalf("enriched metadata = %+v", device.Metadata)
	}
}

func TestNormalizeMAC(t *testing.T) {
	for _, input := range []string{"AA-BB-CC-DD-EE-FF", "aabbccddeeff"} {
		if got := normalizeMAC(input); got != "aa:bb:cc:dd:ee:ff" {
			t.Errorf("normalizeMAC(%q) = %q", input, got)
		}
	}
}

func TestParseSamsungMetadataRejectsNonSamsungJSON(t *testing.T) {
	if _, ok := parseSamsungMetadata([]byte(`{"device":{"name":"Living Room","modelName":"Test TV","wifiMac":"aa:bb:cc:dd:ee:ff"}}`), "192.0.2.20"); ok {
		t.Fatal("unidentified device JSON must not be promoted to Samsung")
	}
	device, ok := parseSamsungMetadata([]byte(`{"device":{"name":"Living Room","type":"Samsung DTV","modelName":"UA55TEST","duid":"uuid:test","wifiMac":"aa:bb:cc:dd:ee:ff"}}`), "192.0.2.20")
	if !ok || !device.Metadata.ControlVerified || device.Metadata.Manufacturer != "Samsung" {
		t.Fatalf("Samsung payload was not verified: %+v, ok=%v", device.Metadata, ok)
	}
	if _, ok := parseSamsungMetadata([]byte(`{"hello":"world"}`), "192.0.2.20"); ok {
		t.Fatal("unrelated JSON should not be identified as Samsung")
	}
}

func TestGenericSSDPClassification(t *testing.T) {
	monitor := genericSSDP("192.0.2.30", map[string]string{"server": "Example Monitor", "st": "upnp:display"})
	if monitor.Metadata.Kind != core.DeviceKindMonitor {
		t.Fatalf("kind = %q, want monitor", monitor.Metadata.Kind)
	}
	computer := genericSSDP("192.0.2.31", map[string]string{"server": "Linux workstation", "st": "upnp:device"})
	if computer.Metadata.Kind != core.DeviceKindComputer {
		t.Fatalf("kind = %q, want computer", computer.Metadata.Kind)
	}
	windowsLaptop := genericSSDP("192.0.2.35", map[string]string{"server": "Windows 11 laptop", "st": "upnp:device"})
	if windowsLaptop.Metadata.Kind != core.DeviceKindComputer || windowsLaptop.Metadata.Category != core.CategoryComputer || windowsLaptop.Metadata.Platform != "Windows laptop" {
		t.Fatalf("unexpected Windows laptop metadata: %+v", windowsLaptop.Metadata)
	}
	windowsComputer := genericSSDP("192.0.2.36", map[string]string{"server": "Microsoft Windows", "st": "upnp:device"})
	if windowsComputer.Metadata.Kind != core.DeviceKindComputer || windowsComputer.Metadata.Category != core.CategoryComputer || windowsComputer.Metadata.Platform != "Windows" {
		t.Fatalf("unexpected Windows computer metadata: %+v", windowsComputer.Metadata)
	}
	roku := genericSSDP("192.0.2.32", map[string]string{"server": "Roku Streaming Player", "usn": "uuid:roku-living-room", "st": "urn:roku-com:device:player:1-0"})
	if roku.Metadata.Kind != core.DeviceKindTV || roku.Metadata.Manufacturer != "Roku" || roku.Metadata.ID != "roku-uuid-roku-living-room" {
		t.Fatalf("unexpected Roku metadata: %+v", roku.Metadata)
	}
	if !roku.Metadata.Paired || len(roku.Metadata.Capabilities) == 0 || roku.Metadata.Capabilities[0] == core.CapabilityUnsupported {
		t.Fatalf("Roku should be available without bridge pairing: %+v", roku.Metadata)
	}
	console := genericSSDP("192.0.2.33", map[string]string{"server": "PlayStation 5", "usn": "uuid:ps5"})
	if console.Metadata.Kind != core.DeviceKindConsole || console.Metadata.Category != core.CategoryConsole {
		t.Fatalf("unexpected console metadata: %+v", console.Metadata)
	}
}

func TestSSDPDoesNotClassifySonosAsComputer(t *testing.T) {
	device := genericSSDP("192.0.2.40", map[string]string{"server": "Linux UPnP/1.0 Sonos/86.8", "st": "urn:schemas-upnp-org:device:MediaRenderer:1"})
	if device.Metadata.Kind != core.DeviceKindUnknown || device.Metadata.Manufacturer != "Sonos" {
		t.Fatalf("device = %#v, want hidden Sonos audio device", device.Metadata)
	}
}

func TestGenericMediaRendererIsNotAssumedToBeATV(t *testing.T) {
	device := genericSSDP("192.0.2.41", map[string]string{"server": "UPnP/1.0", "st": "urn:schemas-upnp-org:device:MediaRenderer:1"})
	if device.Metadata.Kind != core.DeviceKindUnknown || device.Metadata.Category != core.CategoryOther {
		t.Fatalf("generic media renderer was mislabeled: %+v", device.Metadata)
	}
}

func TestSSDPResponseRankingPrefersSamsungEndpoint(t *testing.T) {
	airplay, err := url.Parse("http://192.0.2.34:7000/device.xml")
	if err != nil {
		t.Fatal(err)
	}
	samsung, err := url.Parse("http://192.0.2.34:8001/api/v2/")
	if err != nil {
		t.Fatal(err)
	}
	if ssdpCandidateRank(ssdpCandidate{location: samsung, headers: map[string]string{"st": "ssdp:all"}}) <= ssdpCandidateRank(ssdpCandidate{location: airplay, headers: map[string]string{"server": "AirPlay/2.0"}}) {
		t.Fatal("Samsung endpoint did not outrank the generic AirPlay response")
	}
}
