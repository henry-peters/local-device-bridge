package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func generateCertificate(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return err
	}
	// The bridge uses one self-signed certificate for its private LAN HTTPS
	// endpoint. Mark it as a trustable local root so the explicit
	// `dashboard trust` action works on macOS, Linux, and Windows. It is still
	// never trusted automatically and must be installed by the user.
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "local-device-bridge"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost", "local-device-bridge.local"},
		IPAddresses:           certificateIPs(),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return err
	}
	certFile, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		_ = certFile.Close()
		return err
	}
	if err := certFile.Close(); err != nil {
		return err
	}
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyFile, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		_ = keyFile.Close()
		return err
	}
	if err := keyFile.Close(); err != nil {
		return err
	}
	return nil
}

func certificateIPs() []net.IP {
	result := []net.IP{net.IPv4(127, 0, 0, 1)}
	interfaces, err := net.Interfaces()
	if err != nil {
		return result
	}
	seen := map[string]bool{"127.0.0.1": true}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			var ip net.IP
			switch value := address.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if ip == nil || ip.To4() == nil || ip.IsLinkLocalUnicast() {
				continue
			}
			ip = ip.To4()
			if !seen[ip.String()] {
				seen[ip.String()] = true
				result = append(result, ip)
			}
		}
	}
	return result
}

func certificateMatchesLocalIPs(certPath string) bool {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return false
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	// Certificates generated before the local-root format was introduced are
	// regenerated so `dashboard trust` can establish browser trust correctly.
	if !certificate.IsCA || !certificate.BasicConstraintsValid {
		return false
	}
	for _, required := range certificateIPs() {
		found := false
		for _, actual := range certificate.IPAddresses {
			if required.Equal(actual) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return time.Now().Before(certificate.NotAfter)
}
