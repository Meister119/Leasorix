package scope

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/macaddr"
	"github.com/meister119/leasorix/internal/store"
)

// Store persists scopes, exclusions and reservations and maintains an
// immutable in-memory Snapshot that the DHCP engine reads lock-free.
// Every successful mutation rebuilds the snapshot and notifies listeners.
type Store struct {
	db  *store.DB
	now func() time.Time

	snap      atomic.Pointer[Snapshot]
	reloadMu  sync.Mutex
	version   uint64
	listenMu  sync.Mutex
	listeners []func(*Snapshot)
}

// NewStore creates a store. Call Load before use.
func NewStore(db *store.DB) *Store {
	s := &Store{db: db, now: time.Now}
	s.snap.Store(emptySnapshot())
	return s
}

// SetClock overrides the time source (tests).
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Snapshot returns the current immutable snapshot.
func (s *Store) Snapshot() *Snapshot { return s.snap.Load() }

// OnChange registers a listener called (synchronously) after every reload.
func (s *Store) OnChange(fn func(*Snapshot)) {
	s.listenMu.Lock()
	defer s.listenMu.Unlock()
	s.listeners = append(s.listeners, fn)
}

// Load (re)builds the snapshot from the database.
func (s *Store) Load(ctx context.Context) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	var (
		d   snapshotData
		err error
	)
	if d.scopes, err = s.ListScopes(ctx); err != nil {
		return err
	}
	if d.exclusions, err = s.listExclusions(ctx, 0); err != nil {
		return err
	}
	if d.reservations, err = s.listReservations(ctx, 0); err != nil {
		return err
	}
	if d.options, err = s.queryOptions(ctx, ""); err != nil {
		return err
	}
	if d.policies, err = s.ListPolicies(ctx, -1); err != nil {
		return err
	}
	if d.classes, err = s.ListClasses(ctx); err != nil {
		return err
	}
	if d.customDefs, err = s.listCustomDefinitions(ctx); err != nil {
		return err
	}
	if d.filters, err = s.ListFilters(ctx, ""); err != nil {
		return err
	}
	if d.filterCfg, err = s.readFilterSettings(ctx); err != nil {
		return err
	}
	s.version++
	snap := buildSnapshot(s.version, &d)
	s.snap.Store(snap)

	s.listenMu.Lock()
	ls := append([]func(*Snapshot){}, s.listeners...)
	s.listenMu.Unlock()
	for _, fn := range ls {
		fn(snap)
	}
	return nil
}

func (s *Store) mutate(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if err := s.db.WriteTx(ctx, fn); err != nil {
		return err
	}
	return s.Load(ctx)
}

// ---- scopes ---------------------------------------------------------------

const scopeCols = `id, name, description, network, prefix_len, start_ip, end_ip, lease_time, enabled, created_at, updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanScope(r rowScanner) (Scope, error) {
	var (
		sc                            Scope
		network, start, end, leaseSec int64
		bits                          int
		enabled                       bool
		created, updated              int64
	)
	if err := r.Scan(&sc.ID, &sc.Name, &sc.Description, &network, &bits, &start, &end, &leaseSec, &enabled, &created, &updated); err != nil {
		return sc, err
	}
	sc.Subnet = netip.PrefixFrom(ip4.ToAddr(uint32(network)), bits)
	sc.Start = ip4.ToAddr(uint32(start))
	sc.End = ip4.ToAddr(uint32(end))
	sc.LeaseTime = time.Duration(leaseSec) * time.Second
	sc.Enabled = enabled
	sc.CreatedAt = time.Unix(created, 0).UTC()
	sc.UpdatedAt = time.Unix(updated, 0).UTC()
	return sc, nil
}

// ListScopes returns all scopes ordered by network address.
func (s *Store) ListScopes(ctx context.Context) ([]Scope, error) {
	rows, err := s.db.R.QueryContext(ctx, `SELECT `+scopeCols+` FROM scopes ORDER BY network`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Scope
	for rows.Next() {
		sc, err := scanScope(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// GetScope returns one scope.
func (s *Store) GetScope(ctx context.Context, id int64) (Scope, error) {
	sc, err := scanScope(s.db.R.QueryRowContext(ctx, `SELECT `+scopeCols+` FROM scopes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return sc, apperr.NotFoundf("scope %d", id)
	}
	return sc, err
}

