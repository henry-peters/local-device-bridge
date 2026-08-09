package samsung

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local-device-bridge/local-device-bridge/internal/core"
)

func TestMagicPacket(t *testing.T) {
	packet := magicPacket(net.HardwareAddr{0, 1, 2, 3, 4, 5})
	if len(packet) != 102 {
		t.Fatalf("packet length = %d", len(packet))
	}
	for i := 0; i < 6; i++ {
		if packet[i] != 0xff {
			t.Fatalf("header byte %d = %x", i, packet[i])
		}
	}
	for i := 0; i < 16; i++ {
		for j, want := range []byte{0, 1, 2, 3, 4, 5} {
			if packet[6+i*6+j] != want {
				t.Fatalf("copy %d byte %d = %x", i, j, packet[6+i*6+j])
			}
		}
	}
}

func TestUniqueIPv4(t *testing.T) {
	values := uniqueIPv4([]net.IP{net.IPv4bcast, net.IPv4bcast, net.ParseIP("192.0.2.255")})
	if len(values) != 2 {
		t.Fatalf("uniqueIPv4 returned %d addresses", len(values))
	}
}

func TestReachabilityErrorDetection(t *testing.T) {
	if !isReachabilityError(errors.New("dial tcp: connect: no route to host")) {
		t.Fatal("no-route error was not detected")
	}
	if isReachabilityError(errors.New("TV did not authorize the remote channel")) {
		t.Fatal("authorization error was incorrectly treated as reachability")
	}
}

func TestNewDoesNotInventCapabilitiesBeforeControlVerification(t *testing.T) {
	device := New(core.DiscoveredDevice{Metadata: core.DeviceMetadata{Kind: core.DeviceKindTV, Manufacturer: "Samsung", Capabilities: []core.Capability{core.CapabilityUnsupported}}}, &fakeSecrets{values: map[string]string{}})
	if len(device.Capabilities()) != 1 || device.Capabilities()[0] != core.CapabilityUnsupported {
		t.Fatalf("unverified Samsung adapter exposed controls: %v", device.Capabilities())
	}
	verified := New(core.DiscoveredDevice{Metadata: core.DeviceMetadata{Kind: core.DeviceKindTV, Manufacturer: "Samsung", ControlVerified: true, Capabilities: []core.Capability{core.CapabilityUnsupported}}}, &fakeSecrets{values: map[string]string{}})
	if len(verified.Capabilities()) == 0 || verified.Capabilities()[0] == core.CapabilityUnsupported {
		t.Fatalf("verified Samsung adapter kept discovery-only capabilities: %v", verified.Capabilities())
	}
}

func TestFactoryRequiresVerifiedSamsungControlService(t *testing.T) {
	factory := Factory{Secrets: &fakeSecrets{values: map[string]string{}}}
	base := core.DiscoveredDevice{Metadata: core.DeviceMetadata{Kind: core.DeviceKindTV, Manufacturer: "Samsung"}}
	if factory.Supports(base) {
		t.Fatal("unverified Samsung discovery record received a control adapter")
	}
	base.Metadata.ControlVerified = true
	if !factory.Supports(base) {
		t.Fatal("verified Samsung record did not receive a control adapter")
	}
}

func TestKeyMap(t *testing.T) {
	for _, key := range []string{"HOME", "UP", "VOLUP", "POWEROFF", "PLAY", "0"} {
		if keyMap[key] == "" {
			t.Errorf("missing key %s", key)
		}
	}
	if _, ok := keyMap["NETFLIX"]; ok {
		t.Fatal("Netflix should not be exposed until the adapter can verify it")
	}
	if _, ok := keyMap["YOUTUBE"]; ok {
		t.Fatal("YouTube should not be exposed until the adapter can verify it")
	}
}

func TestFindTokenAcrossHandshakeShapes(t *testing.T) {
	payload := json.RawMessage(`{"clients":[{"attributes":{"name":"Mac mini","token":"abc123"}}]}`)
	if got := findToken(payload); got != "abc123" {
		t.Fatalf("findToken = %q", got)
	}
	payload = json.RawMessage(`{"token":"direct-token"}`)
	if got := findToken(payload); got != "direct-token" {
		t.Fatalf("findToken direct = %q", got)
	}
}

type fakeSecrets struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *fakeSecrets) Get(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.values[key]
	if value == "" {
		return "", errors.New("secret not found")
	}
	return value, nil
}

func (s *fakeSecrets) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

func (s *fakeSecrets) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

