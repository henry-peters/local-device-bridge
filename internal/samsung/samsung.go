package samsung

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/local-device-bridge/local-device-bridge/internal/core"
)

type SecretStore interface {
	Get(string) (string, error)
	Set(string, string) error
	Delete(string) error
}

type Device struct {
	metadata     core.DeviceMetadata
	secrets      SecretStore
	client       *http.Client
	infoEndpoint string
	wsEndpoints  []string
	// wakeFn and the wait settings are injectable for deterministic tests. In
	// production wakeFn is nil and WakeOnLANForDevice is used.
	wakeFn         func() error
	wakeWaitWindow time.Duration
	wakePollEvery  time.Duration
}

const (
	samsungClientName = "Local Device Bridge"
	// Samsung TVs can acknowledge Wake-on-LAN before their local control
	// service has finished starting. Keep the pairing request alive long enough
	// for ports 8001/8002 to come up, but stay below the API server's timeout.
	wakePairRetryWindow = 12 * time.Second
	wakePairRetryEvery  = 2 * time.Second
	// A Samsung TV can receive the magic packet before its HTTP/WebSocket
	// services are ready. Do not report a successful power-on until the local
	// API is reachable again.
	wakeConfirmWindow = 15 * time.Second
	wakeConfirmEvery  = 1 * time.Second
)

var errPairingRejected = errors.New("Samsung TV rejected the saved pairing")
var errRemoteChannelTimeout = errors.New("Samsung TV did not send a remote-channel acknowledgement")

func New(info core.DiscoveredDevice, secrets SecretStore) *Device {
	if (info.Metadata.ControlVerified || info.Metadata.Paired) && (len(info.Metadata.Capabilities) == 0 || (len(info.Metadata.Capabilities) == 1 && info.Metadata.Capabilities[0] == core.CapabilityUnsupported)) {
		// Full capabilities are reserved for a verified or previously paired
		// TV. A discovery name alone must never unlock a fake remote.
		info.Metadata.Capabilities = []core.Capability{core.CapabilityStatus, core.CapabilityPower, core.CapabilityVolume, core.CapabilityMute, core.CapabilityPlayback, core.CapabilityNavigation, core.CapabilitySource, core.CapabilityChannel, core.CapabilityWakeOnLAN}
	}
	return &Device{
		metadata:       info.Metadata,
		secrets:        secrets,
		client:         &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}},
		wakeWaitWindow: wakeConfirmWindow,
		wakePollEvery:  wakeConfirmEvery,
	}
}

func (d *Device) Metadata() core.DeviceMetadata   { return d.metadata }
func (d *Device) Capabilities() []core.Capability { return d.metadata.Capabilities }

func (d *Device) Pair(ctx context.Context) error {
	// Pairing deliberately omits any saved token. This forces Samsung to
	// present a fresh confirmation prompt and makes re-pairing reliable after
	// a TV reset or a changed network-remote setting.
	conn, err := d.connectPairing(ctx)
	if err != nil && isReachabilityError(err) {
		// Pairing is a setup action, so a sleeping or just-waking TV may need
		// both a best-effort magic packet and a short service-start retry. The
		// retry also runs when MAC discovery is unavailable, which avoids the
		// intermittent "pair sometimes works" behavior on Wi-Fi TVs.
		if d.metadata.MAC != "" {
			_ = WakeOnLANForDevice(d.metadata.MAC, d.metadata.IP)
		}
		conn, err = d.connectPairingAfterWake(ctx)
	}
	if err != nil {
		return fmt.Errorf("pair with TV: %w", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	connected := false
	savedToken := false
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			if connected {
				return errors.New("TV accepted the prompt but did not return a reusable pairing token; unpair the TV, pair it again, and accept the prompt")
			}
			break
		}
		var event struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		if json.Unmarshal(payload, &event) != nil {
			continue
		}
		if event.Event == "ms.channel.error" {
			message := findErrorMessage(event.Data)
			if message == "" {
				message = "the TV rejected the pairing request"
			}
			return fmt.Errorf("%w: %s; accept the prompt on the TV and pair again", errPairingRejected, message)
		}
		if event.Event != "ms.channel.connect" && event.Event != "ms.channel.connected" {
			continue
		}
		connected = true
		if token := findToken(event.Data); token != "" {
			if err := d.secrets.Set(d.secretName(), token); err != nil {
				return fmt.Errorf("save TV token: %w", err)
			}
			savedToken = true
			if legacy := d.legacySecretName(); legacy != d.secretName() {
				_ = d.secrets.Delete(legacy)
			}
		}
		d.metadata.Paired = true
		if savedToken {
			return nil
		}
		// Do not report a durable pairing until Samsung has returned a token.
		// A connection event without a token is only a session handshake; after
		// a daemon restart it would otherwise cause a fresh prompt on every key.
	}
	if connected {
		return errors.New("TV accepted the prompt but did not return a reusable pairing token; unpair the TV, pair it again, and accept the prompt")
	}
	return errors.New("TV pairing was not confirmed; approve the prompt on the TV and try again")
}

