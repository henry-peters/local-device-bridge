package core

import "testing"

func TestNormalizeMetadataCategories(t *testing.T) {
	tests := []struct {
		name         string
		md           DeviceMetadata
		category     DeviceCategory
		manufacturer string
	}{
		{"generic television", DeviceMetadata{Kind: DeviceKindTV, Name: "Living Room TV"}, CategoryTVDisplay, ""},
		{"LG webOS", DeviceMetadata{Kind: DeviceKindTV, Manufacturer: "LG Electronics", Model: "webOS TV"}, CategoryTVDisplay, "LG"},
		{"Sony BRAVIA", DeviceMetadata{Kind: DeviceKindTV, Model: "BRAVIA XR"}, CategoryTVDisplay, "Sony"},
		{"PlayStation", DeviceMetadata{Name: "PlayStation 5"}, CategoryConsole, "Sony"},
		{"Xbox", DeviceMetadata{Name: "Xbox Series X"}, CategoryConsole, "Microsoft"},
		{"Nintendo", DeviceMetadata{Name: "Nintendo Switch"}, CategoryConsole, "Nintendo"},
		{"Raspberry Pi", DeviceMetadata{Kind: DeviceKindComputer, Model: "Raspberry Pi OS"}, CategoryComputer, ""},
		{"Windows laptop", DeviceMetadata{Kind: DeviceKindComputer, Model: "Windows 11 laptop"}, CategoryComputer, "Microsoft"},
		{"generic Linux hidden", DeviceMetadata{Kind: DeviceKindComputer, Model: "Linux workstation"}, CategoryOther, ""},
		{"Windows desktop", DeviceMetadata{Kind: DeviceKindComputer, Model: "Windows 11 desktop"}, CategoryComputer, "Microsoft"},
		{"macOS", DeviceMetadata{Kind: DeviceKindComputer, Manufacturer: "Apple", Model: "macOS"}, CategoryComputer, "Apple"},
		{"Sonos speaker hidden", DeviceMetadata{Kind: DeviceKindUnknown, Manufacturer: "Sonos", Model: "Media Renderer", Name: "Living Room Speaker"}, CategoryOther, "Sonos"},
		{"soundbar hidden", DeviceMetadata{Kind: DeviceKindUnknown, Model: "Wireless Soundbar", Name: "Family Audio"}, CategoryOther, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NormalizeMetadata(test.md)
			if got.Category != test.category || got.Manufacturer != test.manufacturer {
				t.Fatalf("NormalizeMetadata() = category %q manufacturer %q; want %q %q", got.Category, got.Manufacturer, test.category, test.manufacturer)
			}
		})
	}
}
