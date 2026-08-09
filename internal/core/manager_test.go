package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type testStore struct {
	devices []DeviceMetadata
	audits  int
}

func (s *testStore) LoadDevices(context.Context) ([]DeviceMetadata, error) { return s.devices, nil }
func (s *testStore) SaveDevice(_ context.Context, md DeviceMetadata) error {
	s.devices = append(s.devices, md)
	return nil
}
func (s *testStore) Audit(context.Context, Command, bool, string) error { s.audits++; return nil }
func (s *testStore) Close() error                                       { return nil }

type testDevice struct {
	md           DeviceMetadata
	paired       bool
	pairFailures int
	executed     []Command
}

func (d *testDevice) Metadata() DeviceMetadata   { return d.md }
func (d *testDevice) Capabilities() []Capability { return d.md.Capabilities }
func (d *testDevice) Pair(context.Context) error {
	if d.pairFailures > 0 {
		d.pairFailures--
		return errors.New("dial tcp 192.0.2.10:8001: connect: no route to host")
	}
	d.paired = true
	return nil
}
func (d *testDevice) Unpair(context.Context) error {
	d.paired = false
	d.md.Paired = false
	return nil
}
func (d *testDevice) State(context.Context) (DeviceState, error) {
	return DeviceState{DeviceID: d.md.ID, Online: true}, nil
}
func (d *testDevice) Execute(_ context.Context, cmd Command) (CommandResult, error) {
	d.executed = append(d.executed, cmd)
	return CommandResult{Message: "ok"}, nil
}

type testProvider struct{ item DiscoveredDevice }

func (p testProvider) Name() string { return "test" }
func (p testProvider) Discover(context.Context) ([]DiscoveredDevice, error) {
	return []DiscoveredDevice{p.item}, nil
}

type testFactory struct{ device *testDevice }

func (f testFactory) Supports(DiscoveredDevice) bool                           { return true }
func (f testFactory) Create(context.Context, DiscoveredDevice) (Device, error) { return f.device, nil }

