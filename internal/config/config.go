// Package config loads the bootstrap configuration of leasorixd.
//
// The bootstrap configuration only covers process-level settings (listen
// addresses, data directory, logging, interfaces, HA peers). DHCP
// configuration such as scopes, reservations and options lives in the
// database and is managed through the API/UI.
//
// Values are resolved in this order (later wins):
//  1. built-in defaults (Default)
//  2. YAML file passed via --config
//  3. environment variables LEASORIX_<SECTION>_<KEY>
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// EnvPrefix is the prefix for all environment variable overrides.
const EnvPrefix = "LEASORIX"

// Config is the root of the bootstrap configuration.
type Config struct {
	// DataDir holds the SQLite database, certificates and other state.
	DataDir string      `yaml:"data_dir"`
	Log     LogConfig   `yaml:"log"`
	HTTP    HTTPConfig  `yaml:"http"`
	DHCP4   DHCP4Config `yaml:"dhcp4"`
	Audit   AuditConfig `yaml:"audit"`
	Auth    AuthConfig  `yaml:"auth"`
	HA      HAConfig    `yaml:"ha"`
}

// AuthConfig configures web sessions and the login rate limit.
type AuthConfig struct {
	// SessionTTL is the absolute lifetime of a web session.
	SessionTTL time.Duration `yaml:"session_ttl"`
	// IdleTimeout ends sessions without activity.
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	// SetupToken pre-defines the one-time token for creating the first
	// admin (default: random, printed to the log).
	SetupToken string `yaml:"setup_token"`
	// LoginMaxFailures failed logins per client IP and per username within
	// LoginWindow block further attempts.
	LoginMaxFailures int           `yaml:"login_max_failures"`
	LoginWindow      time.Duration `yaml:"login_window"`
}

// AuditConfig configures the audit log.
type AuditConfig struct {
	// Retention is how long audit entries are kept; 0 keeps them forever.
	Retention time.Duration `yaml:"retention"`
	// LeaseEvents records lease assign/release/decline/conflict/expire.
	LeaseEvents bool `yaml:"lease_events"`
	// LeaseRenewals additionally records every renewal (high volume).
	LeaseRenewals bool `yaml:"lease_renewals"`
}

// LogConfig configures structured logging.
type LogConfig struct {
	// Level is one of debug, info, warn, error.
	Level string `yaml:"level"`
	// Format is json or text.
	Format string `yaml:"format"`
	// File writes logs to this file instead of stderr, rotated by size.
	File string `yaml:"file"`
	// MaxSizeMB rotates the log file when it exceeds this size.
	MaxSizeMB int `yaml:"max_size_mb"`
	// MaxBackups is the number of rotated files to keep.
	MaxBackups int `yaml:"max_backups"`
}

// HTTPConfig configures the management listener (UI, REST API, health, metrics).
type HTTPConfig struct {
	// Listen is the host:port of the management listener. Defaults to
	// localhost only; bind it to a management interface explicitly.
	Listen string `yaml:"listen"`
	// ShutdownTimeout bounds graceful shutdown of in-flight requests.
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	TLS             TLSConfig     `yaml:"tls"`
}

// TLS modes.
const (
	TLSOff        = "off"
	TLSSelfSigned = "self-signed"
	TLSFiles      = "files"
)

