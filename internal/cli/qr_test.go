package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintPhoneQRDoesNotRequireTTY(t *testing.T) {
	var output bytes.Buffer
	printPhoneQRTo(&output, "http://192.0.2.10:8787/?pair=one-time-test-token")
	if !strings.Contains(output.String(), "Scan this QR code") {
		t.Fatal("QR output did not render without a terminal")
	}
	if len(strings.TrimSpace(output.String())) < 100 {
		t.Fatal("QR output was unexpectedly empty")
	}
}
