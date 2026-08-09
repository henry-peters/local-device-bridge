package core

import (
	"context"
	"strings"
	"time"
)

type DeviceID string

type DeviceKind string

const (
	DeviceKindTV       DeviceKind = "tv"
	DeviceKindMonitor  DeviceKind = "monitor"
	DeviceKindComputer DeviceKind = "computer"
	DeviceKindMobile   DeviceKind = "mobile"
	DeviceKindConsole  DeviceKind = "console"
	DeviceKindUnknown  DeviceKind = "unknown"
)

// DeviceCategory is the canonical inventory section used by every transport.
// Discovery providers may only know a partial identity; NormalizeMetadata
// derives this value once so the dashboard, CLI, Telegram, and API never sort
// the same device differently.
type DeviceCategory string

const (
	CategoryTVDisplay DeviceCategory = "tv_display"
	CategoryComputer  DeviceCategory = "computer"
	CategoryMobile    DeviceCategory = "mobile"
	CategoryConsole   DeviceCategory = "console"
	CategoryOther     DeviceCategory = "other"
)

type Capability string

const (
	CapabilityPower       Capability = "power"
	CapabilityStatus      Capability = "status"
	CapabilityVolume      Capability = "volume"
	CapabilityMute        Capability = "mute"
	CapabilityPlayback    Capability = "playback"
	CapabilityNavigation  Capability = "navigation"
	CapabilitySource      Capability = "source"
	CapabilityChannel     Capability = "channel"
	CapabilityWakeOnLAN   Capability = "wake_on_lan"
	CapabilityUnsupported Capability = "unsupported"
)

type DeviceMetadata struct {
	ID           DeviceID       `json:"id"`
	Kind         DeviceKind     `json:"kind"`
	Category     DeviceCategory `json:"category"`
	Platform     string         `json:"platform,omitempty"`
	Manufacturer string         `json:"manufacturer,omitempty"`
	Model        string         `json:"model,omitempty"`
	Name         string         `json:"name"`
	// Alias is the user-defined friendly name. Name remains the discovery
	// name, while Alias is stable across scans and can be used by agents and
	// CLI/Telegram commands instead of a provider-specific ID.
	Alias      string `json:"alias,omitempty"`
	Discovery  string `json:"discovery,omitempty"`
	IP         string `json:"ip,omitempty"`
	MAC        string `json:"mac,omitempty"`
	DUID       string `json:"duid,omitempty"`
	RemoteUser string `json:"remote_user,omitempty"`
	Firmware   string `json:"firmware,omitempty"`
	Paired     bool   `json:"paired"`
	// ControlVerified is proof that discovery reached the device's real local
	// control endpoint. Names from mDNS/SSDP or ARP alone are not enough to
	// offer a pairing flow.
	ControlVerified bool         `json:"control_verified"`
	Online          bool         `json:"online"`
	Capabilities    []Capability `json:"capabilities"`
	LastSeen        time.Time    `json:"last_seen,omitempty"`
	Error           string       `json:"error,omitempty"`
}

// DisplayName returns the operator-facing name without discarding the
// original discovery name stored in Name.
func DisplayName(md DeviceMetadata) string {
	if alias := strings.TrimSpace(md.Alias); alias != "" {
		return alias
	}
	if name := strings.TrimSpace(md.Name); name != "" {
		return name
	}
	return string(md.ID)
}

type DiscoveredDevice struct {
	Metadata DeviceMetadata `json:"metadata"`
	Source   string         `json:"source"`
}

type DeviceState struct {
	DeviceID DeviceID  `json:"device_id"`
	Online   bool      `json:"online"`
	Power    string    `json:"power,omitempty"`
	Volume   *int      `json:"volume,omitempty"`
	Muted    *bool     `json:"muted,omitempty"`
	Source   string    `json:"source,omitempty"`
	Updated  time.Time `json:"updated"`
	Error    string    `json:"error,omitempty"`
}

type Action string

const (
	ActionStatus     Action = "status"
	ActionPowerOn    Action = "power_on"
	ActionPowerOff   Action = "power_off"
	ActionVolumeUp   Action = "volume_up"
	ActionVolumeDown Action = "volume_down"
	ActionVolumeSet  Action = "volume_set"
	ActionMute       Action = "mute"
	ActionKey        Action = "key"
	ActionSource     Action = "source"
	ActionChannel    Action = "channel"
)

type Command struct {
	DeviceID  DeviceID          `json:"device_id"`
	Action    Action            `json:"action"`
	Arguments map[string]string `json:"arguments,omitempty"`
	Principal string            `json:"principal"`
	Source    string            `json:"source"`
}

type CommandResult struct {
	Message string       `json:"message"`
	State   *DeviceState `json:"state,omitempty"`
}

type AuditEvent struct {
	ID        int64  `json:"id"`
	DeviceID  string `json:"device_id"`
	Action    string `json:"action"`
	Principal string `json:"principal"`
	Source    string `json:"source"`
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

type AuditReader interface {
	ListAudit(context.Context, int) ([]AuditEvent, error)
}

type Principal struct {
	Name  string
	Admin bool
}

type Device interface {
	Metadata() DeviceMetadata
	Capabilities() []Capability
	Pair(context.Context) error
	State(context.Context) (DeviceState, error)
	Execute(context.Context, Command) (CommandResult, error)
}

type PairOptions struct {
	Username string `json:"username,omitempty"`
}

type InventoryVisibility struct {
	ShowDisplayDevices  bool `json:"show_display_devices"`
	ShowConsoleDevices  bool `json:"show_console_devices"`
	ShowComputerDevices bool `json:"show_computer_devices"`
	ShowOfflineDevices  bool `json:"show_offline_devices"`
}

type Pairer interface {
	PairWith(context.Context, PairOptions) error
}

type Unpairer interface {
	Unpair(context.Context) error
}

type DiscoveryProvider interface {
	Name() string
	Discover(context.Context) ([]DiscoveredDevice, error)
}

type DeviceFactory interface {
	Supports(DiscoveredDevice) bool
	Create(context.Context, DiscoveredDevice) (Device, error)
}

type StateStore interface {
	LoadDevices(context.Context) ([]DeviceMetadata, error)
	SaveDevice(context.Context, DeviceMetadata) error
	Audit(context.Context, Command, bool, string) error
	Close() error
}