func (d *Device) connectPairingAfterWake(ctx context.Context) (*websocket.Conn, error) {
	deadline := time.Now().Add(wakePairRetryWindow)
	var lastErr error
	for {
		conn, err := d.connectPairing(ctx)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, lastErr
		}
		timer := time.NewTimer(wakePairRetryEvery)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		}
	}
}

func isReachabilityError(err error) bool {
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"no route to host", "network is unreachable", "connection refused", "i/o timeout", "timeout", "timed out"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (d *Device) PairWith(ctx context.Context, options core.PairOptions) error {
	_ = options
	return d.Pair(ctx)
}

func (d *Device) Unpair(_ context.Context) error {
	var firstErr error
	for _, name := range d.secretNames() {
		if err := d.secrets.Delete(name); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	d.metadata.Paired = false
	if firstErr != nil {
		return fmt.Errorf("remove TV pairing: %w", firstErr)
	}
	return nil
}

func findErrorMessage(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return findErrorMessageValue(value)
}

func findErrorMessageValue(value any) string {
	switch item := value.(type) {
	case map[string]any:
		for _, key := range []string{"message", "error", "reason"} {
			if text, ok := item[key].(string); ok && text != "" {
				return text
			}
		}
		for _, child := range item {
			if message := findErrorMessageValue(child); message != "" {
				return message
			}
		}
	case []any:
		for _, child := range item {
			if message := findErrorMessageValue(child); message != "" {
				return message
			}
		}
	}
	return ""
}

func findToken(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return findTokenValue(value)
}

func findTokenValue(value any) string {
	switch item := value.(type) {
	case map[string]any:
		if token, ok := item["token"]; ok {
			switch tokenValue := token.(type) {
			case string:
				if tokenValue != "" {
					return tokenValue
				}
			case float64:
				if tokenValue != 0 {
					return strconv.FormatInt(int64(tokenValue), 10)
				}
			}
		}
		for _, child := range item {
			if token := findTokenValue(child); token != "" {
				return token
			}
		}
	case []any:
		for _, child := range item {
			if token := findTokenValue(child); token != "" {
				return token
			}
		}
	}
	return ""
}

func (d *Device) State(ctx context.Context) (core.DeviceState, error) {
	state := core.DeviceState{DeviceID: d.metadata.ID, Updated: time.Now().UTC()}
	var lastErr error
	for _, endpoint := range d.infoURLs() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := d.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("TV returned HTTP %d", resp.StatusCode)
			_ = resp.Body.Close()
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		state.Online = true
		var payload struct {
			Device struct {
				Power  string `json:"power"`
				Volume *int   `json:"volume"`
				Muted  *bool  `json:"muted"`
				Source string `json:"source"`
			} `json:"device"`
		}
		if json.Unmarshal(body, &payload) == nil {
			state.Power = payload.Device.Power
			state.Volume = payload.Device.Volume
			state.Muted = payload.Device.Muted
			state.Source = payload.Device.Source
		}
		return state, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no Samsung TV API endpoint responded")
	}
	lastErr = formatReachabilityError(d.metadata.IP, lastErr)
	state.Error = lastErr.Error()
	return state, lastErr
}

func formatReachabilityError(ip string, err error) error {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "connection refused"):
		return fmt.Errorf("Samsung TV at %s rejected the local API connection; turn it on and enable its mobile/device connection or network remote setting: %w", ip, err)
	case strings.Contains(message, "no route to host"), strings.Contains(message, "network is unreachable"):
		return fmt.Errorf("Samsung TV at %s is unreachable from the bridge; confirm the TV and bridge share the same non-guest LAN and scan again: %w", ip, err)
	default:
		return fmt.Errorf("Samsung TV at %s did not answer the local API; turn it on, verify network remote access, and scan again: %w", ip, err)
	}
}

