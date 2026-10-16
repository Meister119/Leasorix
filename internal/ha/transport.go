package ha

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"time"

	"google.golang.org/grpc/credentials"

	"github.com/meister119/leasorix/internal/config"
)

// Transport security of the peer connection.
//
// mtls: both servers present certificates of a common CA. The client
// verifies that the server certificate names the configured peer id, the
// server verifies the same for the client certificate.
//
// secret: TLS 1.3 with throw-away self-signed certificates, followed by a
// mutual proof of the shared secret: each side sends
// HMAC-SHA256(secret, label | role | node id | TLS exporter value). The
// exporter value is unique to the TLS session, so a man in the middle (who
// necessarily runs two TLS sessions) can neither forge nor relay a proof.
// Nothing travels over the connection before both proofs are verified.

const (
	exporterLabel = "EXPORTER-leasorix-ha-v1"
	proofLen      = sha256.Size
)

// Credentials builds the gRPC transport credentials for the configured
// security mode.
func Credentials(cfg config.HAConfig) (credentials.TransportCredentials, error) {
	switch cfg.TLS.Mode {
	case config.HATLSMutual:
		return mutualCreds(cfg)
	case config.HATLSSecret:
		secret, err := cfg.Secret()
		if err != nil {
			return nil, err
		}
		return secretCreds(cfg.NodeID, cfg.Peer.ID, []byte(secret))
	}
	return nil, fmt.Errorf("unknown ha.tls.mode %q", cfg.TLS.Mode)
}

func mutualCreds(cfg config.HAConfig) (credentials.TransportCredentials, error) {
	caPEM, err := os.ReadFile(cfg.TLS.CAFile)
	if err != nil {
		return nil, fmt.Errorf("ha.tls.ca_file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("ha.tls.ca_file: no certificate found")
	}
	cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("ha.tls cert/key: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, err
	}
	if !certNames(leaf, cfg.NodeID) {
		return nil, fmt.Errorf("ha.tls.cert_file is issued for %q, not for node_id %q", leaf.Subject.CommonName, cfg.NodeID)
	}
	peerID := cfg.Peer.ID
	return credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		// Client side: standard verification against the CA with the peer
		// id as expected name.
		RootCAs:    pool,
		ServerName: peerID,
		// Server side: require a client certificate of the CA naming the
		// configured peer.
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  pool,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || !certNames(cs.PeerCertificates[0], peerID) {
				return fmt.Errorf("peer certificate does not belong to %q", peerID)
			}
			return nil
		},
	}), nil
}

// CertExpiry returns the expiry of the configured node certificate (mtls).
func CertExpiry(cfg config.HAConfig) (time.Time, error) {
	cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
	if err != nil {
		return time.Time{}, err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return time.Time{}, err
	}
	return leaf.NotAfter, nil
}

func certNames(c *x509.Certificate, id string) bool {
	return slices.Contains(c.DNSNames, id) || (len(c.DNSNames) == 0 && c.Subject.CommonName == id)
}

// ---- shared secret -------------------------------------------------------------

type secretTransport struct {
	credentials.TransportCredentials
	self, peer string
	secret     []byte
}

func secretCreds(self, peer string, secret []byte) (credentials.TransportCredentials, error) {
	cert, err := ephemeralCert(self)
	if err != nil {
		return nil, err
	}
	tc := credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		// The certificate is not verified; authentication is the
		// channel-bound secret proof below.
		InsecureSkipVerify: true, //nolint:gosec // peer authenticated by the channel-bound HMAC proof (secretTransport)
	})
	return &secretTransport{TransportCredentials: tc, self: self, peer: peer, secret: secret}, nil
}

func (s *secretTransport) proof(role, node string, ekm []byte) []byte {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(exporterLabel + "|" + role + "|" + node + "|"))
	mac.Write(ekm)
	return mac.Sum(nil)
}

func exporter(info credentials.AuthInfo) ([]byte, error) {
	ti, ok := info.(credentials.TLSInfo)
	if !ok {
		return nil, errors.New("not a TLS connection")
	}
	return ti.State.ExportKeyingMaterial(exporterLabel, nil, 32)
}

func (s *secretTransport) exchange(ctx context.Context, conn net.Conn, info credentials.AuthInfo, client bool) error {
	ekm, err := exporter(info)
	if err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(10 * time.Second)
	}
	_ = conn.SetDeadline(deadline)
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	mine, theirs := s.proof("server", s.self, ekm), s.proof("client", s.peer, ekm)
	if client {
		mine, theirs = s.proof("client", s.self, ekm), s.proof("server", s.peer, ekm)
		if _, err := conn.Write(mine); err != nil {
			return err
		}
	}
	got := make([]byte, proofLen)
	if _, err := io.ReadFull(conn, got); err != nil {
		return fmt.Errorf("reading peer authentication: %w", err)
	}
	if !hmac.Equal(got, theirs) {
		return errors.New("peer authentication failed (wrong shared secret or node id)")
	}
	if !client {
		if _, err := conn.Write(mine); err != nil {
			return err
		}
	}
	return nil
}

func (s *secretTransport) ClientHandshake(ctx context.Context, authority string, raw net.Conn) (net.Conn, credentials.AuthInfo, error) {
	conn, info, err := s.TransportCredentials.ClientHandshake(ctx, authority, raw)
	if err != nil {
		return nil, nil, err
	}
	if err := s.exchange(ctx, conn, info, true); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, info, nil
}

func (s *secretTransport) ServerHandshake(raw net.Conn) (net.Conn, credentials.AuthInfo, error) {
	conn, info, err := s.TransportCredentials.ServerHandshake(raw)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.exchange(ctx, conn, info, false); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, info, nil
}

func (s *secretTransport) Clone() credentials.TransportCredentials {
	return &secretTransport{TransportCredentials: s.TransportCredentials.Clone(), self: s.self, peer: s.peer, secret: s.secret}
}

func ephemeralCert(cn string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now().Add(-time.Hour)
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    now,
		NotAfter:     now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
