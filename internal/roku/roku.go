// Package roku implements Roku's local External Control Protocol (ECP).
//
// Roku exposes a small HTTP API on port 8060. It uses the same normalized
// command model as the Samsung adapter so the dashboard, CLI, Telegram, and
// agent manifest can present one remote layout while each adapter translates
// keys to its native protocol.
package roku

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/local-device-bridge/local-device-bridge/internal/core"
)

type Device struct {
	metadata core.DeviceMetadata
	client   *http.Client
	// baseURL is empty in production and exists so protocol behavior can be
	// tested against a local fake ECP server without binding a test process to
	// port 8060.
	baseURL string
}

func New(info core.DiscoveredDevice) *Device {
	return &Device{
		metadata: info.Metadata,
		client:   &http.Client{Timeout: 4 * time.Second},
	}
}

func (d *Device) Metadata() core.DeviceMetadata { return d.metadata }

func (d *Device) Capabilities() []core.Capability {
	if len(d.metadata.Capabilities) > 0 && !containsUnsupported(d.metadata.Capabilities) {
		return d.metadata.Capabilities
	}
	return capabilities()
}

// Roku ECP does not use a pairing token. The TV setting “Control by mobile
// apps” must be enabled, but there is no approval handshake to persist.
func (d *Device) Pair(context.Context) error   { return nil }
func (d *Device) Unpair(context.Context) error { return nil }

func (d *Device) State(ctx context.Context) (core.DeviceState, error) {
	state := core.DeviceState{DeviceID: d.metadata.ID, Updated: time.Now().UTC()}
	var info deviceInfo
	if err := d.getXML(ctx, "query/device-info", &info); err != nil {
		state.Error = fmt.Sprintf("Roku local control unavailable: %v", err)
		return state, stateError(state.Error)
	}
	state.Online = true
	state.Power = firstNonEmpty(info.PowerMode, info.PowerModeAlt)
	return state, nil
}

func (d *Device) Execute(ctx context.Context, cmd core.Command) (core.CommandResult, error) {
	switch cmd.Action {
	case core.ActionStatus:
		state, err := d.State(ctx)
		return core.CommandResult{Message: "Roku status read", State: &state}, err
	case core.ActionPowerOn:
		if d.metadata.MAC == "" {
			return core.CommandResult{}, errors.New("Roku power-on needs Wake-on-LAN, but discovery did not provide a MAC address; use the TV remote once or enable network wake")
		}
		if err := wakeOnLAN(d.metadata.MAC); err != nil {
			return core.CommandResult{}, err
		}
		return core.CommandResult{Message: "Wake-on-LAN sent to Roku; power-on confirmation depends on the TV model"}, nil
	case core.ActionPowerOff:
		if err := d.post(ctx, "keypress/PowerOff"); err != nil {
			return core.CommandResult{}, fmt.Errorf("Roku power-off failed; enable Control by mobile apps: %w", err)
		}
		return core.CommandResult{Message: "Roku power-off command sent"}, nil
	case core.ActionVolumeUp:
		return d.volume(ctx, "VolumeUp", cmd.Arguments)
	case core.ActionVolumeDown:
		return d.volume(ctx, "VolumeDown", cmd.Arguments)
	case core.ActionMute:
		if err := d.post(ctx, "keypress/VolumeMute"); err != nil {
			return core.CommandResult{}, fmt.Errorf("Roku mute failed; enable Control by mobile apps: %w", err)
		}
		return core.CommandResult{Message: "Roku mute command sent"}, nil
	case core.ActionKey:
		key, ok := keyMap[strings.ToUpper(strings.TrimSpace(cmd.Arguments["key"]))]
		if !ok {
			return core.CommandResult{}, fmt.Errorf("unsupported Roku remote key %q", cmd.Arguments["key"])
		}
		if err := d.post(ctx, "keypress/"+key); err != nil {
			return core.CommandResult{}, fmt.Errorf("Roku remote command failed; enable Control by mobile apps: %w", err)
		}
		return core.CommandResult{Message: "Roku remote command sent: " + key}, nil
	case core.ActionSource:
		key, ok := sourceKey(cmd.Arguments["source"])
		if !ok {
			return core.CommandResult{}, fmt.Errorf("unsupported Roku source %q; use tuner, hdmi1, hdmi2, hdmi3, or hdmi4", cmd.Arguments["source"])
		}
		if err := d.post(ctx, "keypress/"+key); err != nil {
			return core.CommandResult{}, fmt.Errorf("Roku source command failed: %w", err)
		}
		return core.CommandResult{Message: "Roku source command sent"}, nil
	case core.ActionChannel:
		channel := strings.TrimSpace(cmd.Arguments["channel"])
		switch channel {
		case "+", "up":
			if err := d.post(ctx, "keypress/ChannelUp"); err != nil {
				return core.CommandResult{}, err
			}
		case "-", "down":
			if err := d.post(ctx, "keypress/ChannelDown"); err != nil {
				return core.CommandResult{}, err
			}
		default:
			return core.CommandResult{}, errors.New("Roku channel accepts only up or down")
		}
		return core.CommandResult{Message: "Roku channel command sent"}, nil
	case core.ActionVolumeSet:
		return core.CommandResult{}, errors.New("Roku ECP does not expose a portable absolute-volume command; use volume_up or volume_down with steps")
	default:
		return core.CommandResult{}, fmt.Errorf("unsupported Roku action %q", cmd.Action)
	}
}

