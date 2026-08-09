package store

import (
	"context"
	"testing"

	"github.com/local-device-bridge/local-device-bridge/internal/core"
)

func TestAuditRoundTrip(t *testing.T) {
	database := t.TempDir() + "/bridge.db"
	state, err := Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	command := core.Command{DeviceID: "samsung-living-room", Action: core.ActionVolumeUp, Principal: "dashboard", Source: "http"}
	if err := state.Audit(context.Background(), command, true, "Sent KEY_VOLUP"); err != nil {
		t.Fatal(err)
	}
	events, err := state.ListAudit(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != string(core.ActionVolumeUp) || !events[0].Success || events[0].Message != "Sent KEY_VOLUP" {
		t.Fatalf("unexpected audit events: %+v", events)
	}
}
