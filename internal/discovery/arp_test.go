package discovery

import "testing"

func TestIsAppleMAC(t *testing.T) {
	if !isAppleMAC("3C-15-C2-AA-BB-CC") {
		t.Fatal("expected Apple OUI to be recognized")
	}
	if isAppleMAC("80:86:5b:aa:bb:cc") {
		t.Fatal("unexpected non-Apple OUI match")
	}
}
