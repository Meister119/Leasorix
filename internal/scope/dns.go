package scope

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/meister119/leasorix/internal/apperr"
)

// DNSMode controls dynamic DNS updates of a scope (Windows: DNS tab).
type DNSMode string

// DNS update modes.
const (
	// DNSNever: no updates.
	DNSNever DNSMode = "never"
	// DNSClient: update as requested by the client in the Client FQDN
	// option (RFC 4702): A and PTR, or only PTR when the client updates
	// its A record itself. Clients without the option are not registered.
	DNSClient DNSMode = "client"
	// DNSAlways: A and PTR for every client that sends a name (option 81
	// or host name option 12).
	DNSAlways DNSMode = "always"
)

// Valid reports whether m is a known mode.
func (m DNSMode) Valid() bool { return m == DNSNever || m == DNSClient || m == DNSAlways }

// MaxDNSTTL is the longest configurable record TTL.
const MaxDNSTTL = 7 * 24 * time.Hour

// DNSSettings are the DNS update settings of a scope.
type DNSSettings struct {
	Mode DNSMode
	// Domain is appended to the client's host name; empty = the domain
	// name option (15) the client receives.
	Domain string
	// TTL of the records; 0 = automatic (a third of the lease time,
	// between one minute and one day).
	TTL time.Duration
	// Cleanup removes the records when the lease ends (release, expiry,
	// deletion).
	Cleanup bool
	// Protect enables name protection (DHCID records, RFC 4701/4703): a
	// name registered for another client, or created manually, is never
	// overwritten.
	Protect bool
}

// DefaultDNSSettings are the settings of a new scope.
func DefaultDNSSettings() DNSSettings {
	return DNSSettings{Mode: DNSNever, Cleanup: true, Protect: true}
}

func (d *DNSSettings) validate(v *apperr.Validator) {
	if d.Mode == "" {
		d.Mode = DNSNever
	}
	v.Check(d.Mode.Valid(), "dns.mode", "must be never, client or always")
	d.Domain = NormalizeDNSName(d.Domain)
	if d.Domain != "" {
		v.Check(ValidDNSName(d.Domain), "dns.domain", "must be a domain name like example.lan")
	}
	v.Check(d.TTL >= 0 && d.TTL <= MaxDNSTTL && d.TTL%time.Second == 0, "dns.ttl",
		"must be 0 (automatic) or up to %d seconds", int(MaxDNSTTL/time.Second))
}

// NormalizeDNSName lower-cases a domain name and removes surrounding
// whitespace and dots.
func NormalizeDNSName(s string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(s)), ".")
}

// ValidDNSName reports whether s (normalized, without trailing dot) is a
// syntactically valid host or domain name: labels of letters, digits and
// hyphens (underscores allowed for service-style names), 1-63 characters,
// no leading or trailing hyphen, at most 253 characters.
func ValidDNSName(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			if !labelChar(l[i]) {
				return false
			}
		}
	}
	return true
}

func labelChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

// ---- TSIG keys -------------------------------------------------------------------

// DNSKeyAlgorithms are the supported TSIG algorithms (RFC 8945), the
// recommended one first.
var DNSKeyAlgorithms = []string{"hmac-sha256", "hmac-sha384", "hmac-sha512", "hmac-sha224", "hmac-sha1", "hmac-md5"}

// minTSIGSecret is the minimum secret length in bytes.
const minTSIGSecret = 16

// DNSKey is a TSIG key that signs DNS updates.
type DNSKey struct {
	ID        int64
	Name      string
	Algorithm string
	// Secret is the base64 encoded key. It is required on creation and
	// keeps its value on update when empty. The API never returns it.
	Secret string
}