func TestManagerScanPairAndExecute(t *testing.T) {
	store := &testStore{}
	device := &testDevice{md: DeviceMetadata{ID: "tv-1", Kind: DeviceKindTV, Name: "Living Room"}}
	m, err := NewManager(store, []DiscoveryProvider{testProvider{item: DiscoveredDevice{Metadata: device.md}}}, []DeviceFactory{testFactory{device: device}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(m.List()) != 1 {
		t.Fatalf("devices = %d", len(m.List()))
	}
	if err := m.Pair(context.Background(), "tv-1"); err != nil {
		t.Fatal(err)
	}
	if !m.List()[0].Paired {
		t.Fatal("device was not marked paired")
	}
	if err := m.Unpair(context.Background(), "tv-1"); err != nil {
		t.Fatal(err)
	}
	if m.List()[0].Paired {
		t.Fatal("device remained paired after unpair")
	}
	if _, err := m.Execute(context.Background(), Command{DeviceID: "tv-1", Action: ActionPowerOff, Principal: "test", Source: "test"}); err != nil {
		t.Fatal(err)
	}
	if len(device.executed) != 1 || store.audits != 1 {
		t.Fatalf("executed=%d audits=%d", len(device.executed), store.audits)
	}
}

func TestManagerPairRefreshesAStaleNetworkAddress(t *testing.T) {
	store := &testStore{}
	device := &testDevice{md: DeviceMetadata{ID: "tv-1", Kind: DeviceKindTV, Manufacturer: "Samsung", ControlVerified: true, Name: "Living Room", IP: "192.0.2.10"}, pairFailures: 1}
	m, err := NewManager(store, []DiscoveryProvider{testProvider{item: DiscoveredDevice{Metadata: device.md}}}, []DeviceFactory{testFactory{device: device}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Pair(context.Background(), "tv-1"); err != nil {
		t.Fatalf("pair did not retry after rediscovery: %v", err)
	}
	if !m.List()[0].Paired {
		t.Fatal("TV was not paired after address refresh")
	}
}

func TestManagerRejectsPairingForUnverifiedSamsungDiscovery(t *testing.T) {
	store := &testStore{}
	m, err := NewManager(store, []DiscoveryProvider{testProvider{item: DiscoveredDevice{Metadata: DeviceMetadata{
		ID: "samsung-unverified", Kind: DeviceKindTV, Manufacturer: "Samsung", Name: "Living Room TV", IP: "192.0.2.10",
	}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	err = m.Pair(context.Background(), "samsung-unverified")
	if err == nil || !strings.Contains(err.Error(), "local control was not verified") {
		t.Fatalf("pairing error = %v", err)
	}
}

func TestManagerPairingSurvivesAutomaticRescan(t *testing.T) {
	store := &testStore{}
	device := &testDevice{md: DeviceMetadata{ID: "tv-1", Kind: DeviceKindTV, Manufacturer: "Samsung", ControlVerified: true, Name: "Living Room TV", IP: "192.0.2.10", MAC: "AA:BB:CC:DD:EE:FF"}}
	m, err := NewManager(store, []DiscoveryProvider{testProvider{item: DiscoveredDevice{Source: "mdns", Metadata: device.md}}}, []DeviceFactory{testFactory{device: device}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Pair(context.Background(), "tv-1"); err != nil {
		t.Fatal(err)
	}
	// The provider deliberately reports the normal unpaired discovery
	// metadata on the next pass. The manager must carry the saved pairing
	// across that refresh instead of relocking the controls.
	if _, err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if items := m.List(); len(items) != 1 || !items[0].Paired {
		t.Fatalf("pairing was lost after automatic rescan: %+v", items)
	}
}

func TestManagerListDeduplicatesDiscoveryRecordsByIP(t *testing.T) {
	store := &testStore{devices: []DeviceMetadata{
		{ID: "lan-1", Kind: DeviceKindUnknown, Name: "LAN device", IP: "192.0.2.20"},
		{ID: "tv-1", Kind: DeviceKindTV, Manufacturer: "Samsung", Name: "Living Room", IP: "192.0.2.20"},
		{ID: "computer-1", Kind: DeviceKindComputer, Name: "Mac mini", IP: "192.0.2.30"},
	}}
	m, err := NewManager(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := m.List()
	if len(items) != 2 {
		t.Fatalf("deduplicated devices = %d, want 2", len(items))
	}
	if items[0].Manufacturer != "Samsung" && items[1].Manufacturer != "Samsung" {
		t.Fatal("Samsung metadata did not win duplicate IP selection")
	}
}

func TestManagerReconcilesGenericTVDiscoveryWithSavedSamsungIdentity(t *testing.T) {
	store := &testStore{devices: []DeviceMetadata{{ID: "samsung-living-room", Kind: DeviceKindTV, Manufacturer: "Samsung", Model: "Test TV", Name: "Living Room TV", IP: "192.0.2.29", Capabilities: []Capability{CapabilityStatus}}}}
	device := &testDevice{md: store.devices[0]}
	m, err := NewManager(store, []DiscoveryProvider{testProvider{item: DiscoveredDevice{Source: "mdns", Metadata: DeviceMetadata{ID: "mdns-living-room-tv", Kind: DeviceKindTV, Name: "Living Room TV", IP: "192.0.2.29", Capabilities: []Capability{CapabilityUnsupported}}}}}, []DeviceFactory{testFactory{device: device}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	items := m.List()
	if len(items) != 1 || items[0].ID != "samsung-living-room" || items[0].Manufacturer != "Samsung" || !items[0].Online {
		t.Fatalf("reconciled inventory = %+v", items)
	}
}

func TestManagerReconcilesSamsungIdentityAfterAddressChange(t *testing.T) {
	oldSeen := time.Now().Add(-time.Minute)
	store := &testStore{devices: []DeviceMetadata{{ID: "samsung-living-room", Kind: DeviceKindTV, Manufacturer: "Samsung", Model: "Test TV", Name: "Living Room TV", IP: "192.0.2.29", LastSeen: oldSeen, Capabilities: []Capability{CapabilityStatus}}}}
	m, err := NewManager(store, []DiscoveryProvider{testProvider{item: DiscoveredDevice{Source: "mdns", Metadata: DeviceMetadata{ID: "mdns-living-room-tv", Kind: DeviceKindTV, Name: "Living Room TV", Model: "airplay", IP: "192.0.2.34", LastSeen: time.Now(), Capabilities: []Capability{CapabilityUnsupported}}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	items := m.List()
	if len(items) != 1 || items[0].ID != "samsung-living-room" || items[0].IP != "192.0.2.34" || items[0].Manufacturer != "Samsung" {
		t.Fatalf("address-refreshed inventory = %+v", items)
	}
}

func TestManagerKeepsPairingAcrossProviderIdentityChanges(t *testing.T) {
	store := &testStore{devices: []DeviceMetadata{
		{ID: "samsung-living-room", Kind: DeviceKindTV, Manufacturer: "Samsung", Model: "Test TV", Name: "Living Room TV", IP: "192.0.2.29", MAC: "AA:BB:CC:DD:EE:FF", DUID: "uuid:living-room", Paired: true, LastSeen: time.Now().Add(-time.Minute)},
		// This is the duplicate generic record that mDNS/SSDP can leave
		// alongside the Samsung record.
		{ID: "mdns-living-room", Kind: DeviceKindTV, Name: "Living Room TV", IP: "192.0.2.29", MAC: "aa:bb:cc:dd:ee:ff", Paired: false},
	}}
	provider := testProvider{item: DiscoveredDevice{Source: "mdns", Metadata: DeviceMetadata{
		ID: "mdns-living-room-new-id", Kind: DeviceKindTV, Name: "Living Room TV", IP: "192.0.2.34", MAC: "AA-BB-CC-DD-EE-FF",
	}}}
	m, err := NewManager(store, []DiscoveryProvider{provider}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	items := m.List()
	if len(items) != 1 || items[0].ID != "samsung-living-room" || !items[0].Paired || items[0].IP != "192.0.2.34" {
		t.Fatalf("pairing was not carried across provider identity change: %+v", items)
	}
}

func TestManagerClearsPairingAcrossDuplicateRecords(t *testing.T) {
	store := &testStore{devices: []DeviceMetadata{
		{ID: "samsung-living-room", Kind: DeviceKindTV, Manufacturer: "Samsung", Name: "Living Room TV", IP: "192.0.2.29", MAC: "AA:BB:CC:DD:EE:FF", DUID: "uuid:living-room", Paired: true},
		{ID: "mdns-living-room", Kind: DeviceKindTV, Name: "Living Room TV", IP: "192.0.2.29", MAC: "aa:bb:cc:dd:ee:ff", Paired: true},
	}}
	device := &testDevice{md: DeviceMetadata{ID: "samsung-living-room", Kind: DeviceKindTV, Manufacturer: "Samsung", Name: "Living Room TV", IP: "192.0.2.29", MAC: "AA:BB:CC:DD:EE:FF", DUID: "uuid:living-room", Paired: false}}
	m, err := NewManager(store, nil, []DeviceFactory{testFactory{device: device}})
	if err != nil {
		t.Fatal(err)
	}
	m.syncAdapterMetadata(context.Background(), device)
	for _, item := range m.List() {
		if item.Paired {
			t.Fatalf("duplicate record retained pairing after invalidation: %+v", item)
		}
	}
}

func TestManagerListCollapsesSamsungAndDisplayRecordsByName(t *testing.T) {
	seen := time.Now()
	store := &testStore{devices: []DeviceMetadata{
		{ID: "samsung-living-room", Kind: DeviceKindTV, Manufacturer: "Samsung", Model: "Test TV", Name: "Living Room TV", IP: "192.0.2.29", LastSeen: seen.Add(-time.Minute), Capabilities: []Capability{CapabilityStatus}},
		{ID: "mdns-living-room", Kind: DeviceKindTV, Name: "Living Room TV", Model: "airplay", IP: "192.0.2.34", LastSeen: seen, Capabilities: []Capability{CapabilityUnsupported}},
	}}
	m, err := NewManager(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := m.List()
	if len(items) != 1 || items[0].Manufacturer != "Samsung" || items[0].IP != "192.0.2.34" {
		t.Fatalf("collapsed inventory = %+v", items)
	}
}

func TestManagerKeepsSameNamedGenericTVsAtDifferentAddressesSeparate(t *testing.T) {
	store := &testStore{devices: []DeviceMetadata{
		{ID: "mdns-room-one", Kind: DeviceKindTV, Name: "Living Room TV", Model: "airplay", IP: "192.0.2.40"},
		{ID: "mdns-room-two", Kind: DeviceKindTV, Name: "Living Room TV", Model: "airplay", IP: "192.0.2.41"},
	}}
	m, err := NewManager(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if items := m.List(); len(items) != 2 {
		t.Fatalf("same-named generic TVs collapsed into %d record(s), want 2", len(items))
	}
}

func TestManagerScanResultsCollapseHostAndBonjourRecords(t *testing.T) {
	items := deduplicateScanResults([]DiscoveredDevice{
		{Source: "host", Metadata: DeviceMetadata{ID: "host", Kind: DeviceKindComputer, Manufacturer: "Apple", Model: "macOS", Name: "Mac mini", Discovery: "host", IP: "192.0.2.33"}},
		{Source: "mdns", Metadata: DeviceMetadata{ID: "mdns-mac", Kind: DeviceKindComputer, Manufacturer: "Apple", Model: "macOS", Name: "Mac mini", Discovery: "mdns", IP: "192.0.2.33"}},
	})
	if len(items) != 1 || items[0].Metadata.ID != "host" {
		t.Fatalf("scan result inventory = %+v, want one host record", items)
	}
}

func TestManagerCategoryVisibility(t *testing.T) {
	store := &testStore{devices: []DeviceMetadata{
		{ID: "samsung", Kind: DeviceKindTV, Manufacturer: "Samsung", IP: "192.0.2.10"},
		{ID: "mac", Kind: DeviceKindComputer, Manufacturer: "Apple", Model: "macOS", IP: "192.0.2.11"},
		{ID: "linux", Kind: DeviceKindComputer, Model: "Linux", IP: "192.0.2.12"},
		{ID: "windows", Kind: DeviceKindComputer, Model: "Windows 11", IP: "192.0.2.13"},
		{ID: "phone", Kind: DeviceKindMobile, Manufacturer: "Apple", Model: "iOS", IP: "192.0.2.14"},
		{ID: "display", Kind: DeviceKindMonitor, IP: "192.0.2.15"},
		{ID: "other", Kind: DeviceKindUnknown, IP: "192.0.2.16"},
	}}
	m, err := NewManager(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.SetInventoryVisibility(InventoryVisibility{ShowDisplayDevices: false, ShowConsoleDevices: false, ShowComputerDevices: true, ShowOfflineDevices: true})
	items := m.List()
	if len(items) != 2 {
		t.Fatalf("category-filtered inventory = %+v, want macOS and Windows", items)
	}
}

func TestManagerListHidesInvalidPersistedLANRecords(t *testing.T) {
	store := &testStore{devices: []DeviceMetadata{
		{ID: "broadcast", Kind: DeviceKindUnknown, Name: "broadcast", IP: "255.255.255.255"},
		{ID: "subnet-broadcast", Kind: DeviceKindUnknown, Name: "subnet broadcast", IP: "192.0.2.255"},
		{ID: "multicast", Kind: DeviceKindUnknown, Name: "multicast", IP: "224.0.0.251"},
		{ID: "valid", Kind: DeviceKindUnknown, Name: "valid", IP: "192.0.2.44"},
	}}
	m, err := NewManager(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := m.List()
	if len(items) != 0 {
		t.Fatalf("visible devices = %+v, want anonymous LAN records hidden", items)
	}
}

func TestManagerInventoryVisibilityFiltersNonProductAndOffline(t *testing.T) {
	store := &testStore{devices: []DeviceMetadata{
		{ID: "phone", Kind: DeviceKindMobile, Name: "Phone", Online: true, IP: "192.0.2.40"},
		{ID: "offline", Kind: DeviceKindComputer, Name: "Offline computer", Online: false, IP: "192.0.2.41"},
	}}
	m, err := NewManager(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.SetInventoryVisibility(InventoryVisibility{ShowComputerDevices: true, ShowOfflineDevices: false})
	items := m.List()
	if len(items) != 0 {
		t.Fatalf("filtered inventory = %+v, want empty", items)
	}
}

func TestManagerKeepsKnownDevicesVisibleWhenOffline(t *testing.T) {
	store := &testStore{devices: []DeviceMetadata{
		{ID: "tv-1", Kind: DeviceKindTV, Manufacturer: "Samsung", Name: "Living Room TV", IP: "192.0.2.20", Online: true, LastSeen: time.Now().Add(-time.Minute)},
		{ID: "console-1", Kind: DeviceKindConsole, Manufacturer: "Sony", Platform: "PlayStation", Name: "PlayStation", IP: "192.0.2.21", Online: true},
		{ID: "pi-1", Kind: DeviceKindComputer, Manufacturer: "Raspberry Pi", Model: "Raspberry Pi 5", Name: "Kitchen Pi", IP: "192.0.2.22", Online: true},
	}}
	m, err := NewManager(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := m.List()
	if len(items) != 2 {
		t.Fatalf("known inventory = %+v, want TV and Raspberry Pi only", items)
	}
	for _, item := range items {
		if item.Online {
			t.Fatalf("remembered device %q was reported online before a fresh scan", item.Name)
		}
	}

	refreshed, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(refreshed) != 2 {
		t.Fatalf("scan inventory = %+v, want remembered offline devices included", refreshed)
	}
	for _, item := range refreshed {
		if item.Metadata.Online {
			t.Fatalf("scan incorrectly marked %q online without a discovery response", item.Metadata.Name)
		}
	}
}

func TestManagerResolvesAndPersistsFriendlyDeviceNames(t *testing.T) {
	store := &testStore{devices: []DeviceMetadata{{ID: "tv-1", Kind: DeviceKindTV, Manufacturer: "Samsung", Name: "Living Room TV", IP: "192.0.2.20"}}}
	m, err := NewManager(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Rename(context.Background(), "tv-1", "Living Room TV"); err != nil {
		t.Fatal(err)
	}
	id, err := m.ResolveDeviceReference("Living Room TV")
	if err != nil || id != "tv-1" {
		t.Fatalf("friendly reference resolved to %q, %v", id, err)
	}
	items := m.List()
	if len(items) != 1 || items[0].Alias != "Living Room TV" || DisplayName(items[0]) != "Living Room TV" {
		t.Fatalf("renamed inventory = %+v", items)
	}
	if err := m.Rename(context.Background(), "Living Room TV", "Room TV"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ResolveDeviceReference("missing device"); err == nil {
		t.Fatal("missing device reference unexpectedly resolved")
	}
}

func TestManagerRehydratesLoadedDeviceAdapter(t *testing.T) {
	store := &testStore{devices: []DeviceMetadata{{ID: "tv-1", Kind: DeviceKindTV, Manufacturer: "Samsung", Name: "Living Room", IP: "192.0.2.20"}}}
	device := &testDevice{md: store.devices[0]}
	m, err := NewManager(store, nil, []DeviceFactory{testFactory{device: device}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Execute(context.Background(), Command{DeviceID: "tv-1", Action: ActionPowerOn, Principal: "test", Source: "test"}); err != nil {
		t.Fatalf("loaded adapter was not executable: %v", err)
	}
	if len(device.executed) != 1 {
		t.Fatalf("executed commands = %d, want 1", len(device.executed))
	}
}
