package discovery

import (
	"net"
	"testing"
)

func TestIsBroadcastARPIP(t *testing.T) {
	if !isBroadcastARPIP(net.ParseIP("192.0.2.255")) {
		t.Fatal("expected subnet broadcast to be filtered")
	}
	if isBroadcastARPIP(net.ParseIP("192.0.2.44")) {
		t.Fatal("unexpected broadcast match")
	}
}
