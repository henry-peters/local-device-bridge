package service

import (
	"strings"
	"testing"
)

func TestLaunchdPlistUsesStableDaemonArgumentsAndKeepAlive(t *testing.T) {
	plist := launchdPlist("/Users/example/.local/bin/local-device-bridge", "/Users/example/Library/Application Support/local-device-bridge/config.json", "/Users/example/Library/Logs/bridge.log", "/Users/example/Library/Logs/bridge.err")
	for _, want := range []string{
		"<key>Label</key><string>com.local-device-bridge</string>",
		"<string>--config</string>",
		"<string>/Users/example/Library/Application Support/local-device-bridge/config.json</string>",
		"<key>RunAtLoad</key><true/>",
		"<key>SuccessfulExit</key><false/>",
		"<key>NetworkState</key><true/>",
		"<key>StandardErrorPath</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("launchd plist does not contain %q:\n%s", want, plist)
		}
	}
	if strings.Contains(plist, "sudoers") || strings.Contains(plist, "authorized_keys") {
		t.Fatal("launchd plist must not contain privileged pairing commands")
	}
}

func TestSystemdArgEscapesArguments(t *testing.T) {
	if got, want := systemdArg("/tmp/a path/config.json"), `/tmp/a\x20path/config.json`; got != want {
		t.Fatalf("systemdArg = %q, want %q", got, want)
	}
}