func checkScopeConflicts(ctx context.Context, tx *sql.Tx, sc *Scope) error {
	network := int64(ip4.MustFromAddr(sc.Subnet.Addr()))
	bcast := int64(ip4.Broadcast(uint32(network), sc.Subnet.Bits()))
	var other string
	// Two subnets overlap iff one contains the other's network address.
	err := tx.QueryRowContext(ctx, `
		SELECT name FROM scopes
		WHERE id <> ? AND network <= ? AND (network | ((1 << (32 - prefix_len)) - 1)) >= ?
		LIMIT 1`, sc.ID, bcast, network).Scan(&other)
	switch {
	case err == nil:
		return apperr.Conflictf("subnet %s overlaps scope %q", sc.Subnet, other)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	err = tx.QueryRowContext(ctx, `SELECT name FROM scopes WHERE id <> ? AND name = ?`, sc.ID, sc.Name).Scan(&other)
	switch {
	case err == nil:
		return apperr.Conflictf("scope name %q already exists", sc.Name)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	return nil
}

// CreateScope validates and inserts a new scope.
func (s *Store) CreateScope(ctx context.Context, sc Scope) (Scope, error) {
	sc.ID = 0
	if err := sc.validate(); err != nil {
		return sc, err
	}
	now := s.now().UTC().Truncate(time.Second)
	sc.CreatedAt, sc.UpdatedAt = now, now
	err := s.mutate(ctx, func(tx *sql.Tx) error {
		if err := checkScopeConflicts(ctx, tx, &sc); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO scopes (name, description, network, prefix_len, start_ip, end_ip, lease_time, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sc.Name, sc.Description, ip4.MustFromAddr(sc.Subnet.Addr()), sc.Subnet.Bits(),
			ip4.MustFromAddr(sc.Start), ip4.MustFromAddr(sc.End), int64(sc.LeaseTime/time.Second),
			sc.Enabled, now.Unix(), now.Unix())
		if err != nil {
			return err
		}
		sc.ID, err = res.LastInsertId()
		return err
	})
	return sc, err
}

// UpdateScope updates name, description, range, lease time and enabled
// state. The subnet of an existing scope cannot be changed.
func (s *Store) UpdateScope(ctx context.Context, sc Scope) (Scope, error) {
	if err := sc.validate(); err != nil {
		return sc, err
	}
	err := s.mutate(ctx, func(tx *sql.Tx) error {
		old, err := scanScope(tx.QueryRowContext(ctx, `SELECT `+scopeCols+` FROM scopes WHERE id = ?`, sc.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFoundf("scope %d", sc.ID)
		}
		if err != nil {
			return err
		}
		if old.Subnet != sc.Subnet {
			var v apperr.Validator
			v.Add("subnet", "the subnet of an existing scope cannot be changed")
			return v.Err()
		}
		if err := checkScopeConflicts(ctx, tx, &sc); err != nil {
			return err
		}
		r := sc.Range()
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM exclusions WHERE scope_id = ? AND (start_ip < ? OR end_ip > ?)`,
			sc.ID, r.Start, r.End).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			var v apperr.Validator
			v.Add("start", "%d exclusion range(s) would lie outside the new address range", n)
			return v.Err()
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM policy_ranges r JOIN policies p ON p.id = r.policy_id
			WHERE p.scope_id = ? AND (r.start_ip < ? OR r.end_ip > ?)`, sc.ID, r.Start, r.End).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			var v apperr.Validator
			v.Add("start", "%d policy range(s) would lie outside the new address range", n)
			return v.Err()
		}
		sc.CreatedAt = old.CreatedAt
		sc.UpdatedAt = s.now().UTC().Truncate(time.Second)
		_, err = tx.ExecContext(ctx, `
			UPDATE scopes SET name = ?, description = ?, start_ip = ?, end_ip = ?, lease_time = ?, enabled = ?, updated_at = ?
			WHERE id = ?`,
			sc.Name, sc.Description, r.Start, r.End, int64(sc.LeaseTime/time.Second), sc.Enabled, sc.UpdatedAt.Unix(), sc.ID)
		return err
	})
	return sc, err
}

// DeleteScope removes a scope together with its exclusions, reservations,
// options and leases.
func (s *Store) DeleteScope(ctx context.Context, id int64) error {
	return s.mutate(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM scopes WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.NotFoundf("scope %d", id)
		}
		return nil
	})
}

// ---- exclusions -----------------------------------------------------------