func (k *DNSKey) validate(create bool) error {
	var v apperr.Validator
	k.Name = NormalizeDNSName(k.Name)
	v.Check(ValidDNSName(k.Name), "name", "must be a key name like leasorix-key")
	k.Algorithm = strings.ToLower(strings.TrimSpace(k.Algorithm))
	if k.Algorithm == "" {
		k.Algorithm = DNSKeyAlgorithms[0]
	}
	v.Check(slices.Contains(DNSKeyAlgorithms, k.Algorithm), "algorithm", "must be one of %s", strings.Join(DNSKeyAlgorithms, ", "))
	k.Secret = strings.TrimSpace(k.Secret)
	if k.Secret != "" || create {
		b, err := base64.StdEncoding.DecodeString(k.Secret)
		v.Check(err == nil && len(b) >= minTSIGSecret, "secret", "must be base64 encoded with at least %d bytes (e.g. from tsig-keygen)", minTSIGSecret)
	}
	return v.Err()
}

func dnsKeyDetails(k DNSKey) map[string]any {
	return map[string]any{"name": k.Name, "algorithm": k.Algorithm}
}

// ListDNSKeys returns all TSIG keys by name (with secrets; callers must not
// expose them).
func (s *Store) ListDNSKeys(ctx context.Context) ([]DNSKey, error) {
	rows, err := s.db.R.QueryContext(ctx, `SELECT id, name, algorithm, secret FROM dns_keys ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DNSKey{}
	for rows.Next() {
		var k DNSKey
		if err := rows.Scan(&k.ID, &k.Name, &k.Algorithm, &k.Secret); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// GetDNSKey returns one key.
func (s *Store) GetDNSKey(ctx context.Context, id int64) (DNSKey, error) {
	var k DNSKey
	err := s.db.R.QueryRowContext(ctx, `SELECT id, name, algorithm, secret FROM dns_keys WHERE id = ?`, id).
		Scan(&k.ID, &k.Name, &k.Algorithm, &k.Secret)
	if errors.Is(err, sql.ErrNoRows) {
		return k, apperr.NotFoundf("TSIG key %d", id)
	}
	return k, err
}

func uniqueDNS(err error, what, name string) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return apperr.Conflictf("%s %q already exists", what, name)
	}
	return err
}

// CreateDNSKey adds a TSIG key.
func (s *Store) CreateDNSKey(ctx context.Context, k DNSKey) (DNSKey, error) {
	k.ID = 0
	if err := k.validate(true); err != nil {
		return k, err
	}
	err := s.mutate(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO dns_keys (name, algorithm, secret) VALUES (?, ?, ?)`, k.Name, k.Algorithm, k.Secret)
		if err != nil {
			return uniqueDNS(err, "TSIG key", k.Name)
		}
		if k.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		return s.rec(ctx, tx, "dns_key.create", "dns_key", k.ID, change(nil, dnsKeyDetails(k)))
	})
	return k, err
}

// UpdateDNSKey changes a key; an empty secret keeps the stored one.
func (s *Store) UpdateDNSKey(ctx context.Context, k DNSKey) (DNSKey, error) {
	if err := k.validate(false); err != nil {
		return k, err
	}
	old, err := s.GetDNSKey(ctx, k.ID)
	if err != nil {
		return k, err
	}
	if k.Secret == "" {
		k.Secret = old.Secret
	}
	err = s.mutate(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE dns_keys SET name = ?, algorithm = ?, secret = ? WHERE id = ?`, k.Name, k.Algorithm, k.Secret, k.ID); err != nil {
			return uniqueDNS(err, "TSIG key", k.Name)
		}
		d := change(dnsKeyDetails(old), dnsKeyDetails(k))
		if k.Secret != old.Secret {
			d["secret_changed"] = true
		}
		return s.rec(ctx, tx, "dns_key.update", "dns_key", k.ID, d)
	})
	return k, err
}

// DeleteDNSKey removes a key that no zone uses.
func (s *Store) DeleteDNSKey(ctx context.Context, id int64) error {
	old, err := s.GetDNSKey(ctx, id)
	if err != nil {
		return err
	}
	return s.mutate(ctx, func(tx *sql.Tx) error {
		zones, err := zonesUsingKey(ctx, tx, id)
		if err != nil {
			return err
		}
		if len(zones) > 0 {
			return apperr.Conflictf("TSIG key %q is used by zone(s) %s", old.Name, strings.Join(zones, ", "))
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM dns_keys WHERE id = ?`, id); err != nil {
			return err
		}
		return s.rec(ctx, tx, "dns_key.delete", "dns_key", id, change(dnsKeyDetails(old), nil))
	})
}

