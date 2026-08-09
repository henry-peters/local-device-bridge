package cli

import (
	"fmt"
	"os"

	"github.com/mdp/qrterminal/v3"
	"golang.org/x/term"
)

// printPhoneQR renders a short-lived phone pairing URL directly in the
// terminal. It contains a one-time browser pairing token, never the reusable
// dashboard or Agent API token.
func printPhoneQR(url string) {
	if url == "" || !term.IsTerminal(int(os.Stdout.Fd())) {
		return
	}
	fmt.Println("  Scan this QR code with your phone camera:")
	qrterminal.GenerateWithConfig(url, qrterminal.Config{
		Level:          qrterminal.M,
		Writer:         os.Stdout,
		HalfBlocks:     false,
		BlackChar:      qrterminal.BLACK,
		BlackWhiteChar: qrterminal.BLACK,
		WhiteChar:      qrterminal.WHITE,
		WhiteBlackChar: qrterminal.WHITE,
		QuietZone:      1,
	})
	fmt.Println("  The QR signs this phone in automatically; no token needs to be typed.")
}