func (d *Device) Execute(ctx context.Context, cmd core.Command) (core.CommandResult, error) {
	switch cmd.Action {
	case core.ActionStatus:
		state, err := d.State(ctx)
		return core.CommandResult{Message: "TV status read", State: &state}, err
	case core.ActionPowerOn:
		if d.metadata.MAC == "" {
			return core.CommandResult{}, errors.New("TV has no MAC address for Wake-on-LAN")
		}
		wake := d.wakeFn
		if wake == nil {
			wake = func() error { return WakeOnLANForDevice(d.metadata.MAC, d.metadata.IP) }
		}
		if err := wake(); err != nil {
			return core.CommandResult{}, err
		}
		state, err := d.waitForReachable(ctx)
		if err != nil {
			return core.CommandResult{}, err
		}
		message := "Wake-on-LAN sent; the TV local control service is reachable again"
		if state.Power != "" {
			message += " (power: " + state.Power + ")"
		}
		return core.CommandResult{Message: message, State: &state}, nil
	case core.ActionPowerOff:
		// Samsung's local remote exposes POWER as the working power-off key
		// on the supported consumer models. Preflight the local endpoint so
		// an offline TV is never sent a toggle that could wake it.
		state, err := d.State(ctx)
		if err != nil {
			return core.CommandResult{}, fmt.Errorf("TV is not reachable; no power toggle was sent: %w", err)
		}
		if !state.Online {
			return core.CommandResult{Message: "TV is not reachable; no power toggle was sent"}, nil
		}
		result, err := d.sendKey(ctx, "KEY_POWER")
		if err != nil {
			return core.CommandResult{}, err
		}
		result.Message = "Power-off command sent; Samsung does not report the resulting panel state"
		return result, nil
	case core.ActionVolumeUp:
		return d.sendVolume(ctx, "KEY_VOLUP", cmd.Arguments)
	case core.ActionVolumeDown:
		return d.sendVolume(ctx, "KEY_VOLDOWN", cmd.Arguments)
	case core.ActionMute:
		return d.sendKey(ctx, "KEY_MUTE")
	case core.ActionKey:
		key := strings.ToUpper(strings.TrimSpace(cmd.Arguments["key"]))
		mapped, ok := keyMap[key]
		if !ok {
			return core.CommandResult{}, fmt.Errorf("unsupported TV key %q", key)
		}
		return d.sendKey(ctx, mapped)
	case core.ActionSource:
		key, ok := sourceKeys[strings.ToLower(strings.TrimSpace(cmd.Arguments["source"]))]
		if !ok {
			return core.CommandResult{}, fmt.Errorf("unsupported TV source %q", cmd.Arguments["source"])
		}
		return d.sendKey(ctx, key)
	case core.ActionChannel:
		return d.sendDigits(ctx, cmd.Arguments["channel"])
	case core.ActionVolumeSet:
		return core.CommandResult{}, errors.New("Samsung local remote does not expose a reliable absolute-volume state; use volume_up or volume_down")
	default:
		return core.CommandResult{}, fmt.Errorf("unsupported Samsung action %q", cmd.Action)
	}
}

