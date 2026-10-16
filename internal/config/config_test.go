package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func envMap(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "leasorix.yml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultsAreValid(t *testing.T) {
	cfg, err := load("", envMap(nil))
	if err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	if cfg.HTTP.Listen != "127.0.0.1:8067" {
		t.Errorf("default listen = %q", cfg.HTTP.Listen)
	}
}

func TestLoadYAMLAndEnvOverride(t *testing.T) {
	p := writeFile(t, `
data_dir: /tmp/lx
log:
  level: debug
  format: text
http:
  listen: 0.0.0.0:9000
  shutdown_timeout: 3s
`)
	cfg, err := load(p, envMap(map[string]string{
		"LEASORIX_HTTP_LISTEN": "10.0.0.1:8443",
		"LEASORIX_LOG_LEVEL":   "warn",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "/tmp/lx" {
		t.Errorf("data_dir = %q", cfg.DataDir)
	}
	if cfg.Log.Format != "text" {
		t.Errorf("log.format = %q", cfg.Log.Format)
	}
	if cfg.Log.Level != "warn" {
		t.Errorf("env override for log.level not applied: %q", cfg.Log.Level)
	}
	if cfg.HTTP.Listen != "10.0.0.1:8443" {
		t.Errorf("env override for http.listen not applied: %q", cfg.HTTP.Listen)
	}
	if cfg.HTTP.ShutdownTimeout != 3*time.Second {
		t.Errorf("shutdown_timeout = %v", cfg.HTTP.ShutdownTimeout)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	p := writeFile(t, "htttp:\n  listen: x\n")
	if _, err := load(p, envMap(nil)); err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestEmptyFileUsesDefaults(t *testing.T) {
	p := writeFile(t, "")
	if _, err := load(p, envMap(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]map[string]string{
		"bad level":    {"LEASORIX_LOG_LEVEL": "loud"},
		"bad format":   {"LEASORIX_LOG_FORMAT": "xml"},
		"bad listen":   {"LEASORIX_HTTP_LISTEN": "nohostport"},
		"empty data":   {"LEASORIX_DATA_DIR": " "},
		"bad duration": {"LEASORIX_HTTP_SHUTDOWN_TIMEOUT": "soon"},
		"neg duration": {"LEASORIX_HTTP_SHUTDOWN_TIMEOUT": "-1s"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := load("", envMap(env)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestEnvSliceAndScalars(t *testing.T) {
	type inner struct {
		Names []string      `yaml:"names"`
		On    bool          `yaml:"on"`
		N     int           `yaml:"n"`
		U     uint16        `yaml:"u"`
		F     float64       `yaml:"f"`
		D     time.Duration `yaml:"d"`
	}
	type root struct {
		Inner inner `yaml:"inner"`
	}
	var r root
	err := applyEnv(&r, "X", envMap(map[string]string{
		"X_INNER_NAMES": "eth0, eth1,,",
		"X_INNER_ON":    "true",
		"X_INNER_N":     "-4",
		"X_INNER_U":     "67",
		"X_INNER_F":     "0.5",
		"X_INNER_D":     "1m",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.Inner.Names, "|") != "eth0|eth1" || !r.Inner.On || r.Inner.N != -4 ||
		r.Inner.U != 67 || r.Inner.F != 0.5 || r.Inner.D != time.Minute {
		t.Fatalf("unexpected result: %+v", r.Inner)
	}
}

func TestDHCP4Config(t *testing.T) {
	p := writeFile(t, `
dhcp4:
  interfaces: [eth0, eth1]
  conflict_detection:
    enabled: false
`)
	cfg, err := load(p, envMap(map[string]string{"LEASORIX_DHCP4_OFFER_TIMEOUT": "30s"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.DHCP4.Interfaces, ",") != "eth0,eth1" || cfg.DHCP4.ConflictDetection.Enabled ||
		cfg.DHCP4.OfferTimeout != 30*time.Second || cfg.DHCP4.DeclineTime != 24*time.Hour {
		t.Fatalf("unexpected dhcp4 config %+v", cfg.DHCP4)
	}
	for name, env := range map[string]map[string]string{
		"dup iface":    {"LEASORIX_DHCP4_INTERFACES": "eth0,eth0"},
		"zero workers": {"LEASORIX_DHCP4_WORKERS": "0"},
		"attempts":     {"LEASORIX_DHCP4_CONFLICT_DETECTION_ATTEMPTS": "9"},
		"grace":        {"LEASORIX_DHCP4_EXPIRED_GRACE": "0s"},
	} {
		if _, err := load("", envMap(env)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestAuditAndLogConfig(t *testing.T) {
	cfg, err := load("", envMap(map[string]string{
		"LEASORIX_AUDIT_RETENTION":      "720h",
		"LEASORIX_AUDIT_LEASE_RENEWALS": "true",
		"LEASORIX_LOG_FILE":             "/var/log/leasorix.log",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Audit.Retention != 720*time.Hour || !cfg.Audit.LeaseRenewals || !cfg.Audit.LeaseEvents || cfg.Log.MaxSizeMB != 100 {
		t.Fatalf("%+v %+v", cfg.Audit, cfg.Log)
	}
	for _, env := range []map[string]string{
		{"LEASORIX_AUDIT_RETENTION": "-1h"},
		{"LEASORIX_LOG_FILE": "/x", "LEASORIX_LOG_MAX_SIZE_MB": "0"},
		{"LEASORIX_LOG_MAX_BACKUPS": "-1"},
	} {
		if _, err := load("", envMap(env)); err == nil {
			t.Errorf("%v accepted", env)
		}
	}
}

func TestTLSAndAuthConfig(t *testing.T) {
	cfg, err := load("", envMap(nil))
	if err != nil || cfg.HTTP.TLS.Mode != TLSSelfSigned || cfg.Auth.LoginMaxFailures != 10 {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	for _, env := range []map[string]string{
		{"LEASORIX_HTTP_TLS_MODE": "maybe"},
		{"LEASORIX_HTTP_TLS_MODE": "files"},
		{"LEASORIX_AUTH_SESSION_TTL": "0s"},
		{"LEASORIX_AUTH_LOGIN_MAX_FAILURES": "0"},
	} {
		if _, err := load("", envMap(env)); err == nil {
			t.Errorf("%v accepted", env)
		}
	}
	cfg, err = load("", envMap(map[string]string{"LEASORIX_HTTP_TLS_MODE": "files", "LEASORIX_HTTP_TLS_CERT_FILE": "/c", "LEASORIX_HTTP_TLS_KEY_FILE": "/k"}))
	if err != nil || cfg.HTTP.TLS.CertFile != "/c" {
		t.Fatalf("files mode: %v", err)
	}
}

func TestHAConfig(t *testing.T) {
	base := map[string]string{
		"LEASORIX_HA_MODE":                 "load-balance",
		"LEASORIX_HA_NODE_ID":              "dhcp-a",
		"LEASORIX_HA_ROLE":                 "primary",
		"LEASORIX_HA_PEER_ID":              "dhcp-b",
		"LEASORIX_HA_PEER_ADDRESS":         "10.0.0.2:8068",
		"LEASORIX_HA_TLS_MODE":             "secret",
		"LEASORIX_HA_TLS_SHARED_SECRET":    "0123456789abcdef0123",
		"LEASORIX_HA_LOAD_BALANCE_PERCENT": "60",
	}
	cfg, err := load("", envMap(base))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HA.Enabled() || cfg.HA.MCLT != time.Hour || cfg.HA.Replication != HAReplSync || cfg.HA.LoadBalancePercent != 60 {
		t.Fatalf("ha config %+v", cfg.HA)
	}
	if d, _ := load("", envMap(nil)); d.HA.Enabled() {
		t.Fatal("HA must be off by default")
	}
	for k, v := range map[string]string{
		"LEASORIX_HA_MODE":                    "active-active",
		"LEASORIX_HA_ROLE":                    "leader",
		"LEASORIX_HA_PEER_ID":                 "dhcp-a",
		"LEASORIX_HA_NODE_ID":                 "bad id",
		"LEASORIX_HA_PEER_ADDRESS":            "nohost",
		"LEASORIX_HA_LOAD_BALANCE_PERCENT":    "101",
		"LEASORIX_HA_STANDBY_RESERVE_PERCENT": "60",
		"LEASORIX_HA_REPLICATION":             "eventual",
		"LEASORIX_HA_MCLT":                    "10s",
		"LEASORIX_HA_COMMUNICATION_TIMEOUT":   "3s",
		"LEASORIX_HA_CONFIG_LEADER":           "both",
		"LEASORIX_HA_TLS_SHARED_SECRET":       "short",
		"LEASORIX_HA_TLS_MODE":                "mtls",
	} {
		env := map[string]string{}
		for bk, bv := range base {
			env[bk] = bv
		}
		env[k] = v
		if _, err := load("", envMap(env)); err == nil {
			t.Errorf("%s=%s accepted", k, v)
		}
	}
	// Secret from a file.
	path := writeFile(t, "  file-secret-0123456789\n")
	env := map[string]string{}
	for bk, bv := range base {
		env[bk] = bv
	}
	delete(env, "LEASORIX_HA_TLS_SHARED_SECRET")
	env["LEASORIX_HA_TLS_SHARED_SECRET_FILE"] = path
	cfg, err = load("", envMap(env))
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := cfg.HA.Secret(); s != "file-secret-0123456789" {
		t.Fatalf("secret %q", s)
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	cfg, err := load("../../deploy/examples/leasorix.yml", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.DHCP4.Interfaces) == 0 {
		t.Fatal("example config without interfaces")
	}
}

func TestDHCP6Config(t *testing.T) {
	p := writeFile(t, "dhcp6:\n  decline_time: 1h\n")
	cfg, err := load(p, envMap(map[string]string{"LEASORIX_DHCP6_INTERFACES": "eth0,eth1"}))
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.DHCP6
	if strings.Join(d.Interfaces, "|") != "eth0|eth1" || d.DeclineTime != time.Hour || d.Workers != 64 || d.OfferTimeout != time.Minute {
		t.Fatalf("unexpected dhcp6 config: %+v", d)
	}
	p = writeFile(t, "dhcp6:\n  interfaces: [eth0]\n  workers: 0\n")
	if _, err := load(p, envMap(nil)); err == nil {
		t.Fatal("workers 0 accepted")
	}
}
