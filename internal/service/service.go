// Package service installs the bridge as a per-user operating-system service.
// A user service keeps the dashboard and command API available after login and
// restarts the daemon if it exits unexpectedly.
package service

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const label = "com.local-device-bridge"

// Install registers and starts the current executable for the current user.
// It intentionally uses per-user services so the bridge can read the user's
// keychain and never needs administrator privileges.
func Install(executable, configPath string) error {
	if strings.TrimSpace(executable) == "" {
		return errors.New("service install needs the bridge executable path")
	}
	if strings.TrimSpace(configPath) == "" {
		return errors.New("service install needs the bridge configuration path")
	}
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(executable, configPath)
	case "linux":
		return installSystemd(executable, configPath)
	case "windows":
		return installTask(executable, configPath)
	default:
		return fmt.Errorf("automatic service installation is not supported on %s", runtime.GOOS)
	}
}

// Uninstall removes the current user's bridge service. It does not delete
// configuration, pairings, credentials, or the installed binary.
func Uninstall() error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallLaunchd()
	case "linux":
		return uninstallSystemd()
	case "windows":
		return uninstallTask()
	default:
		return fmt.Errorf("automatic service removal is not supported on %s", runtime.GOOS)
	}
}

func installLaunchd(executable, configPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	uid, err := commandOutput("id", "-u")
	if err != nil {
		return fmt.Errorf("find macOS user id: %w", err)
	}
	target := "gui/" + strings.TrimSpace(uid)
	agentDir := filepath.Join(home, "Library", "LaunchAgents")
	logDir := filepath.Join(home, "Library", "Logs")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		return fmt.Errorf("create launch agent directory: %w", err)
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return fmt.Errorf("create bridge log directory: %w", err)
	}
	plistPath := filepath.Join(agentDir, label+".plist")
	plist := launchdPlist(executable, configPath, filepath.Join(logDir, "local-device-bridge.log"), filepath.Join(logDir, "local-device-bridge.err"))
	if err := os.WriteFile(plistPath, []byte(plist), 0o600); err != nil {
		return fmt.Errorf("write launch agent: %w", err)
	}
	// Replacing an existing agent is safe and avoids a second process when the
	// operator reruns setup or upgrades the binary.
	_ = run("launchctl", "bootout", target+"/"+label)
	if err := run("launchctl", "bootstrap", target, plistPath); err != nil {
		return fmt.Errorf("load launch agent: %w", err)
	}
	if err := run("launchctl", "kickstart", "-k", target+"/"+label); err != nil {
		return fmt.Errorf("start launch agent: %w", err)
	}
	return nil
}

func uninstallLaunchd() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	uid, err := commandOutput("id", "-u")
	if err != nil {
		return fmt.Errorf("find macOS user id: %w", err)
	}
	target := "gui/" + strings.TrimSpace(uid)
	plistPath := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	_ = run("launchctl", "bootout", target+"/"+label)
	if err := os.Remove(plistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove launch agent: %w", err)
	}
	return nil
}

func installSystemd(executable, configPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o700); err != nil {
		return fmt.Errorf("create systemd user directory: %w", err)
	}
	unitPath := filepath.Join(unitDir, "local-device-bridge.service")
	unit := fmt.Sprintf(`[Unit]
Description=local-device-bridge LAN device controller
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s --config %s daemon
Restart=on-failure
RestartSec=5
Environment=HOME=%s
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=default.target
`, systemdArg(executable), systemdArg(configPath), systemdArg(home))
	if err := os.WriteFile(unitPath, []byte(unit), 0o600); err != nil {
		return fmt.Errorf("write systemd user service: %w", err)
	}
	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd user services: %w", err)
	}
	if err := run("systemctl", "--user", "enable", "--now", "local-device-bridge.service"); err != nil {
		return fmt.Errorf("enable systemd user service: %w", err)
	}
	return nil
}

func uninstallSystemd() error {
	_ = run("systemctl", "--user", "disable", "--now", "local-device-bridge.service")
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	unitPath := filepath.Join(home, ".config", "systemd", "user", "local-device-bridge.service")
	if err := os.Remove(unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove systemd user service: %w", err)
	}
	_ = run("systemctl", "--user", "daemon-reload")
	return nil
}

func installTask(executable, configPath string) error {
	command := fmt.Sprintf(`"%s" --config "%s" daemon`, executable, configPath)
	if err := run("schtasks", "/Create", "/TN", "local-device-bridge", "/TR", command, "/SC", "ONLOGON", "/RL", "LIMITED", "/F"); err != nil {
		return fmt.Errorf("register Windows logon task: %w", err)
	}
	return run("schtasks", "/Run", "/TN", "local-device-bridge")
}

func uninstallTask() error {
	_ = run("schtasks", "/End", "/TN", "local-device-bridge")
	if err := run("schtasks", "/Delete", "/TN", "local-device-bridge", "/F"); err != nil {
		return fmt.Errorf("remove Windows logon task: %w", err)
	}
	return nil
}

func launchdPlist(executable, configPath, stdoutPath, stderrPath string) string {
	values := []string{executable, "--config", configPath, "daemon", stdoutPath, stderrPath}
	var args strings.Builder
	for _, value := range values[:4] {
		args.WriteString("    <string>")
		_ = xml.EscapeText(&args, []byte(value))
		args.WriteString("</string>\n")
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>` + label + `</string>
  <key>ProgramArguments</key><array>
` + args.String() + `  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict>
    <key>SuccessfulExit</key><false/>
    <key>NetworkState</key><true/>
  </dict>
  <key>ThrottleInterval</key><integer>5</integer>
  <key>ProcessType</key><string>Interactive</string>
  <key>StandardOutPath</key><string>` + xmlText(stdoutPath) + `</string>
  <key>StandardErrorPath</key><string>` + xmlText(stderrPath) + `</string>
</dict></plist>
`
}

func xmlText(value string) string {
	var escaped strings.Builder
	_ = xml.EscapeText(&escaped, []byte(value))
	return escaped.String()
}

func systemdArg(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, " ", `\x20`)
	value = strings.ReplaceAll(value, "\t", `\x09`)
	return value
}

func commandOutput(name string, args ...string) (string, error) {
	command := exec.Command(name, args...)
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func run(name string, args ...string) error {
	command := exec.Command(name, args...)
	if output, err := command.CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, message)
	}
	return nil
}

// ParseUID is kept small and testable for callers that need to display service
// diagnostics without exposing command output or credentials.
func ParseUID(value string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(value))
}
