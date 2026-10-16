package ha

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/meister119/leasorix/internal/config"
)

// NodeCert is a generated node certificate with its private key (PEM).
type NodeCert struct {
	NodeID  string
	CertPEM []byte
	KeyPEM  []byte
}

// CertBundle is a freshly generated CA with one certificate per node. The
// CA key is discarded: for a new node the whole bundle is regenerated.
type CertBundle struct {
	CAPEM    []byte
	Nodes    []NodeCert
	NotAfter time.Time
}

// Certificate lifetimes.
const (
	caValidity   = 10 * 365 * 24 * time.Hour
	nodeValidity = 5 * 365 * 24 * time.Hour
)

// GenerateCerts creates a CA and node certificates for mutual TLS between
// the given nodes. Each node certificate carries the node id as DNS name
// and common name; the partner verifies exactly that name.
func GenerateCerts(nodeIDs []string) (*CertBundle, error) {
	if len(nodeIDs) < 2 {
		return nil, errors.New("at least two node ids are required")
	}
	seen := map[string]bool{}
	for _, id := range nodeIDs {
		if !config.ValidNodeID(id) || seen[id] {
			return nil, fmt.Errorf("invalid or duplicate node id %q", id)
		}
		seen[id] = true
	}
	now := time.Now().Add(-5 * time.Minute)
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Leasorix HA CA", Organization: []string{"Leasorix"}},
		NotBefore:             now,
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}
	b := &CertBundle{CAPEM: pemBlock("CERTIFICATE", caDER), NotAfter: now.Add(nodeValidity)}
	for _, id := range nodeIDs {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		tmpl := &x509.Certificate{
			SerialNumber: serial(),
			Subject:      pkix.Name{CommonName: id, Organization: []string{"Leasorix"}},
			DNSNames:     []string{id},
			NotBefore:    now,
			NotAfter:     b.NotAfter,
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
		if err != nil {
			return nil, err
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, err
		}
		b.Nodes = append(b.Nodes, NodeCert{NodeID: id, CertPEM: pemBlock("CERTIFICATE", der), KeyPEM: pemBlock("PRIVATE KEY", keyDER)})
	}
	return b, nil
}

// WriteFiles stores the bundle as ca.pem, <node>.pem and <node>-key.pem in
// dir (keys with mode 0600). Existing files are not overwritten.
func (b *CertBundle) WriteFiles(dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	type file struct {
		name string
		data []byte
		mode os.FileMode
	}
	files := []file{{"ca.pem", b.CAPEM, 0o644}}
	for _, n := range b.Nodes {
		files = append(files, file{n.NodeID + ".pem", n.CertPEM, 0o644}, file{n.NodeID + "-key.pem", n.KeyPEM, 0o600})
	}
	var written []string
	for _, f := range files {
		p := filepath.Join(dir, f.name)
		fh, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, f.mode) //nolint:gosec // path built from the output directory chosen by the operator
		if err != nil {
			return written, err
		}
		_, err = fh.Write(f.data)
		if cerr := fh.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return written, err
		}
		written = append(written, p)
	}
	return written, nil
}

func pemBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err)
	}
	return n
}