func (d *Device) sendVolume(ctx context.Context, key string, arguments map[string]string) (core.CommandResult, error) {
	steps := 1
	if raw := strings.TrimSpace(arguments["steps"]); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 20 {
			return core.CommandResult{}, errors.New("volume steps must be a number from 1 to 20")
		}
		steps = parsed
	}
	var result core.CommandResult
	for step := 0; step < steps; step++ {
		var err error
		result, err = d.sendKey(ctx, key)
		if err != nil {
			return core.CommandResult{}, err
		}
		if step+1 < steps {
			timer := time.NewTimer(80 * time.Millisecond)
			select {
			case <-timer.C:
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return core.CommandResult{}, ctx.Err()
			}
		}
	}
	if steps > 1 {
		result.Message = fmt.Sprintf("Volume changed %d steps", steps)
	}
	return result, nil
}

func (d *Device) waitForReachable(ctx context.Context) (core.DeviceState, error) {
	waitWindow := d.wakeWaitWindow
	if waitWindow <= 0 {
		waitWindow = wakeConfirmWindow
	}
	pollEvery := d.wakePollEvery
	if pollEvery <= 0 {
		pollEvery = wakeConfirmEvery
	}
	deadline := time.Now().Add(waitWindow)
	var lastState core.DeviceState
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return lastState, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		attemptTimeout := 2 * time.Second
		if remaining < attemptTimeout {
			attemptTimeout = remaining
		}
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		state, err := d.State(attemptCtx)
		cancel()
		lastState = state
		if err == nil && state.Online {
			return state, nil
		}
		lastErr = err
		remaining = time.Until(deadline)
		if remaining <= 0 {
			break
		}
		timer := time.NewTimer(pollEvery)
		if pollEvery > remaining {
			timer.Stop()
			timer = time.NewTimer(remaining)
		}
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return lastState, ctx.Err()
		}
	}
	if lastErr == nil {
		lastErr = errors.New("the TV local API did not respond")
	}
	return lastState, fmt.Errorf("Wake-on-LAN was sent, but the TV did not become reachable within %s; enable network standby or Wake-on-WLAN and verify the TV is on the same LAN: %w", waitWindow.Round(time.Second), lastErr)
}