func (d *Device) volume(ctx context.Context, key string, arguments map[string]string) (core.CommandResult, error) {
	steps := 1
	if raw := strings.TrimSpace(arguments["steps"]); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 20 {
			return core.CommandResult{}, errors.New("volume steps must be a number from 1 to 20")
		}
		steps = value
	}
	for index := 0; index < steps; index++ {
		if err := d.post(ctx, "keypress/"+key); err != nil {
			return core.CommandResult{}, fmt.Errorf("Roku volume command failed: %w", err)
		}
		if index+1 < steps {
			select {
			case <-time.After(80 * time.Millisecond):
			case <-ctx.Done():
				return core.CommandResult{}, ctx.Err()
			}
		}
	}
	return core.CommandResult{Message: fmt.Sprintf("Roku volume changed %d step(s)", steps)}, nil
}

func (d *Device) post(ctx context.Context, command string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint(command), strings.NewReader(""))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Roku returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (d *Device) getXML(ctx context.Context, command string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.endpoint(command), nil)
	if err != nil {
		return err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Roku returned HTTP %d", resp.StatusCode)
	}
	if err := xml.NewDecoder(resp.Body).Decode(target); err != nil {
		return err
	}
	return nil
}

func (d *Device) endpoint(command string) string {
	if d.baseURL != "" {
		return strings.TrimRight(d.baseURL, "/") + "/" + strings.TrimPrefix(command, "/")
	}
	return "http://" + net.JoinHostPort(d.metadata.IP, "8060") + "/" + strings.TrimPrefix(command, "/")
}

type deviceInfo struct {
	PowerMode    string `xml:"power-mode"`
	PowerModeAlt string `xml:"powerMode"`
}

func stateError(message string) error { return errors.New(message) }

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "unknown"
}

func sourceKey(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "tuner", "tv", "antenna":
		return "InputTuner", true
	case "hdmi1":
		return "InputHDMI1", true
	case "hdmi2":
		return "InputHDMI2", true
	case "hdmi3":
		return "InputHDMI3", true
	case "hdmi4":
		return "InputHDMI4", true
	case "av", "av1":
		return "InputAV1", true
	default:
		return "", false
	}
}

var keyMap = map[string]string{
	"UP": "Up", "DOWN": "Down", "LEFT": "Left", "RIGHT": "Right",
	"ENTER": "Select", "OK": "Select", "RETURN": "Back", "BACK": "Back",
	"HOME": "Home", "PLAY": "Play", "PLAY_PAUSE": "Play", "INFO": "Info",
}

func capabilities() []core.Capability {
	return []core.Capability{core.CapabilityStatus, core.CapabilityPower, core.CapabilityVolume, core.CapabilityMute, core.CapabilityPlayback, core.CapabilityNavigation, core.CapabilitySource, core.CapabilityChannel}
}

func containsUnsupported(values []core.Capability) bool {
	return len(values) == 1 && values[0] == core.CapabilityUnsupported
}

func wakeOnLAN(rawMAC string) error {
	mac, err := net.ParseMAC(rawMAC)
	if err != nil || len(mac) != 6 {
		return fmt.Errorf("invalid Roku MAC address %q", rawMAC)
	}
	packet := make([]byte, 102)
	for index := 0; index < 6; index++ {
		packet[index] = 0xff
	}
	for repeat := 0; repeat < 16; repeat++ {
		copy(packet[6+repeat*6:], mac)
	}
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4bcast, Port: 9})
	if err != nil {
		return fmt.Errorf("send Roku Wake-on-LAN packet: %w", err)
	}
	defer conn.Close()
	if _, err := conn.Write(packet); err != nil {
		return fmt.Errorf("send Roku Wake-on-LAN packet: %w", err)
	}
	return nil
}

type Factory struct{}

func (Factory) Supports(info core.DiscoveredDevice) bool {
	text := strings.ToLower(info.Metadata.Manufacturer + " " + info.Metadata.Model + " " + info.Metadata.Name)
	return info.Metadata.Kind == core.DeviceKindTV && (strings.EqualFold(info.Metadata.Manufacturer, "Roku") || strings.Contains(text, "roku"))
}

func (f Factory) Create(_ context.Context, info core.DiscoveredDevice) (core.Device, error) {
	if !f.Supports(info) {
		return nil, errors.New("not a Roku device")
	}
	info.Metadata.Capabilities = capabilities()
	return New(info), nil
}
