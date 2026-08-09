package discovery

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

func TestIPv4HostsExcludesNetworkBroadcastAndOwnAddress(t *testing.T) {
	_, network, err := net.ParseCIDR("192.0.2.0/29")
	if err != nil {
		t.Fatal(err)
	}
	hosts := IPv4Hosts(network, net.ParseIP("192.0.2.2"))
	if len(hosts) != 5 {
		t.Fatalf("hosts = %v, want five usable addresses", hosts)
	}
	for _, host := range hosts {
		if host == "192.0.2.0" || host == "192.0.2.7" || host == "192.0.2.2" {
			t.Fatalf("invalid host returned: %s", host)
		}
	}
}

func TestRaspberryPiOUI(t *testing.T) {
	if !isRaspberryPiMAC("dc:a6:32:00:11:22") {
		t.Fatal("expected Raspberry Pi OUI")
	}
	if isRaspberryPiMAC("80:86:5b:00:11:22") {
		t.Fatal("unexpected Raspberry Pi OUI")
	}
}

func TestDiscoveryNetworkDiagnostics(t *testing.T) {
	if os.Getenv("LDB_NETWORK_TEST") == "" {
		t.Skip("set LDB_NETWORK_TEST=1 to inspect the local LAN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	networks := LocalIPv4Networks(nil)
	t.Logf("local IPv4 networks: %+v", networks)
	mdnsDevices, mdnsErr := (&MDNSProvider{Timeout: 1500 * time.Millisecond}).Discover(ctx)
	t.Logf("mDNS devices=%+v err=%v", mdnsDevices, mdnsErr)
	ssdpDevices, ssdpErr := (&SSDPProvider{Timeout: 2 * time.Second}).Discover(ctx)
	t.Logf("SSDP devices=%+v err=%v", ssdpDevices, ssdpErr)
	arpDevices, arpErr := (&ARPProvider{}).Discover(ctx)
	t.Logf("ARP/API devices=%+v err=%v", arpDevices, arpErr)
}