func (d *Device) sendDigits(ctx context.Context, value string) (core.CommandResult, error) {
	if value == "" {
		return core.CommandResult{}, errors.New("channel is required")
	}
	for _, digit := range value {
		key, ok := keyMap[string(digit)]
		if !ok {
			return core.CommandResult{}, fmt.Errorf("invalid channel %q", value)
		}
		if _, err := d.sendKey(ctx, key); err != nil {
			return core.CommandResult{}, err
		}
		select {
		case <-ctx.Done():
			return core.CommandResult{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return d.sendKey(ctx, "KEY_ENTER")
}

func (d *Device) sendKey(ctx context.Context, key string) (core.CommandResult, error) {
	if !d.metadata.Paired {
		return core.CommandResult{}, errors.New("Samsung TV pairing is required; open Access Rules and pair the TV first")
	}
	if d.token() == "" {
		// Metadata can survive a daemon restart even when an older build marked
		// a tokenless session as paired. Never open a tokenless command channel:
		// Samsung treats it as a new client and shows the pairing prompt again.
		d.metadata.Paired = false
		return core.CommandResult{}, errors.New("Samsung TV pairing is incomplete; pair the TV again so the bridge can save its reusable token")
	}
	conn, err := d.connect(ctx)
	if err != nil {
		if errors.Is(err, errPairingRejected) {
			_ = d.invalidatePairing()
			return core.CommandResult{}, errors.New("Samsung TV pairing expired or was rejected; the saved pairing was cleared. Pair the TV again")
		}
		return core.CommandResult{}, formatRemoteAccessError(d.metadata.IP, err)
	}
	defer conn.Close()
	payload := map[string]any{"method": "ms.remote.control", "params": map[string]string{"Cmd": "Click", "DataOfCmd": key, "Option": "false", "TypeOfRemote": "SendRemoteKey"}}
	b, _ := json.Marshal(payload)
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		return core.CommandResult{}, err
	}
	acknowledged, err := readRemoteChannelResponse(ctx, conn)
	if err != nil {
		if errors.Is(err, errPairingRejected) {
			// Only an explicit Samsung authorization error proves that the saved
			// token is invalid. A number of TV generations accept the command and
			// then close or stay silent on the remote channel. Treating that silent
			// response as an expired pairing caused the dashboard to relock itself
			// on every command and made users approve the TV repeatedly.
			_ = d.invalidatePairing()
			return core.CommandResult{}, errors.New("Samsung TV pairing expired or was rejected; the saved pairing was cleared. Pair the TV again")
		}
		if errors.Is(err, errRemoteChannelTimeout) || strings.Contains(strings.ToLower(err.Error()), "closed the remote channel") {
			return core.CommandResult{Message: "TV accepted remote connection; sent " + key + " (the TV did not return an acknowledgement)"}, nil
		}
		return core.CommandResult{}, err
	}
	if !acknowledged {
		return core.CommandResult{Message: "TV accepted remote connection; sent " + key + " (the TV did not return an acknowledgement)"}, nil
	}
	return core.CommandResult{Message: "TV accepted remote connection; sent " + key}, nil
}

func formatRemoteAccessError(ip string, err error) error {
	if isReachabilityError(err) {
		return fmt.Errorf("Samsung TV at %s is unreachable; no remote command was sent. Confirm the TV is awake and on the same non-guest LAN, then scan again: %w", ip, err)
	}
	return fmt.Errorf("Samsung TV at %s rejected the remote channel; enable its mobile/device connection or network remote setting and pair again: %w", ip, err)
}

func readRemoteChannelResponse(ctx context.Context, conn *websocket.Conn) (bool, error) {
	deadline := time.Now().Add(1200 * time.Millisecond)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = conn.SetReadDeadline(deadline)
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				return false, errRemoteChannelTimeout
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return false, errRemoteChannelTimeout
			}
			return false, errors.New("TV closed the remote channel; enable its mobile/device connection or network remote setting and pair it again")
		}
		var event struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		if json.Unmarshal(payload, &event) == nil {
			if event.Event == "ms.channel.error" {
				message := findErrorMessage(event.Data)
				if message == "" {
					message = "the TV rejected the remote channel"
				}
				return false, fmt.Errorf("%w: %s", errPairingRejected, message)
			}
			if event.Event == "ms.channel.connect" || event.Event == "ms.channel.connected" {
				return true, nil
			}
		}
	}
}

func (d *Device) connect(ctx context.Context) (*websocket.Conn, error) {
	var (
		conn *websocket.Conn
		err  error
	)
	if len(d.wsEndpoints) > 0 {
		conn, err = d.connectEndpoints(ctx, d.wsEndpoints, d.token())
	} else {
		conn, err = d.connectEndpoints(ctx, d.defaultWSEndpoints(), d.token())
	}
	if err != nil {
		return nil, err
	}
	if err := d.readSavedPairingHandshake(ctx, conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (d *Device) connectPairing(ctx context.Context) (*websocket.Conn, error) {
	if len(d.wsEndpoints) > 0 {
		return d.connectEndpoints(ctx, d.wsEndpoints, "")
	}
	return d.connectEndpoints(ctx, d.defaultWSEndpoints(), "")
}

// readSavedPairingHandshake must complete before a remote key is written. A
// Samsung TV can return a rotated token in this first channel event, including
// after it has shown a one-time approval prompt. Previously the bridge wrote
// the key immediately and discarded that event, which made the next command
// look like a new client and caused repeated prompts.
func (d *Device) readSavedPairingHandshake(ctx context.Context, conn *websocket.Conn) error {
	deadline := time.Now().Add(4 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = conn.SetReadDeadline(deadline)
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				return errors.New("Samsung TV did not confirm the saved pairing; pair the TV again if it is showing an approval prompt")
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return errors.New("Samsung TV did not confirm the saved pairing; pair the TV again if it is showing an approval prompt")
			}
			return err
		}
		var event struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		if json.Unmarshal(payload, &event) != nil {
			continue
		}
		if event.Event == "ms.channel.error" {
			message := findErrorMessage(event.Data)
			if message == "" {
				message = "the TV rejected the saved pairing"
			}
			return fmt.Errorf("%w: %s", errPairingRejected, message)
		}
		if event.Event != "ms.channel.connect" && event.Event != "ms.channel.connected" {
			continue
		}
		if token := findToken(event.Data); token != "" && token != d.token() {
			if err := d.secrets.Set(d.secretName(), token); err != nil {
				return fmt.Errorf("save refreshed TV token: %w", err)
			}
		}
		return nil
	}
}