func newFakeTV(t *testing.T) (*httptest.Server, *Device, <-chan []byte) {
	t.Helper()
	commands := make(chan []byte, 8)
	secrets := &fakeSecrets{values: map[string]string{stableSecretName("samsung-test"): "fake-tv-token"}}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device":{"power":"on","volume":37,"muted":false,"source":"HDMI1"}}`))
	})
	mux.HandleFunc("/api/v2/channels/samsung.remote.control", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]any{"event": "ms.channel.connect", "data": map[string]string{"token": "fake-tv-token"}})
		_, payload, err := conn.ReadMessage()
		if err == nil {
			commands <- payload
		}
	})
	server := httptest.NewServer(mux)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v2/channels/samsung.remote.control"
	device := &Device{
		metadata:     core.DeviceMetadata{ID: "samsung-test", Kind: core.DeviceKindTV, Manufacturer: "Samsung", IP: "127.0.0.1", Paired: true},
		secrets:      secrets,
		client:       server.Client(),
		infoEndpoint: server.URL + "/api/v2/",
		wsEndpoints:  []string{wsURL},
	}
	return server, device, commands
}

func TestSamsungStateParsesReportedFields(t *testing.T) {
	server, device, _ := newFakeTV(t)
	defer server.Close()
	state, err := device.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Online || state.Power != "on" || state.Volume == nil || *state.Volume != 37 || state.Muted == nil || *state.Muted || state.Source != "HDMI1" {
		t.Fatalf("unexpected state: %+v", state)
	}
}

func TestPowerOffChecksReachabilityBeforeSendingPowerKey(t *testing.T) {
	server, device, commands := newFakeTV(t)
	defer server.Close()
	if _, err := device.Execute(context.Background(), core.Command{Action: core.ActionPowerOff}); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	select {
	case raw := <-commands:
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fake TV did not receive power-off command")
	}
	params := payload["params"].(map[string]any)
	if got := params["DataOfCmd"]; got != "KEY_POWER" {
		t.Fatalf("power-off key = %v, want KEY_POWER", got)
	}
}

func TestPowerOnWaitsForReachabilityAndKeepsRemoteUsable(t *testing.T) {
	server, device, commands := newFakeTV(t)
	defer server.Close()
	device.metadata.MAC = "00:01:02:03:04:05"
	device.wakeWaitWindow = 100 * time.Millisecond
	device.wakePollEvery = 5 * time.Millisecond
	wakeCalled := false
	device.wakeFn = func() error {
		wakeCalled = true
		return nil
	}

	result, err := device.Execute(context.Background(), core.Command{Action: core.ActionPowerOn})
	if err != nil {
		t.Fatal(err)
	}
	if !wakeCalled {
		t.Fatal("power-on did not send Wake-on-LAN")
	}
	if result.State == nil || !result.State.Online {
		t.Fatalf("power-on did not return confirmed online state: %+v", result)
	}
	if !strings.Contains(result.Message, "reachable again") {
		t.Fatalf("power-on message = %q", result.Message)
	}

	if _, err := device.Execute(context.Background(), core.Command{Action: core.ActionKey, Arguments: map[string]string{"key": "HOME"}}); err != nil {
		t.Fatalf("remote command after power-on failed: %v", err)
	}
	select {
	case <-commands:
	case <-time.After(2 * time.Second):
		t.Fatal("fake TV did not receive remote command after power-on")
	}
}

func TestPowerOnDoesNotReportSuccessWhenTVStaysOffline(t *testing.T) {
	server, device, _ := newFakeTV(t)
	defer server.Close()
	device.metadata.MAC = "00:01:02:03:04:05"
	device.infoEndpoint = "http://127.0.0.1:1/api/v2/"
	device.wakeWaitWindow = 20 * time.Millisecond
	device.wakePollEvery = 5 * time.Millisecond
	device.wakeFn = func() error { return nil }

	if result, err := device.Execute(context.Background(), core.Command{Action: core.ActionPowerOn}); err == nil {
		t.Fatalf("power-on reported success while TV stayed offline: %+v", result)
	} else if !strings.Contains(err.Error(), "did not become reachable") {
		t.Fatalf("unexpected offline power-on error: %v", err)
	}
}