func (s *Store) listExclusions(ctx context.Context, scopeID int64) ([]Exclusion, error) {
	q := `SELECT id, scope_id, start_ip, end_ip FROM exclusions`
	var args []any
	if scopeID != 0 {
		q += ` WHERE scope_id = ?`
		args = append(args, scopeID)
	}
	q += ` ORDER BY scope_id, start_ip`
	rows, err := s.db.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Exclusion
	for rows.Next() {
		var e Exclusion
		var start, end int64
		if err := rows.Scan(&e.ID, &e.ScopeID, &start, &end); err != nil {
			return nil, err
		}
		e.Start, e.End = ip4.ToAddr(uint32(start)), ip4.ToAddr(uint32(end))
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListExclusions returns the exclusions of a scope.
func (s *Store) ListExclusions(ctx context.Context, scopeID int64) ([]Exclusion, error) {
	if _, err := s.GetScope(ctx, scopeID); err != nil {
		return nil, err
	}
	return s.listExclusions(ctx, scopeID)
}

// AddExclusion adds an exclusion range. It must lie inside the scope's
// address range and must not overlap another exclusion.
func (s *Store) AddExclusion(ctx context.Context, e Exclusion) (Exclusion, error) {
	var v apperr.Validator
	start, okS := ip4.FromAddr(e.Start)
	end, okE := ip4.FromAddr(e.End)
	v.Check(okS, "start", "must be an IPv4 address")
	v.Check(okE, "end", "must be an IPv4 address")
	if okS && okE {
		v.Check(start <= end, "end", "must not be lower than start")
	}
	if err := v.Err(); err != nil {
		return e, err
	}
	err := s.mutate(ctx, func(tx *sql.Tx) error {
		sc, err := scanScope(tx.QueryRowContext(ctx, `SELECT `+scopeCols+` FROM scopes WHERE id = ?`, e.ScopeID))
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFoundf("scope %d", e.ScopeID)
		}
		if err != nil {
			return err
		}
		r := sc.Range()
		if !r.Contains(start) || !r.Contains(end) {
			var v apperr.Validator
			v.Add("start", "exclusion %s must lie within the scope range %s", ip4.Range{Start: start, End: end}, r)
			return v.Err()
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM exclusions WHERE scope_id = ? AND start_ip <= ? AND end_ip >= ?`,
			e.ScopeID, end, start).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return apperr.Conflictf("exclusion overlaps an existing exclusion")
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO exclusions (scope_id, start_ip, end_ip) VALUES (?, ?, ?)`, e.ScopeID, start, end)
		if err != nil {
			return err
		}
		e.ID, err = res.LastInsertId()
		return err
	})
	return e, err
}

// DeleteExclusion removes an exclusion of the given scope.
func (s *Store) DeleteExclusion(ctx context.Context, scopeID, id int64) error {
	return s.mutate(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM exclusions WHERE id = ? AND scope_id = ?`, id, scopeID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.NotFoundf("exclusion %d", id)
		}
		return nil
	})
}

// ---- reservations ---------------------------------------------------------

const resCols = `id, scope_id, ip, mac, client_id, hostname, description, created_at, updated_at`

func scanReservation(r rowScanner) (Reservation, error) {
	var (
		res              Reservation
		ip               int64
		created, updated int64
	)
	if err := r.Scan(&res.ID, &res.ScopeID, &ip, &res.MAC, &res.ClientID, &res.Hostname, &res.Description, &created, &updated); err != nil {
		return res, err
	}
	res.IP = ip4.ToAddr(uint32(ip))
	res.CreatedAt = time.Unix(created, 0).UTC()
	res.UpdatedAt = time.Unix(updated, 0).UTC()
	return res, nil
}

func (s *Store) listReservations(ctx context.Context, scopeID int64) ([]Reservation, error) {
	q := `SELECT ` + resCols + ` FROM reservations`
	var args []any
	if scopeID != 0 {
		q += ` WHERE scope_id = ?`
		args = append(args, scopeID)
	}
	q += ` ORDER BY ip`
	rows, err := s.db.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reservation
	for rows.Next() {
		r, err := scanReservation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListReservations returns the reservations of a scope (0 = all scopes).
func (s *Store) ListReservations(ctx context.Context, scopeID int64) ([]Reservation, error) {
	if scopeID != 0 {
		if _, err := s.GetScope(ctx, scopeID); err != nil {
			return nil, err
		}
	}
	return s.listReservations(ctx, scopeID)
}

// GetReservation returns one reservation.
func (s *Store) GetReservation(ctx context.Context, id int64) (Reservation, error) {
	r, err := scanReservation(s.db.R.QueryRowContext(ctx, `SELECT `+resCols+` FROM reservations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, apperr.NotFoundf("reservation %d", id)
	}
	return r, err
}

func (r *Reservation) normalize() error {
	var v apperr.Validator
	if r.MAC != "" {
		m, err := macaddr.Normalize(r.MAC)
		if err != nil {
			v.Add("mac", "%v", err)
		}
		r.MAC = m
	}
	if r.ClientID != "" {
		c, err := macaddr.NormalizeClientID(r.ClientID)
		if err != nil {
			v.Add("client_id", "%v", err)
		}
		r.ClientID = c
	}
	v.Check(r.MAC != "" || r.ClientID != "", "mac", "MAC address or client identifier is required")
	v.Check(r.IP.Is4(), "ip", "must be an IPv4 address")
	r.Hostname = strings.TrimSpace(r.Hostname)
	v.Check(len(r.Hostname) <= 255, "hostname", "too long")
	return v.Err()
}

func (s *Store) checkReservation(ctx context.Context, tx *sql.Tx, r *Reservation) error {
	sc, err := scanScope(tx.QueryRowContext(ctx, `SELECT `+scopeCols+` FROM scopes WHERE id = ?`, r.ScopeID))
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFoundf("scope %d", r.ScopeID)
	}
	if err != nil {
		return err
	}
	ip := ip4.MustFromAddr(r.IP)
	network := ip4.MustFromAddr(sc.Subnet.Addr())
	bits := sc.Subnet.Bits()
	if !ip4.Contains(network, bits, ip) || ip == network || ip == ip4.Broadcast(network, bits) {
		var v apperr.Validator
		v.Add("ip", "%s is not a usable address of subnet %s", r.IP, sc.Subnet)
		return v.Err()
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM reservations WHERE id <> ? AND ip = ?`, r.ID, ip).Scan(&id)
	if err == nil {
		return apperr.Conflictf("%s is already reserved (reservation %d)", r.IP, id)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if r.MAC != "" {
		err = tx.QueryRowContext(ctx, `SELECT id FROM reservations WHERE id <> ? AND scope_id = ? AND mac = ?`, r.ID, r.ScopeID, r.MAC).Scan(&id)
		if err == nil {
			return apperr.Conflictf("MAC %s already has a reservation in this scope (reservation %d)", r.MAC, id)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if r.ClientID != "" {
		err = tx.QueryRowContext(ctx, `SELECT id FROM reservations WHERE id <> ? AND scope_id = ? AND client_id = ?`, r.ID, r.ScopeID, r.ClientID).Scan(&id)
		if err == nil {
			return apperr.Conflictf("client identifier already has a reservation in this scope (reservation %d)", id)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

// CreateReservation validates and inserts a reservation.
func (s *Store) CreateReservation(ctx context.Context, r Reservation) (Reservation, error) {
	r.ID = 0
	if err := r.normalize(); err != nil {
		return r, err
	}
	now := s.now().UTC().Truncate(time.Second)
	r.CreatedAt, r.UpdatedAt = now, now
	err := s.mutate(ctx, func(tx *sql.Tx) error {
		if err := s.checkReservation(ctx, tx, &r); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO reservations (scope_id, ip, mac, client_id, hostname, description, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ScopeID, ip4.MustFromAddr(r.IP), r.MAC, r.ClientID, r.Hostname, r.Description, now.Unix(), now.Unix())
		if err != nil {
			return err
		}
		r.ID, err = res.LastInsertId()
		return err
	})
	return r, err
}

// UpdateReservation updates a reservation (it may not move between scopes).
func (s *Store) UpdateReservation(ctx context.Context, r Reservation) (Reservation, error) {
	if err := r.normalize(); err != nil {
		return r, err
	}
	err := s.mutate(ctx, func(tx *sql.Tx) error {
		old, err := scanReservation(tx.QueryRowContext(ctx, `SELECT `+resCols+` FROM reservations WHERE id = ?`, r.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFoundf("reservation %d", r.ID)
		}
		if err != nil {
			return err
		}
		r.ScopeID = old.ScopeID
		r.CreatedAt = old.CreatedAt
		r.UpdatedAt = s.now().UTC().Truncate(time.Second)
		if err := s.checkReservation(ctx, tx, &r); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE reservations SET ip = ?, mac = ?, client_id = ?, hostname = ?, description = ?, updated_at = ?
			WHERE id = ?`,
			ip4.MustFromAddr(r.IP), r.MAC, r.ClientID, r.Hostname, r.Description, r.UpdatedAt.Unix(), r.ID)
		return err
	})
	return r, err
}

// DeleteReservation removes a reservation.
func (s *Store) DeleteReservation(ctx context.Context, id int64) error {
	return s.mutate(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM reservations WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.NotFoundf("reservation %d", id)
		}
		return nil
	})
}

// String implements fmt.Stringer for log output.
func (s Scope) String() string {
	return fmt.Sprintf("%s (%s)", s.Name, s.Subnet)
}
