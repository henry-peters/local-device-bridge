package roku

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/local-device-bridge/local-device-bridge/internal/core"
)

func newFakeRoku(t *testing.T) (*httptest.Server, *Device, <-chan string) {
	t.Helper()
	commands := make(chan string, 16)
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/query/device-info" {
			w.Header().Set("Content-Type", "application/xml")
			_ = xml.NewEncoder(w).Encode(struct {
				XMLName   xml.Name `xml:"device-info"`
				PowerMode string   `xml:"power-mode"`
			}{PowerMode: "Ready"})
			return
		}
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		commands <- strings.TrimPrefix(r.URL.Path, "/keypress/")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	device := &Device{metadata: core.DeviceMetadata{ID: "roku-test", Kind: core.DeviceKindTV, Manufacturer: "Roku", IP: "127.0.0.1"}, client: server.Client(), baseURL: server.URL}
	return server, device, commands
}

func TestStateAndRemoteCommands(t *testing.T) {
	server, device, commands := newFakeRoku(t)
	defer server.Close()

	state, err := device.State(context.Background())
	if err != nil || !state.Online || state.Power != "Ready" {
		t.Fatalf("unexpected Roku state: %+v, err=%v", state, err)
	}
	if _, err := device.Execute(context.Background(), core.Command{Action: core.ActionKey, Arguments: map[string]string{"key": "HOME"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := device.Execute(context.Background(), core.Command{Action: core.ActionVolumeUp, Arguments: map[string]string{"steps": "3"}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"Home", "VolumeUp", "VolumeUp", "VolumeUp"}
	for _, expected := range want {
		if got := <-commands; got != expected {
			t.Fatalf("Roku command = %q, want %q", got, expected)
		}
	}
}

func TestRokuPowerOffAndSource(t *testing.T) {
	server, device, commands := newFakeRoku(t)
	defer server.Close()
	for _, command := range []core.Command{
		{Action: core.ActionPowerOff},
		{Action: core.ActionSource, Arguments: map[string]string{"source": "hdmi1"}},
		{Action: core.ActionKey, Arguments: map[string]string{"key": "PLAY_PAUSE"}},
	} {
		if _, err := device.Execute(context.Background(), command); err != nil {
			t.Fatal(err)
		}
	}
	for _, expected := range []string{"PowerOff", "InputHDMI1", "Play"} {
		if got := <-commands; got != expected {
			t.Fatalf("Roku command = %q, want %q", got, expected)
		}
	}
}

func TestRokuFactory(t *testing.T) {
	info := core.DiscoveredDevice{Metadata: core.DeviceMetadata{Kind: core.DeviceKindTV, Manufacturer: "Roku", Name: "Living Room"}}
	device, err := (Factory{}).Create(context.Background(), info)
	if err != nil || device.Metadata().Manufacturer != "Roku" || len(device.Capabilities()) == 0 {
		t.Fatalf("unexpected Roku factory result: device=%v err=%v", device, err)
	}
}