func TestPlayPauseUsesSamsungMediaToggleKey(t *testing.T) {
	server, device, commands := newFakeTV(t)
	defer server.Close()
	if _, err := device.Execute(context.Background(), core.Command{Action: core.ActionKey, Arguments: map[string]string{"key": "PLAY"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-commands:
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		params := payload["params"].(map[string]any)
		if got := params["DataOfCmd"]; got != "KEY_PLAYPAUSE" {
			t.Fatalf("play/pause key = %v, want KEY_PLAYPAUSE", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fake TV did not receive play/pause command")
	}
}

func TestVolumeSupportsMultipleStepsInOneCommand(t *testing.T) {
	server, device, commands := newFakeTV(t)
	defer server.Close()
	result, err := device.Execute(context.Background(), core.Command{Action: core.ActionVolumeUp, Arguments: map[string]string{"steps": "3"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Message, "3 steps") {
		t.Fatalf("volume batch message = %q", result.Message)
	}
	for step := 0; step < 3; step++ {
		select {
		case raw := <-commands:
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			params := payload["params"].(map[string]any)
			if got := params["DataOfCmd"]; got != "KEY_VOLUP" {
				t.Fatalf("volume step key = %v", got)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("fake TV did not receive volume step %d", step+1)
		}
	}
}

func TestRemoteKeyRejectsUnauthorizedTVChannel(t *testing.T) {
	secrets := &fakeSecrets{values: map[string]string{stableSecretName("samsung-unauthorized"): "saved-token"}}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/channels/samsung.remote.control", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]any{"event": "ms.channel.error", "data": map[string]string{"message": "remote disabled"}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v2/channels/samsung.remote.control"
	device := &Device{metadata: core.DeviceMetadata{ID: "samsung-unauthorized", Kind: core.DeviceKindTV, Manufacturer: "Samsung", IP: "127.0.0.1", Paired: true}, secrets: secrets, client: server.Client(), wsEndpoints: []string{wsURL}}
	if _, err := device.Execute(context.Background(), core.Command{Action: core.ActionKey, Arguments: map[string]string{"key": "HOME"}}); err == nil || !strings.Contains(err.Error(), "pairing expired") {
		t.Fatalf("unauthorized channel error = %v", err)
	}
	if device.Metadata().Paired {
		t.Fatal("authorization failure did not clear the invalid pairing")
	}
}

func TestRejectedSavedPairingClearsCredentialAndRequiresRePair(t *testing.T) {
	secrets := &fakeSecrets{values: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/channels/samsung.remote.control", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v2/channels/samsung.remote.control"
	device := &Device{metadata: core.DeviceMetadata{ID: "samsung-rejected", Kind: core.DeviceKindTV, Manufacturer: "Samsung", IP: "127.0.0.1", Paired: true}, secrets: secrets, client: server.Client(), wsEndpoints: []string{wsURL}}
	if err := secrets.Set(device.secretName(), "expired-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := device.Execute(context.Background(), core.Command{Action: core.ActionKey, Arguments: map[string]string{"key": "HOME"}}); err == nil || !strings.Contains(err.Error(), "pairing expired") {
		t.Fatalf("expired pairing error = %v", err)
	}
	if device.Metadata().Paired {
		t.Fatal("rejected pairing remained marked as paired")
	}
	if _, err := secrets.Get(device.secretName()); err == nil {
		t.Fatal("expired TV credential was not cleared")
	}
}

func TestPairingDoesNotReuseSavedToken(t *testing.T) {
	secrets := &fakeSecrets{values: map[string]string{}}
	var receivedToken string
	mux := http.NewServeMux()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux.HandleFunc("/api/v2/channels/samsung.remote.control", func(w http.ResponseWriter, r *http.Request) {
		receivedToken = r.URL.Query().Get("token")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]any{"event": "ms.channel.connect", "data": map[string]string{"token": "fresh-token"}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v2/channels/samsung.remote.control"
	device := &Device{metadata: core.DeviceMetadata{ID: "samsung-repair", Kind: core.DeviceKindTV, Manufacturer: "Samsung", IP: "127.0.0.1", Paired: true}, secrets: secrets, client: server.Client(), wsEndpoints: []string{wsURL}}
	if err := secrets.Set(device.secretName(), "old-token"); err != nil {
		t.Fatal(err)
	}
	if err := device.Pair(context.Background()); err != nil {
		t.Fatal(err)
	}
	if receivedToken != "" {
		t.Fatalf("pairing reused saved token %q", receivedToken)
	}
	if got, _ := secrets.Get(device.secretName()); got != "fresh-token" {
		t.Fatalf("fresh pairing token = %q", got)
	}
}

func TestPairingWaitsForReusableToken(t *testing.T) {
	secrets := &fakeSecrets{values: map[string]string{}}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/channels/samsung.remote.control", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]any{"event": "ms.channel.connect", "data": map[string]any{}})
		time.Sleep(20 * time.Millisecond)
		_ = conn.WriteJSON(map[string]any{"event": "ms.channel.connected", "data": map[string]string{"token": "reusable-token"}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v2/channels/samsung.remote.control"
	device := &Device{metadata: core.DeviceMetadata{ID: "samsung-delayed-token", Kind: core.DeviceKindTV, Manufacturer: "Samsung", IP: "127.0.0.1"}, secrets: secrets, client: server.Client(), wsEndpoints: []string{wsURL}}
	if err := device.Pair(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := secrets.Get(device.secretName()); got != "reusable-token" {
		t.Fatalf("saved token = %q, want reusable-token", got)
	}
}

func TestRemoteKeyKeepsPairingWhenSavedTokenChannelIsSilent(t *testing.T) {
	secrets := &fakeSecrets{values: map[string]string{stableSecretName("samsung-silent-channel"): "saved-token"}}
	commands := make(chan []byte, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/channels/samsung.remote.control", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]any{"event": "ms.channel.connect", "data": map[string]string{"token": "saved-token"}})
		_, payload, readErr := conn.ReadMessage()
		if readErr == nil {
			commands <- payload
		}
		time.Sleep(300 * time.Millisecond)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v2/channels/samsung.remote.control"
	device := &Device{metadata: core.DeviceMetadata{ID: "samsung-silent-channel", Kind: core.DeviceKindTV, Manufacturer: "Samsung", IP: "127.0.0.1", Paired: true}, secrets: secrets, client: server.Client(), wsEndpoints: []string{wsURL}}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	result, err := device.Execute(ctx, core.Command{Action: core.ActionKey, Arguments: map[string]string{"key": "HOME"}})
	if err != nil {
		t.Fatalf("silent channel should not invalidate a usable pairing: %v", err)
	}
	if !strings.Contains(result.Message, "did not return an acknowledgement") {
		t.Fatalf("silent channel message = %q", result.Message)
	}
	if !device.Metadata().Paired {
		t.Fatal("silent channel cleared the paired state")
	}
	if token, err := secrets.Get(stableSecretName("samsung-silent-channel")); err != nil || token != "saved-token" {
		t.Fatalf("silent channel changed saved token: %q, err=%v", token, err)
	}
	select {
	case raw := <-commands:
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		params := payload["params"].(map[string]any)
		if got := params["DataOfCmd"]; got != "KEY_HOME" {
			t.Fatalf("remote key = %v, want KEY_HOME", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("silent TV did not receive remote command")
	}
}

func TestRemoteConnectionPersistsRotatedTokenBeforeSendingKey(t *testing.T) {
	secrets := &fakeSecrets{values: map[string]string{stableSecretName("samsung-rotating-token"): "old-token"}}
	var receivedToken string
	commands := make(chan []byte, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/channels/samsung.remote.control", func(w http.ResponseWriter, r *http.Request) {
		receivedToken = r.URL.Query().Get("token")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]any{"event": "ms.channel.connect", "data": map[string]string{"token": "rotated-token"}})
		if _, _, err := conn.ReadMessage(); err == nil {
			commands <- []byte("received")
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v2/channels/samsung.remote.control"
	device := &Device{metadata: core.DeviceMetadata{ID: "samsung-rotating-token", Kind: core.DeviceKindTV, Manufacturer: "Samsung", IP: "127.0.0.1", Paired: true}, secrets: secrets, client: server.Client(), wsEndpoints: []string{wsURL}}
	if _, err := device.Execute(context.Background(), core.Command{Action: core.ActionKey, Arguments: map[string]string{"key": "HOME"}}); err != nil {
		t.Fatalf("rotated-token command failed: %v", err)
	}
	if receivedToken != "old-token" {
		t.Fatalf("saved token sent to TV = %q, want old-token", receivedToken)
	}
	if token, err := secrets.Get(device.secretName()); err != nil || token != "rotated-token" {
		t.Fatalf("rotated token was not persisted: %q, err=%v", token, err)
	}
	select {
	case <-commands:
	case <-time.After(2 * time.Second):
		t.Fatal("rotated-token TV did not receive remote command")
	}
}

func TestPowerOffDoesNotToggleUnreachableTV(t *testing.T) {
	server, device, commands := newFakeTV(t)
	defer server.Close()
	device.infoEndpoint = "http://127.0.0.1:1/api/v2/"
	if _, err := device.Execute(context.Background(), core.Command{Action: core.ActionPowerOff}); err == nil || !strings.Contains(err.Error(), "no power toggle was sent") {
		t.Fatalf("unexpected unreachable-TV result: %v", err)
	}
	select {
	case command := <-commands:
		t.Fatalf("unreachable TV received command: %s", command)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPairPersistsTVToken(t *testing.T) {
	server, device, _ := newFakeTV(t)
	defer server.Close()
	if err := device.Pair(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := device.secrets.Get(device.secretName()); err != nil || got != "fake-tv-token" {
		t.Fatalf("stored token = %q, err=%v", got, err)
	}
	if !device.Metadata().Paired {
		t.Fatal("pairing did not update the live adapter state")
	}
}

func TestUnpairDeletesTVToken(t *testing.T) {
	server, device, _ := newFakeTV(t)
	defer server.Close()
	if err := device.Pair(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := device.Unpair(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := device.secrets.Get(device.secretName()); err == nil {
		t.Fatal("TV token still exists after unpair")
	}
	if device.Metadata().Paired {
		t.Fatal("unpairing did not update the live adapter state")
	}
}
