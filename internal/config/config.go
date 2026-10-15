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
	DataDir string     `yaml:"data_dir"`
	Log     LogConfig  `yaml:"log"`
	HTTP    HTTPConfig `yaml:"http"`
}

// LogConfig configures structured logging.
type LogConfig struct {
	// Level is one of debug, info, warn, error.
	Level string `yaml:"level"`
	// Format is json or text.
	Format string `yaml:"format"`
}

// HTTPConfig configures the management listener (UI, REST API, health, metrics).
type HTTPConfig struct {
	// Listen is the host:port of the management listener. Defaults to
	// localhost only; bind it to a management interface explicitly.
	Listen string `yaml:"listen"`
	// ShutdownTimeout bounds graceful shutdown of in-flight requests.
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// Default returns the built-in defaults.
func Default() Config {
	return Config{
		DataDir: "/var/lib/leasorix",
		Log: LogConfig{
			Level:  "info",
			Format: "json",
		},
		HTTP: HTTPConfig{
			Listen:          "127.0.0.1:8067",
			ShutdownTimeout: 10 * time.Second,
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
	return errors.Join(errs...)
}
