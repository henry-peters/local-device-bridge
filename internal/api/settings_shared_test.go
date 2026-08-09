package api

import (
	"testing"

	"github.com/local-device-bridge/local-device-bridge/internal/core"
)

func TestCompanionListenersShareSettingsState(t *testing.T) {
	shared := &sharedSettings{value: core.InventoryVisibility{ShowComputerDevices: true}}
	phone := &Server{settings: shared}
	host := &Server{settings: shared}
	phone.settings.mu.Lock()
	phone.settings.value.ShowComputerDevices = false
	phone.settings.mu.Unlock()
	if host.currentSettings().ShowComputerDevices {
		t.Fatal("host listener retained a stale settings snapshot")
	}
}
