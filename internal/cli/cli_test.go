package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/local-device-bridge/local-device-bridge/internal/config"
)

func TestParsePairOptions(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want map[string]string
	}{
		{name: "empty", want: nil},
		{name: "Mac username", args: []string{"remote-user"}, want: map[string]string{"username": "remote-user"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parsePairOptions(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("options = %#v, want %#v", got, test.want)
			}
			for key, want := range test.want {
				if got[key] != want {
					t.Errorf("options[%q] = %q, want %q", key, got[key], want)
				}
			}
		})
	}
}

func TestParsePairOptionsRejectsInvalidValues(t *testing.T) {
	for _, args := range [][]string{{"--ip"}, {"--unknown"}, {"one", "two"}} {
		if _, err := parsePairOptions(args); err == nil {
			t.Errorf("parsePairOptions(%q) succeeded, want error", args)
		}
	}
}

func TestReadRemoteKey(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "up arrow", input: "\x1b[A", want: "UP"},
		{name: "down arrow", input: "\x1b[B", want: "DOWN"},
		{name: "right arrow", input: "\x1b[C", want: "RIGHT"},
		{name: "left arrow", input: "\x1b[D", want: "LEFT"},
		{name: "volume up", input: "+", want: "VOLUME_UP"},
		{name: "play", input: " ", want: "PLAY"},
		{name: "quit", input: "q", want: "quit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := readRemoteKey(bytes.NewBufferString(test.input))
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("readRemoteKey(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestBannerRulesFillRequestedWidth(t *testing.T) {
	const width = 117
	var output bytes.Buffer
	printBannerWidth(&output, width)
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) < 2 || len(lines[0]) != width || len(lines[len(lines)-1]) != width {
		t.Fatalf("banner rules do not fill %d columns: first=%d last=%d", width, len(lines[0]), len(lines[len(lines)-1]))
	}
	if !strings.Contains(output.String(), "CLI  //  SETUP") {
		t.Fatal("banner does not show the CLI label")
	}
}

func TestDashboardEndpointUsesLoopbackForWildcardBind(t *testing.T) {
	cfg := config.Default()
	cfg.Server.Bind = "0.0.0.0:8787"
	if got, want := dashboardEndpoint(cfg), "127.0.0.1:8787"; got != want {
		t.Fatalf("dashboardEndpoint = %q, want %q", got, want)
	}
}

func TestDashboardURLUsesConfiguredPort(t *testing.T) {
	cfg := config.Default()
	cfg.Server.Bind = "127.0.0.1:9191"
	if got, want := dashboardURL(cfg), "http://127.0.0.1:9191"; got != want {
		t.Fatalf("dashboardURL = %q, want %q", got, want)
	}
	cfg.Server.AllowLAN = true
	if got, want := dashboardURL(cfg), "http://127.0.0.1:9192"; got != want {
		t.Fatalf("LAN dashboardURL = %q, want %q", got, want)
	}
	if got := dashboardLANHTTPURL(cfg); !strings.HasSuffix(got, ":9191") || !strings.HasPrefix(got, "http://") {
		t.Fatalf("dashboardLANHTTPURL = %q, want an HTTP URL on port 9191", got)
	}
}
