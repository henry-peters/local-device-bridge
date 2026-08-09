package discovery

import "testing"

func TestHostIdentity(t *testing.T) {
	for goos, want := range map[string][2]string{
		"darwin":  {"Apple", "macOS"},
		"linux":   {"Linux", "Linux"},
		"windows": {"Microsoft", "Windows"},
	} {
		manufacturer, model := hostIdentity(goos)
		if manufacturer != want[0] || model != want[1] {
			t.Errorf("hostIdentity(%q) = %q %q, want %q %q", goos, manufacturer, model, want[0], want[1])
		}
	}
}
