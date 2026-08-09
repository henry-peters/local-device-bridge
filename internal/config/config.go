package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Config struct {
	Server    ServerConfig    `json:"server"`
	Discovery DiscoveryConfig `json:"discovery"`
	Telegram  TelegramConfig  `json:"telegram"`
	CLI       CLIConfig       `json:"cli"`
	State     StateConfig     `json:"state"`
}

type ServerConfig struct {
	Bind     string `json:"bind"`
	AllowLAN bool   `json:"allow_lan"`
	// InsecureLANHTTP exposes a token-protected HTTP endpoint for a trusted home
	// Wi-Fi. It is the beginner-friendly default so phones do not need to trust
	// a private certificate. Set it false to require HTTPS instead.
	InsecureLANHTTP bool   `json:"insecure_lan_http"`
	TLSCert         string `json:"tls_cert,omitempty"`
	TLSKey          string `json:"tls_key,omitempty"`
	DashboardOrigin string `json:"dashboard_origin,omitempty"`
}

type DiscoveryConfig struct {
	Interfaces          []string      `json:"interfaces,omitempty"`
	Timeout             time.Duration `json:"timeout"`
	ScanInterval        time.Duration `json:"scan_interval"`
	ShowDisplayDevices  bool          `json:"show_display_devices"`
	ShowConsoleDevices  bool          `json:"show_console_devices"`
	ShowComputerDevices bool          `json:"show_computer_devices"`
	ShowOfflineDevices  bool          `json:"show_offline_devices"`
}

type TelegramConfig struct {
	Enabled     bool     `json:"enabled"`
	TokenEnv    string   `json:"token_env"`
	AllowedIDs  []string `json:"allowed_ids,omitempty"`
	AllowPublic bool     `json:"allow_public"`
}

// CLIConfig stores the operator's preferred way to start and use the bridge.
// It is presentation policy only; the daemon/API remain available so service
// managers and other clients can use the same command engine.
type CLIConfig struct {
	DashboardEnabled    bool `json:"dashboard_enabled"`
	AutoLaunchDashboard bool `json:"auto_launch_dashboard"`
	ShowDashboardURL    bool `json:"show_dashboard_url"`
}

type StateConfig struct {
	Directory string `json:"directory"`
	Database  string `json:"database"`
}

func Default() Config {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	dir = filepath.Join(dir, "local-device-bridge")
	return Config{
		Server:    ServerConfig{Bind: "127.0.0.1:8787", InsecureLANHTTP: true},
		Discovery: DiscoveryConfig{Timeout: 5 * time.Second, ScanInterval: 60 * time.Second, ShowDisplayDevices: true, ShowConsoleDevices: false, ShowComputerDevices: true, ShowOfflineDevices: true},
		Telegram:  TelegramConfig{TokenEnv: "TELEGRAM_BOT_TOKEN"},
		CLI:       CLIConfig{DashboardEnabled: true, AutoLaunchDashboard: true, ShowDashboardURL: true},
		State:     StateConfig{Directory: dir, Database: filepath.Join(dir, "bridge.db")},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Server.Bind == "" {
		cfg.Server.Bind = "127.0.0.1:8787"
	}
	if cfg.Discovery.Timeout <= 0 {
		cfg.Discovery.Timeout = 5 * time.Second
	}
	if cfg.Discovery.ScanInterval <= 0 {
		cfg.Discovery.ScanInterval = 60 * time.Second
	}
	if cfg.Telegram.TokenEnv == "" {
		cfg.Telegram.TokenEnv = "TELEGRAM_BOT_TOKEN"
	}
	if cfg.State.Directory == "" {
		cfg.State.Directory = filepath.Dir(cfg.State.Database)
	}
	if cfg.State.Database == "" {
		cfg.State.Database = filepath.Join(cfg.State.Directory, "bridge.db")
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