func zonesUsingKey(ctx context.Context, tx *sql.Tx, keyID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM dns_zones WHERE key_id = ? ORDER BY name`, keyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var zones []string
	for rows.Next() {
		var z string
		if err := rows.Scan(&z); err != nil {
			return nil, err
		}
		zones = append(zones, z)
	}
	return zones, rows.Err()
}

// ---- zones ------------------------------------------------------------------------

// DNSZone is a zone that receives dynamic updates.
type DNSZone struct {
	ID int64
	// Name is the zone apex, e.g. example.lan or 1.168.192.in-addr.arpa.
	Name string
	// Server is the primary name server: host or IP, optionally with
	// port (default 53).
	Server string
	// KeyID is the TSIG key (0 = unsigned updates).
	KeyID int64
	// KeyName is the name of the key (read only).
	KeyName string
}

func (z *DNSZone) validate() error {
	var v apperr.Validator
	z.Name = NormalizeDNSName(z.Name)
	v.Check(ValidDNSName(z.Name), "name", "must be a zone name like example.lan or 1.168.192.in-addr.arpa")
	z.Server = strings.TrimSpace(z.Server)
	if _, err := ServerAddr(z.Server); err != nil {
		v.Add("server", "%s", err.Error())
	}
	v.Check(z.KeyID >= 0, "key_id", "must not be negative")
	return v.Err()
}

// ServerAddr returns host:port for a zone's server setting (default port
// 53).
func ServerAddr(s string) (string, error) {
	if s == "" {
		return "", errors.New("must not be empty")
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		// No port: a bare host name or (unbracketed) IP address.
		host, port = strings.Trim(s, "[]"), "53"
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return "", errors.New("must be host or host:port with a port between 1 and 65535")
	}
	if net.ParseIP(host) == nil && !ValidDNSName(NormalizeDNSName(host)) {
		return "", errors.New("must be an IP address or host name, optionally with :port")
	}
	return net.JoinHostPort(host, port), nil
}

func dnsZoneDetails(z DNSZone) map[string]any {
	return map[string]any{"name": z.Name, "server": z.Server, "key_id": z.KeyID}
}

const dnsZoneQuery = `SELECT z.id, z.name, z.server, IFNULL(z.key_id, 0), IFNULL(k.name, '')
	FROM dns_zones z LEFT JOIN dns_keys k ON k.id = z.key_id`

func scanZones(rows *sql.Rows) ([]DNSZone, error) {
	defer rows.Close()
	out := []DNSZone{}
	for rows.Next() {
		var z DNSZone
		if err := rows.Scan(&z.ID, &z.Name, &z.Server, &z.KeyID, &z.KeyName); err != nil {
			return nil, err
		}
		out = append(out, z)
	}
	return out, rows.Err()
}

// ListDNSZones returns all zones by name.
func (s *Store) ListDNSZones(ctx context.Context) ([]DNSZone, error) {
	rows, err := s.db.R.QueryContext(ctx, dnsZoneQuery+` ORDER BY z.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	return scanZones(rows)
}

// GetDNSZone returns one zone.
func (s *Store) GetDNSZone(ctx context.Context, id int64) (DNSZone, error) {
	rows, err := s.db.R.QueryContext(ctx, dnsZoneQuery+` WHERE z.id = ?`, id)
	if err != nil {
		return DNSZone{}, err
	}
	zs, err := scanZones(rows)
	if err != nil {
		return DNSZone{}, err
	}
	if len(zs) == 0 {
		return DNSZone{}, apperr.NotFoundf("DNS zone %d", id)
	}
	return zs[0], nil
}

