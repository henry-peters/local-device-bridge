package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/local-device-bridge/local-device-bridge/internal/api"
	"github.com/local-device-bridge/local-device-bridge/internal/cli"
	"github.com/local-device-bridge/local-device-bridge/internal/config"
	"github.com/local-device-bridge/local-device-bridge/internal/console"
	"github.com/local-device-bridge/local-device-bridge/internal/core"
	"github.com/local-device-bridge/local-device-bridge/internal/daemonlock"
	"github.com/local-device-bridge/local-device-bridge/internal/discovery"
	"github.com/local-device-bridge/local-device-bridge/internal/macos"
	"github.com/local-device-bridge/local-device-bridge/internal/roku"
	"github.com/local-device-bridge/local-device-bridge/internal/samsung"
	"github.com/local-device-bridge/local-device-bridge/internal/security"
	"github.com/local-device-bridge/local-device-bridge/internal/store"
	"github.com/local-device-bridge/local-device-bridge/internal/telegram"
)

const version = api.Version

func main() {
	configPath, daemon := daemonInvocation(os.Args[1:])
	if !daemon {
		if err := cli.Run(os.Args[1:]); err != nil {
			slog.Error("command failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := runDaemon(configPath); err != nil && !errors.Is(err, context.Canceled) {
		// runDaemon intentionally silences noisy third-party discovery logs;
		// report fatal startup errors directly so lock, port, and config failures
		// are never swallowed by the discovery logger setup.
		fmt.Fprintf(os.Stderr, "local-device-bridge: daemon failed: %v\n", err)
		os.Exit(1)
	}
}

func daemonInvocation(args []string) (string, bool) {
	if len(args) >= 1 && args[0] == "daemon" {
		return cli.ConfigPath(), true
	}
	if len(args) >= 3 && args[0] == "--config" && args[2] == "daemon" {
		return args[1], true
	}
	return "", false
}

func runDaemon(configPath string) error {
	// The mDNS library uses the standard logger for internal lifecycle messages;
	// keep those implementation details out of the user-facing daemon console.
	log.SetOutput(io.Discard)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.State.Directory, 0o700); err != nil {
		return err
	}
	daemonLock, err := daemonlock.Acquire(filepath.Join(cfg.State.Directory, "daemon.lock"))
	if err != nil {
		return err
	}
	defer daemonLock.Close()
	state, err := store.Open(cfg.State.Database)
	if err != nil {
		return err
	}
	defer state.Close()
	secrets := security.NewSecretStore("local-device-bridge", cfg.State.Directory)
	mdnsTimeout := cfg.Discovery.Timeout
	if mdnsTimeout <= 0 || mdnsTimeout > 4*time.Second {
		mdnsTimeout = 4 * time.Second
	}
	providers := []core.DiscoveryProvider{
		&discovery.LocalHostProvider{},
		&discovery.SSDPProvider{Timeout: cfg.Discovery.Timeout, Interfaces: cfg.Discovery.Interfaces},
		&discovery.MDNSProvider{Timeout: mdnsTimeout, Interfaces: cfg.Discovery.Interfaces},
		&discovery.ARPProvider{Interfaces: cfg.Discovery.Interfaces},
	}
	manager, err := core.NewManager(state, providers, []core.DeviceFactory{samsung.Factory{Secrets: secrets}, roku.Factory{}, macos.Factory{}, console.Factory{}})
	if err != nil {
		return err
	}
	manager.SetInventoryVisibility(core.InventoryVisibility{
		ShowDisplayDevices:  cfg.Discovery.ShowDisplayDevices,
		ShowConsoleDevices:  cfg.Discovery.ShowConsoleDevices,
		ShowComputerDevices: cfg.Discovery.ShowComputerDevices,
		ShowOfflineDevices:  cfg.Discovery.ShowOfflineDevices,
	})
	logger := slog.Default()
	server, err := api.NewServer(manager, cfg, secrets, logger)
	if err != nil {
		return err
	}
	server.SetConfigPath(configPath)
	logger.Info("local-device-bridge starting", "version", version, "bind", cfg.Server.Bind, "lan", cfg.Server.AllowLAN)
	if cfg.Server.AllowLAN {
		localBind := api.LocalDashboardBind(cfg.Server.Bind)
		go func() {
			if err := server.ListenAndServeLocal(ctx, localBind); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("local dashboard companion stopped", "bind", localBind, "error", err)
			}
		}()
	}
	go func() {
		if _, err := manager.Scan(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("initial network scan failed", "error", err)
		}
	}()
	go runDiscoveryLoop(ctx, manager, cfg.Discovery.ScanInterval, cfg.Discovery.Timeout, logger)
	if cfg.Telegram.Enabled {
		token := os.Getenv(cfg.Telegram.TokenEnv)
		if token == "" {
			token, _ = secrets.Get("telegram_bot_token")
		}
		bot := telegram.New(token, cfg.Telegram.AllowedIDs, manager, nil)
		bot.SetAllowPublic(cfg.Telegram.AllowPublic)
		go func() {
			if err := bot.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("telegram stopped", "error", err)
			}
		}()
	}
	return serveWithRecovery(ctx, server, logger)
}

func serveWithRecovery(ctx context.Context, server *api.Server, logger *slog.Logger) error {
	// Keep the foreground daemon alive if the HTTP listener is interrupted by
	// a transient OS/network error. launchd/systemd/Task Scheduler still
	// supervise a full process crash, while this loop recovers the local API
	// without losing the loaded manager or embedded dashboard.
	const restartDelay = 2 * time.Second
	for {
		err := server.ListenAndServe(ctx)
		if err == nil || ctx.Err() != nil {
			return err
		}
		logger.Error("daemon listener stopped; restarting", "error", err, "retry_in", restartDelay)
		timer := time.NewTimer(restartDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		}
	}
}

func runDiscoveryLoop(ctx context.Context, manager *core.Manager, interval, providerTimeout time.Duration, logger *slog.Logger) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scanTimeout := 20 * time.Second
			if providerTimeout > 0 && providerTimeout+2*time.Second > scanTimeout {
				scanTimeout = providerTimeout + 2*time.Second
			}
			scanCtx, cancel := context.WithTimeout(ctx, scanTimeout)
			found, err := manager.Scan(scanCtx)
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("background network scan failed", "error", err)
				continue
			}
			logger.Debug("background network scan complete", "devices", len(found))
		}
	}
}
