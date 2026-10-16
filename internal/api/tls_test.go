package api

import (
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/meister119/leasorix/internal/config"
)

func TestSelfSignedTLS(t *testing.T) {
	dir := t.TempDir()
	cfg, err := TLSConfig(config.TLSConfig{Mode: config.TLSSelfSigned}, dir, "10.1.2.3:8443")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(net.ParseIP("10.1.2.3")) {
			found = true
		}
	}
	if !found || leaf.Subject.CommonName != "leasorix" {
		t.Fatalf("SANs %v CN %s", leaf.IPAddresses, leaf.Subject.CommonName)
	}
	st, err := os.Stat(filepath.Join(dir, "tls", "key.pem"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key permissions: %v %v", st.Mode(), err)
	}
	// Second call reuses the files.
	cfg2, err := TLSConfig(config.TLSConfig{Mode: config.TLSSelfSigned}, dir, "10.1.2.3:8443")
	if err != nil || string(cfg2.Certificates[0].Certificate[0]) != string(cfg.Certificates[0].Certificate[0]) {
		t.Fatal("certificate regenerated")
	}
	if c, err := TLSConfig(config.TLSConfig{Mode: config.TLSOff}, dir, ""); c != nil || err != nil {
		t.Fatal("off mode")
	}
	if _, err := TLSConfig(config.TLSConfig{Mode: config.TLSFiles, CertFile: "/nope", KeyFile: "/nope"}, dir, ""); err == nil {
		t.Fatal("missing files accepted")
	}
}