func (d *Device) defaultWSEndpoints() []string {
	name := base64.RawStdEncoding.EncodeToString([]byte(samsungClientName))
	query := url.Values{"name": []string{name}}
	return []string{
		"wss://" + net.JoinHostPort(d.metadata.IP, "8002") + "/api/v2/channels/samsung.remote.control?" + query.Encode(),
		"ws://" + net.JoinHostPort(d.metadata.IP, "8001") + "/api/v2/channels/samsung.remote.control?" + query.Encode(),
	}
}

func (d *Device) connectEndpoints(ctx context.Context, urls []string, token string) (*websocket.Conn, error) {
	name := base64.RawStdEncoding.EncodeToString([]byte(samsungClientName))
	dialer := websocket.Dialer{HandshakeTimeout: 4 * time.Second, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	var lastErr error
	var rejected error
	for _, endpoint := range urls {
		parsed, parseErr := url.Parse(endpoint)
		if parseErr != nil {
			lastErr = parseErr
			continue
		}
		query := parsed.Query()
		query.Set("name", name)
		if token != "" {
			query.Set("token", token)
		} else {
			query.Del("token")
		}
		parsed.RawQuery = query.Encode()
		conn, response, err := dialer.DialContext(ctx, parsed.String(), nil)
		if err == nil {
			return conn, nil
		}
		if response != nil && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) {
			rejected = fmt.Errorf("%w: TV rejected the saved pairing (HTTP %d)", errPairingRejected, response.StatusCode)
			continue
		}
		lastErr = err
	}
	if rejected != nil {
		return nil, rejected
	}
	return nil, lastErr
}

func (d *Device) infoURLs() []string {
	if d.infoEndpoint != "" {
		return []string{d.infoEndpoint}
	}
	return []string{
		"http://" + net.JoinHostPort(d.metadata.IP, "8001") + "/api/v2/",
		"https://" + net.JoinHostPort(d.metadata.IP, "8002") + "/api/v2/",
	}
}
func (d *Device) token() string {
	for _, name := range d.secretNames() {
		if token, err := d.secrets.Get(name); err == nil && token != "" {
			return token
		}
	}
	return ""
}

func (d *Device) secretName() string {
	identity := strings.TrimSpace(d.metadata.DUID)
	if identity == "" {
		identity = strings.TrimSpace(d.metadata.MAC)
	}
	if identity == "" {
		identity = string(d.metadata.ID)
	}
	return stableSecretName(identity)
}

func (d *Device) legacySecretName() string { return "tv-token-" + string(d.metadata.ID) }

func (d *Device) secretNames() []string {
	// Keep every stable identity variant as a lookup candidate. Some Samsung
	// generations expose DUID only after the first metadata probe, while
	// others expose Wi-Fi MAC first. This prevents a DHCP or metadata change
	// from making a valid saved pairing appear to disappear.
	identities := []string{strings.TrimSpace(d.metadata.DUID), strings.TrimSpace(d.metadata.MAC), string(d.metadata.ID)}
	result := make([]string, 0, len(identities)+1)
	seen := make(map[string]bool)
	for _, identity := range identities {
		if identity == "" {
			continue
		}
		name := stableSecretName(identity)
		if !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}
	legacy := d.legacySecretName()
	if !seen[legacy] {
		result = append(result, legacy)
	}
	return result
}

func stableSecretName(identity string) string {
	hash := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(identity))))
	return "tv-token-" + hex.EncodeToString(hash[:8])
}

func (d *Device) invalidatePairing() error {
	return d.Unpair(context.Background())
}

func (f Factory) Supports(info core.DiscoveredDevice) bool {
	return info.Metadata.Manufacturer == "Samsung" && info.Metadata.Kind == core.DeviceKindTV && (info.Metadata.ControlVerified || info.Metadata.Paired)
}

