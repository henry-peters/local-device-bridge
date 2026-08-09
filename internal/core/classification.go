package core

import "strings"

// NormalizeMetadata turns provider-specific hints into a stable user-facing
// identity. It never invents control capabilities: classification and support
// are deliberately separate concerns.
func NormalizeMetadata(md DeviceMetadata) DeviceMetadata {
	text := strings.ToLower(strings.Join([]string{md.Manufacturer, md.Model, md.Name, md.Discovery}, " "))
	setManufacturer := func(name string, markers ...string) bool {
		if strings.EqualFold(md.Manufacturer, name) {
			return true
		}
		for _, marker := range markers {
			if strings.Contains(text, marker) {
				md.Manufacturer = name
				return true
			}
		}
		return false
	}

	switch {
	case containsAny(text, "playstation", "ps5", "ps4"):
		md.Manufacturer, md.Platform, md.Kind = "Sony", "PlayStation", DeviceKindConsole
	case containsAny(text, "xbox"):
		md.Manufacturer, md.Platform, md.Kind = "Microsoft", "Xbox", DeviceKindConsole
	case containsAny(text, "nintendo", "switch"):
		md.Manufacturer, md.Platform, md.Kind = "Nintendo", "Nintendo", DeviceKindConsole
	case setManufacturer("Sony", "sony", "bravia"):
		md.Platform = "Sony BRAVIA"
	case setManufacturer("LG", "lg electronics", "webos", "lg tv", "lg oled"):
		md.Platform = "LG webOS"
	case setManufacturer("Samsung", "samsung", "tizen"):
		md.Platform = "Samsung TV"
	case setManufacturer("Roku", "roku"):
		md.Platform = "Roku TV"
	case md.Kind == DeviceKindComputer && containsAny(text, "windows", "win32", "microsoft"):
		md.Manufacturer = "Microsoft"
		if isWindowsLaptopMetadata(md) {
			md.Platform = "Windows laptop"
		} else {
			md.Platform = "Windows"
		}
	}
	// Repair records written by older discovery builds that treated every UPnP
	// MediaRenderer as a television. Sonos is a Linux audio device, not a TV.
	if containsAny(text, "sonos", "rincon") {
		md.Kind, md.Manufacturer, md.Platform, md.Category = DeviceKindUnknown, "Sonos", "Audio device", CategoryOther
		return md
	}
	// Audio products frequently advertise UPnP/AirPlay services that look like
	// a display to generic discovery. They are deliberately outside this
	// product's inventory: only computers, Raspberry Pis, TVs, and displays
	// are user-facing devices.
	if containsAny(text, "soundbar", "speaker", "homepod", "musiccast", "heos", "soundtouch", "audio receiver", "audio system", "wireless audio", "media renderer", "bose", "denon", "marantz", "yamaha audio") {
		md.Kind, md.Platform, md.Category = DeviceKindUnknown, "Audio device", CategoryOther
		return md
	}

	if md.Kind == DeviceKindUnknown {
		switch {
		case containsAny(text, "television", " smart tv", " tv ", "tv-", "bravia", "webos", "googlecast", "airplay"):
			md.Kind = DeviceKindTV
		case containsAny(text, "playstation", "ps5", "ps4", "xbox", "nintendo switch"):
			md.Kind = DeviceKindConsole
		}
	}

	switch {
	case md.Kind == DeviceKindConsole:
		md.Category = CategoryConsole
		if md.Platform == "" {
			md.Platform = "Game console"
		}
	case md.Kind == DeviceKindTV || md.Kind == DeviceKindMonitor:
		md.Category = CategoryTVDisplay
		if md.Platform == "" {
			md.Platform = "TV / display"
		}
	case isMobileMetadata(md):
		md.Category = CategoryMobile
		if md.Platform == "" {
			if strings.EqualFold(md.Manufacturer, "Apple") || containsAny(text, "iphone", "ipad", "ios") {
				md.Platform = "iPhone / iPad"
			} else {
				md.Platform = "Android / mobile"
			}
		}
	case isMacOSMetadata(md):
		md.Category, md.Platform = CategoryComputer, "macOS"
	case isRaspberryPiMetadata(md):
		md.Category, md.Platform = CategoryComputer, "Raspberry Pi"
	case isWindowsMetadata(md):
		md.Category = CategoryComputer
		if md.Platform == "" {
			md.Platform = "Windows"
		}
	case md.Kind == DeviceKindComputer:
		// Generic Linux advertisements are not a safe control target. Keep them
		// internal so only macOS, Windows, and Raspberry Pi identities appear.
		md.Category = CategoryOther
		if md.Platform == "" {
			md.Platform = "Unsupported computer"
		}
	default:
		md.Category = CategoryOther
		if md.Platform == "" {
			md.Platform = "Network device"
		}
	}
	return md
}

func containsAny(value string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