func checkZoneKey(ctx context.Context, tx *sql.Tx, z *DNSZone) error {
	z.KeyName = ""
	if z.KeyID == 0 {
		return nil
	}
	err := tx.QueryRowContext(ctx, `SELECT name FROM dns_keys WHERE id = ?`, z.KeyID).Scan(&z.KeyName)
	if errors.Is(err, sql.ErrNoRows) {
		var v apperr.Validator
		v.Add("key_id", "TSIG key %d does not exist", z.KeyID)
		return v.Err()
	}
	return err
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// CreateDNSZone adds a zone.
func (s *Store) CreateDNSZone(ctx context.Context, z DNSZone) (DNSZone, error) {
	z.ID = 0
	if err := z.validate(); err != nil {
		return z, err
	}
	err := s.mutate(ctx, func(tx *sql.Tx) error {
		if err := checkZoneKey(ctx, tx, &z); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO dns_zones (name, server, key_id) VALUES (?, ?, ?)`, z.Name, z.Server, nullID(z.KeyID))
		if err != nil {
			return uniqueDNS(err, "DNS zone", z.Name)
		}
		if z.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		return s.rec(ctx, tx, "dns_zone.create", "dns_zone", z.ID, change(nil, dnsZoneDetails(z)))
	})
	return z, err
}

// UpdateDNSZone changes a zone.
func (s *Store) UpdateDNSZone(ctx context.Context, z DNSZone) (DNSZone, error) {
	if err := z.validate(); err != nil {
		return z, err
	}
	old, err := s.GetDNSZone(ctx, z.ID)
	if err != nil {
		return z, err
	}
	err = s.mutate(ctx, func(tx *sql.Tx) error {
		if err := checkZoneKey(ctx, tx, &z); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE dns_zones SET name = ?, server = ?, key_id = ? WHERE id = ?`, z.Name, z.Server, nullID(z.KeyID), z.ID); err != nil {
			return uniqueDNS(err, "DNS zone", z.Name)
		}
		return s.rec(ctx, tx, "dns_zone.update", "dns_zone", z.ID, change(dnsZoneDetails(old), dnsZoneDetails(z)))
	})
	return z, err
}

// DeleteDNSZone removes a zone.
func (s *Store) DeleteDNSZone(ctx context.Context, id int64) error {
	old, err := s.GetDNSZone(ctx, id)
	if err != nil {
		return err
	}
	return s.mutate(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM dns_zones WHERE id = ?`, id); err != nil {
			return err
		}
		return s.rec(ctx, tx, "dns_zone.delete", "dns_zone", id, change(dnsZoneDetails(old), nil))
	})
}

// ---- snapshot view ------------------------------------------------------------

// DNSTarget is a zone with everything needed to send updates to it.
type DNSTarget struct {
	Zone string // zone apex without trailing dot
	// Server is host:port.
	Server string
	// KeyName, Algorithm and Secret sign the update (empty KeyName =
	// unsigned).
	KeyName, Algorithm, Secret string
}

func (s *Store) loadDNSTargets(ctx context.Context) ([]DNSTarget, error) {
	rows, err := s.db.R.QueryContext(ctx, `SELECT z.name, z.server, IFNULL(k.name, ''), IFNULL(k.algorithm, ''), IFNULL(k.secret, '')
		FROM dns_zones z LEFT JOIN dns_keys k ON k.id = z.key_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DNSTarget
	for rows.Next() {
		var t DNSTarget
		if err := rows.Scan(&t.Zone, &t.Server, &t.KeyName, &t.Algorithm, &t.Secret); err != nil {
			return nil, err
		}
		addr, err := ServerAddr(t.Server)
		if err != nil {
			continue
		}
		t.Zone, t.Server = NormalizeDNSName(t.Zone), addr
		out = append(out, t)
	}
	// Longest zone first, so the first suffix match is the most specific.
	slices.SortFunc(out, func(a, b DNSTarget) int { return len(b.Zone) - len(a.Zone) })
	return out, rows.Err()
}

// DNSTargetFor returns the most specific configured zone containing name
// (normalized, without trailing dot), or nil.
func (s *Snapshot) DNSTargetFor(name string) *DNSTarget {
	for i := range s.dnsTargets {
		t := &s.dnsTargets[i]
		if name == t.Zone || strings.HasSuffix(name, "."+t.Zone) {
			return t
		}
	}
	return nil
}