type Factory struct{ Secrets SecretStore }

func (f Factory) Create(_ context.Context, info core.DiscoveredDevice) (core.Device, error) {
	return New(info, f.Secrets), nil
}

func WakeOnLAN(mac string) error {
	hw, err := net.ParseMAC(strings.ReplaceAll(mac, ":", "-"))
	if err != nil || len(hw) != 6 {
		return fmt.Errorf("invalid TV MAC address %q", mac)
	}
	return sendMagicPackets(hw, []net.IP{net.IPv4bcast})
}

func WakeOnLANForDevice(mac, ip string) error {
	hw, err := net.ParseMAC(strings.ReplaceAll(mac, ":", "-"))
	if err != nil || len(hw) != 6 {
		return fmt.Errorf("invalid TV MAC address %q", mac)
	}
	targets := []net.IP{net.IPv4bcast}
	if target := net.ParseIP(ip).To4(); target != nil {
		targets = append(targets, target)
		for _, address := range localBroadcasts(target) {
			targets = append(targets, address)
		}
	}
	return sendMagicPackets(hw, uniqueIPv4(targets))
}

func sendMagicPackets(mac net.HardwareAddr, targets []net.IP) error {
	packet := magicPacket(mac)
	var lastErr error
	sent := false
	for attempt := 0; attempt < 3; attempt++ {
		for _, target := range targets {
			for _, port := range []int{9, 7} {
				conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: target, Port: port})
				if err != nil {
					lastErr = err
					continue
				}
				if _, err := conn.Write(packet); err != nil {
					lastErr = err
				} else {
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
		return fmt.Errorf("send Wake-on-LAN packet: %w", lastErr)
	}
	return nil
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
			broadcast := net.IPv4(ip4[0]|^mask[0], ip4[1]|^mask[1], ip4[2]|^mask[2], ip4[3]|^mask[3]).To4()
			result = append(result, broadcast)
		}
	}
	return result
}

func uniqueIPv4(values []net.IP) []net.IP {
	seen := map[string]bool{}
	result := make([]net.IP, 0, len(values))
	for _, value := range values {
		value = value.To4()
		if value == nil || seen[value.String()] {
			continue
		}
		seen[value.String()] = true
		result = append(result, value)
	}
	return result
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

var keyMap = map[string]string{
	"0": "KEY_0", "1": "KEY_1", "2": "KEY_2", "3": "KEY_3", "4": "KEY_4", "5": "KEY_5", "6": "KEY_6", "7": "KEY_7", "8": "KEY_8", "9": "KEY_9",
	"UP": "KEY_UP", "DOWN": "KEY_DOWN", "LEFT": "KEY_LEFT", "RIGHT": "KEY_RIGHT", "ENTER": "KEY_ENTER", "OK": "KEY_ENTER", "RETURN": "KEY_RETURN", "BACK": "KEY_RETURN", "HOME": "KEY_HOME", "MENU": "KEY_MENU", "EXIT": "KEY_EXIT", "INFO": "KEY_INFO", "PLAY": "KEY_PLAYPAUSE", "PAUSE": "KEY_PLAYPAUSE", "PLAYPAUSE": "KEY_PLAYPAUSE", "PLAY_PAUSE": "KEY_PLAYPAUSE", "STOP": "KEY_STOP", "VOLUP": "KEY_VOLUP", "VOLDOWN": "KEY_VOLDOWN", "MUTE": "KEY_MUTE", "CHUP": "KEY_CHUP", "CHDOWN": "KEY_CHDOWN", "SOURCE": "KEY_SOURCE", "POWER": "KEY_POWER", "POWEROFF": "KEY_POWEROFF",
}

var sourceKeys = map[string]string{"hdmi1": "KEY_HDMI1", "hdmi2": "KEY_HDMI2", "hdmi3": "KEY_HDMI3", "hdmi4": "KEY_HDMI4", "tv": "KEY_TV", "source": "KEY_SOURCE"}

var _ = strconv.Itoa
