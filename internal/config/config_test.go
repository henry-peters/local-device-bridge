package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := Default()
	cfg.Server.Bind = "0.0.0.0:9000"
	cfg.Server.AllowLAN = true
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Server.Bind != cfg.Server.Bind || !loaded.Server.AllowLAN {
		t.Fatalf("loaded config = %#v", loaded)
	}
	if !loaded.Discovery.ShowDisplayDevices || !loaded.Discovery.ShowConsoleDevices || !loaded.Discovery.ShowComputerDevices {
		t.Fatal("inventory defaults were not preserved")
	}
	if !loaded.CLI.DashboardEnabled || !loaded.CLI.AutoLaunchDashboard || !loaded.CLI.ShowDashboardURL {
		t.Fatalf("CLI defaults were not preserved: %#v", loaded.CLI)
	}
	if loaded.Discovery.ScanInterval != 60*time.Second {
		t.Fatalf("scan interval = %s, want 1m", loaded.Discovery.ScanInterval)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions = %o", info.Mode().Perm())
	}
}

func TestLoadNormalizesLoopbackLANBind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"server":{"bind":"127.0.0.1:8787","allow_lan":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Bind != "0.0.0.0:8787" {
		t.Fatalf("LAN bind = %q, want 0.0.0.0:8787", cfg.Server.Bind)
	}
}