// TLSConfig configures HTTPS for the management listener.
type TLSConfig struct {
	// Mode is off, self-signed (generated into <data_dir>/tls) or files.
	Mode     string `yaml:"mode"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// DHCP4Config configures the DHCPv4 service.
type DHCP4Config struct {
	// Interfaces to serve. Each one gets its own socket bound with
	// SO_BINDTODEVICE. Relayed traffic must arrive on one of them, too.
	// Empty = DHCPv4 service disabled.
	Interfaces []string `yaml:"interfaces"`
	// Workers bounds the number of packets processed concurrently.
	Workers int `yaml:"workers"`
	// OfferTimeout is how long an offered address is held for the client.
	OfferTimeout time.Duration `yaml:"offer_timeout"`
	// DeclineTime blocks declined / conflicting addresses for this long.
	DeclineTime time.Duration `yaml:"decline_time"`
	// ExpiredGrace keeps expired leases for the previous client this long.
	ExpiredGrace time.Duration `yaml:"expired_grace"`
	// ExpiryInterval is the period of the lease expiry worker.
	ExpiryInterval time.Duration `yaml:"expiry_interval"`
	// ConflictDetection pings addresses before offering them.
	ConflictDetection ConflictDetectionConfig `yaml:"conflict_detection"`
}

// ConflictDetectionConfig configures ICMP conflict detection.
type ConflictDetectionConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Timeout  time.Duration `yaml:"timeout"`
	Attempts int           `yaml:"attempts"`
}

// HA modes.
const (
	HAOff          = ""
	HALoadBalance  = "load-balance"
	HAHotStandby   = "hot-standby"
	HAPrimary      = "primary"
	HASecondary    = "secondary"
	HAReplSync     = "sync"
	HAReplAsync    = "async"
	HATLSMutual    = "mtls"
	HATLSSecret    = "secret"
	minSecretBytes = 16
)

// HAConfig configures the high availability relationship with one partner
// server. Both servers need matching settings (mode, percentages, MCLT,
// config leader); mismatches are detected and reported.
type HAConfig struct {
	// Mode is empty (no HA), load-balance or hot-standby.
	Mode string `yaml:"mode"`
	// NodeID identifies this server; it must match the node certificate
	// (mtls) and the partner's peer.id.
	NodeID string `yaml:"node_id"`
	// Role is primary or secondary. In hot-standby the primary serves all
	// clients; in load-balance the primary serves LoadBalancePercent of them.
	Role string `yaml:"role"`
	// Listen is the address of the peer (gRPC) listener.
	Listen string       `yaml:"listen"`
	Peer   HAPeerConfig `yaml:"peer"`
	// LoadBalancePercent is the share of clients (and free addresses)
	// handled by the primary in load-balance mode.
	LoadBalancePercent int `yaml:"load_balance_percent"`
	// StandbyReservePercent is the share of the free addresses reserved for
	// the standby in hot-standby mode.
	StandbyReservePercent int `yaml:"standby_reserve_percent"`
	// Replication is sync (lease change confirmed by the partner before the
	// ACK) or async.
	Replication string `yaml:"replication"`
	// MCLT (maximum client lead time) bounds leases granted without the
	// partner's knowledge and the wait before taking over the whole pool.
	MCLT time.Duration `yaml:"mclt"`
	// HeartbeatInterval is the period of status exchanges.
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
	// CommunicationTimeout without successful heartbeat switches to
	// COMMUNICATION_INTERRUPTED.
	CommunicationTimeout time.Duration `yaml:"communication_timeout"`
	// ReplicationTimeout bounds one synchronous lease update.
	ReplicationTimeout time.Duration `yaml:"replication_timeout"`
	// AutoPartnerDown switches from COMMUNICATION_INTERRUPTED to
	// PARTNER_DOWN after this time; 0 = only by operator confirmation.
	AutoPartnerDown time.Duration `yaml:"auto_partner_down"`
	// ConfigLeader names the role whose configuration is authoritative
	// (primary or secondary); the other server is read-only.
	ConfigLeader string      `yaml:"config_leader"`
	TLS          HATLSConfig `yaml:"tls"`
}

// HAPeerConfig identifies the partner server.
type HAPeerConfig struct {
	ID      string `yaml:"id"`
	Address string `yaml:"address"`
}

// HATLSConfig secures the peer connection: mtls (certificates of a common
// CA, the certificate names the node id) or secret (TLS with a shared
// secret bound to the TLS session).
type HATLSConfig struct {
	Mode             string `yaml:"mode"`
	CAFile           string `yaml:"ca_file"`
	CertFile         string `yaml:"cert_file"`
	KeyFile          string `yaml:"key_file"`
	SharedSecret     string `yaml:"shared_secret"`
	SharedSecretFile string `yaml:"shared_secret_file"`
}

// Enabled reports whether HA is configured.
func (h *HAConfig) Enabled() bool { return h.Mode != HAOff }

// Secret returns the shared secret (from the file if configured).
func (h *HAConfig) Secret() (string, error) {
	if h.TLS.SharedSecretFile == "" {
		return h.TLS.SharedSecret, nil
	}
	raw, err := os.ReadFile(h.TLS.SharedSecretFile)
	if err != nil {
		return "", fmt.Errorf("ha.tls.shared_secret_file: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// Default returns the built-in defaults.
func Default() Config {
	return Config{
		DataDir: "/var/lib/leasorix",
		Log: LogConfig{
			Level:      "info",
			Format:     "json",
			MaxSizeMB:  100,
			MaxBackups: 5,
		},
		Audit: AuditConfig{
			Retention:   90 * 24 * time.Hour,
			LeaseEvents: true,
		},
		HTTP: HTTPConfig{
			Listen:          "127.0.0.1:8067",
			ShutdownTimeout: 10 * time.Second,
			TLS:             TLSConfig{Mode: TLSSelfSigned},
		},
		Auth: AuthConfig{
			SessionTTL:       12 * time.Hour,
			IdleTimeout:      time.Hour,
			LoginMaxFailures: 10,
			LoginWindow:      15 * time.Minute,
		},
		HA: HAConfig{
			Listen:                ":8068",
			LoadBalancePercent:    50,
			StandbyReservePercent: 5,
			Replication:           HAReplSync,
			MCLT:                  time.Hour,
			HeartbeatInterval:     2 * time.Second,
			CommunicationTimeout:  10 * time.Second,
			ReplicationTimeout:    2 * time.Second,
			ConfigLeader:          HAPrimary,
			TLS:                   HATLSConfig{Mode: HATLSMutual},
		},
		DHCP4: DHCP4Config{
			Workers:        256,
			OfferTimeout:   60 * time.Second,
			DeclineTime:    24 * time.Hour,
			ExpiredGrace:   4 * time.Hour,
			ExpiryInterval: 10 * time.Second,
			ConflictDetection: ConflictDetectionConfig{
				Enabled:  true,
				Timeout:  500 * time.Millisecond,
				Attempts: 1,
			},
		},
	}
}

// Load builds the configuration from defaults, the optional YAML file at
// path (empty path = no file) and the process environment.
func Load(path string) (*Config, error) {
	return load(path, os.LookupEnv)
}

func load(path string, lookup func(string) (string, bool)) (*Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path) //nolint:gosec // path is an operator-supplied CLI flag
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := decodeYAML(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if err := applyEnv(&cfg, EnvPrefix, lookup); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func decodeYAML(raw []byte, cfg *Config) error {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// Validate checks the configuration for consistency.
func (c *Config) Validate() error {
	var errs []error
	if strings.TrimSpace(c.DataDir) == "" {
		errs = append(errs, errors.New("data_dir must not be empty"))
	}
	switch strings.ToLower(c.Log.Level) {
	case "debug", "info", "warn", "warning", "error":
	default:
		errs = append(errs, fmt.Errorf("log.level %q: must be debug, info, warn or error", c.Log.Level))
	}
	switch strings.ToLower(c.Log.Format) {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf("log.format %q: must be json or text", c.Log.Format))
	}
	if _, _, err := net.SplitHostPort(c.HTTP.Listen); err != nil {
		errs = append(errs, fmt.Errorf("http.listen %q: %w", c.HTTP.Listen, err))
	}
	if c.HTTP.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("http.shutdown_timeout must be positive"))
	}
	if c.Log.File != "" && c.Log.MaxSizeMB < 1 {
		errs = append(errs, errors.New("log.max_size_mb must be at least 1"))
	}
	if c.Log.MaxBackups < 0 {
		errs = append(errs, errors.New("log.max_backups must not be negative"))
	}
	switch c.HTTP.TLS.Mode {
	case TLSOff, TLSSelfSigned:
	case TLSFiles:
		if c.HTTP.TLS.CertFile == "" || c.HTTP.TLS.KeyFile == "" {
			errs = append(errs, errors.New("http.tls: cert_file and key_file are required for mode files"))
		}
	default:
		errs = append(errs, fmt.Errorf("http.tls.mode %q: must be off, self-signed or files", c.HTTP.TLS.Mode))
	}
	if c.Auth.SessionTTL <= 0 || c.Auth.IdleTimeout <= 0 || c.Auth.LoginWindow <= 0 || c.Auth.LoginMaxFailures < 1 {
		errs = append(errs, errors.New("auth: session_ttl, idle_timeout, login_window must be positive and login_max_failures at least 1"))
	}
	if c.Audit.Retention < 0 {
		errs = append(errs, errors.New("audit.retention must not be negative (0 = keep forever)"))
	}
	errs = append(errs, c.DHCP4.validate()...)
	errs = append(errs, c.HA.validate()...)
	return errors.Join(errs...)
}

func (h *HAConfig) validate() []error {
	if !h.Enabled() {
		return nil
	}
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf("ha."+format, args...)) }
	switch h.Mode {
	case HALoadBalance, HAHotStandby:
	default:
		add("mode %q: must be empty, load-balance or hot-standby", h.Mode)
	}
	if !ValidNodeID(h.NodeID) || !ValidNodeID(h.Peer.ID) {
		add("node_id and peer.id must be set (letters, digits, '.', '-', '_')")
	} else if h.NodeID == h.Peer.ID {
		add("node_id and peer.id must differ")
	}
	if h.Role != HAPrimary && h.Role != HASecondary {
		add("role %q: must be primary or secondary", h.Role)
	}
	if h.ConfigLeader != HAPrimary && h.ConfigLeader != HASecondary {
		add("config_leader %q: must be primary or secondary", h.ConfigLeader)
	}
	if _, _, err := net.SplitHostPort(h.Listen); err != nil {
		add("listen %q: %v", h.Listen, err)
	}
	if _, _, err := net.SplitHostPort(h.Peer.Address); err != nil {
		add("peer.address %q: %v", h.Peer.Address, err)
	}
	if h.LoadBalancePercent < 0 || h.LoadBalancePercent > 100 {
		add("load_balance_percent must be between 0 and 100")
	}
	if h.StandbyReservePercent < 0 || h.StandbyReservePercent > 50 {
		add("standby_reserve_percent must be between 0 and 50")
	}
	if h.Replication != HAReplSync && h.Replication != HAReplAsync {
		add("replication %q: must be sync or async", h.Replication)
	}
	if h.MCLT < time.Minute {
		add("mclt must be at least 1m")
	}
	if h.HeartbeatInterval <= 0 || h.CommunicationTimeout < 2*h.HeartbeatInterval {
		add("heartbeat_interval must be positive and communication_timeout at least twice as long")
	}
	if h.ReplicationTimeout <= 0 {
		add("replication_timeout must be positive")
	}
	if h.AutoPartnerDown < 0 {
		add("auto_partner_down must not be negative (0 = manual only)")
	}
	switch h.TLS.Mode {
	case HATLSMutual:
		if h.TLS.CAFile == "" || h.TLS.CertFile == "" || h.TLS.KeyFile == "" {
			add("tls: ca_file, cert_file and key_file are required for mode mtls")
		}
	case HATLSSecret:
		if h.TLS.SharedSecret != "" && h.TLS.SharedSecretFile != "" {
			add("tls: set either shared_secret or shared_secret_file")
		}
		if s, err := h.Secret(); err != nil {
			errs = append(errs, err)
		} else if len(s) < minSecretBytes {
			add("tls: the shared secret must be at least %d characters", minSecretBytes)
		}
	default:
		add("tls.mode %q: must be mtls or secret", h.TLS.Mode)
	}
	return errs
}

// ValidNodeID reports whether s is a valid HA node id: 1-64 letters,
// digits, '.', '-' or '_' (it is also used as certificate DNS name).
func ValidNodeID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

func (d *DHCP4Config) validate() []error {
	var errs []error
	seen := map[string]bool{}
	for _, n := range d.Interfaces {
		if strings.TrimSpace(n) == "" || seen[n] {
			errs = append(errs, fmt.Errorf("dhcp4.interfaces: empty or duplicate entry %q", n))
		}
		seen[n] = true
	}
	if d.Workers < 1 {
		errs = append(errs, errors.New("dhcp4.workers must be at least 1"))
	}
	for name, v := range map[string]time.Duration{
		"offer_timeout":   d.OfferTimeout,
		"decline_time":    d.DeclineTime,
		"expired_grace":   d.ExpiredGrace,
		"expiry_interval": d.ExpiryInterval,
	} {
		if v <= 0 {
			errs = append(errs, fmt.Errorf("dhcp4.%s must be positive", name))
		}
	}
	cd := d.ConflictDetection
	if cd.Enabled && (cd.Timeout <= 0 || cd.Attempts < 1 || cd.Attempts > 5) {
		errs = append(errs, errors.New("dhcp4.conflict_detection: timeout must be positive and attempts between 1 and 5"))
	}
	return errs
}
