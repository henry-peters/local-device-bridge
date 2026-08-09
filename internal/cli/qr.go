package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/mdp/qrterminal/v3"
)

// printPhoneQR renders a short-lived phone pairing URL directly in the
// terminal. It contains a one-time browser pairing token, never the reusable
// dashboard or Agent API token.
func printPhoneQR(url string) {
	if url == "" {
		return
	}
	printPhoneQRTo(os.Stdout, url)
}

func printPhoneQRTo(writer io.Writer, url string) {
	if writer == nil || url == "" {
		return
	}
	fmt.Fprintln(writer, "  Scan this QR code with your phone camera:")
	qrterminal.GenerateWithConfig(url, qrterminal.Config{
		Level:          qrterminal.M,
		Writer:         writer,
		HalfBlocks:     false,
		BlackChar:      qrterminal.BLACK,
		BlackWhiteChar: qrterminal.BLACK,
		WhiteChar:      qrterminal.WHITE,
		WhiteBlackChar: qrterminal.WHITE,
		QuietZone:      1,
	})
	fmt.Fprintln(writer, "  The QR signs this phone in automatically; no token needs to be typed.")
}
