package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/meister119/leasorix/internal/config"
)

// TLSConfig builds the server TLS configuration. It returns nil for mode
// "off". In "self-signed" mode a certificate is generated into
// <dataDir>/tls on first start and reused afterwards.
func TLSConfig(cfg config.TLSConfig, dataDir, listen string) (*tls.Config, error) {
	var certFile, keyFile string
	switch cfg.Mode {
	case config.TLSOff:
		return nil, nil
	case config.TLSFiles:
		certFile, keyFile = cfg.CertFile, cfg.KeyFile
	case config.TLSSelfSigned:
		dir := filepath.Join(dataDir, "tls")
		certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
		if _, err := os.Stat(certFile); os.IsNotExist(err) {
			if err := writeSelfSigned(dir, certFile, keyFile, listen); err != nil {
				return nil, fmt.Errorf("generate self-signed certificate: %w", err)
			}
		}
	default:
		return nil, fmt.Errorf("unknown tls mode %q", cfg.Mode)
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load certificate: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
}

func writeSelfSigned(dir, certFile, keyFile, listen string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "leasorix", Organization: []string{"Leasorix (self-signed)"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(825 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	if host != "" && host != "localhost" {
		tmpl.DNSNames = append(tmpl.DNSNames, host)
	}
	if h, _, err := net.SplitHostPort(listen); err == nil {
		if ip := net.ParseIP(h); ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if ip == nil && h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644) //nolint:gosec // public certificate
}
