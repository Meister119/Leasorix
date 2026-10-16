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
	return errors.Join(errs...)
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
