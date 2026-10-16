package scope6

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
	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/store"
)

// Store persists the DHCPv6 configuration and maintains the snapshot.
type Store struct {
	db  *store.DB
	now func() time.Time

	snap      atomic.Pointer[Snapshot]
	reloadMu  sync.Mutex
	version   uint64
	listenMu  sync.Mutex
	listeners []func(*Snapshot)
}

// NewStore creates a store; call Load before use.
func NewStore(db *store.DB) *Store {
	s := &Store{db: db, now: time.Now}
	s.snap.Store(buildSnapshot(0, &snapshotData{}))
	return s
}

// SetClock overrides the time source (tests).
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Snapshot returns the current snapshot.
func (s *Store) Snapshot() *Snapshot { return s.snap.Load() }

// OnChange registers a listener called after every reload.
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
	if d.exclusions, err = s.ListExclusions(ctx, 0); err != nil {
		return err
	}
	if d.reservations, err = s.ListReservations(ctx, 0); err != nil {
		return err
	}
	if d.options, err = s.allOptions(ctx); err != nil {
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

func rec(ctx context.Context, tx *sql.Tx, action, objType string, id any, before, after map[string]any) error {
	d := map[string]any{}
	if before != nil {
		d["before"] = before
	}
	if after != nil {
		d["after"] = after
	}
	return audit.Insert(ctx, tx, audit.New(ctx, action, objType, fmt.Sprint(id), d))
}

func addrText(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

func prefixText(p netip.Prefix) string {
	if !p.IsValid() {
		return ""
	}
	return p.String()
}

func parseAddr(s string) netip.Addr {
	a, _ := netip.ParseAddr(s)
	return a
}

func parsePrefix(s string) netip.Prefix {
	p, _ := netip.ParsePrefix(s)
	return p
}

// ---- scopes -----------------------------------------------------------------------

const scopeCols = `id, name, description, prefix, start_ip, end_ip, preferred_lifetime, valid_lifetime,
	rapid_commit, preference, enabled, pd_pool, pd_length, created_at, updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanScope(r rowScanner) (Scope, error) {
	var (
		sc                        Scope
		prefix, start, end, pool  string
		pref, valid, created, upd int64
		preference                int
	)
	if err := r.Scan(&sc.ID, &sc.Name, &sc.Description, &prefix, &start, &end, &pref, &valid,
		&sc.RapidCommit, &preference, &sc.Enabled, &pool, &sc.PDLength, &created, &upd); err != nil {
		return sc, err
	}
	sc.Prefix, sc.Start, sc.End, sc.PDPool = parsePrefix(prefix), parseAddr(start), parseAddr(end), parsePrefix(pool)
	sc.PreferredLifetime, sc.ValidLifetime = time.Duration(pref)*time.Second, time.Duration(valid)*time.Second
	sc.Preference = uint8(preference)
	sc.CreatedAt, sc.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(upd, 0).UTC()
	return sc, nil
}

func scopeDetails(sc Scope) map[string]any {
	return map[string]any{
		"name": sc.Name, "description": sc.Description, "prefix": prefixText(sc.Prefix), "start": addrText(sc.Start), "end": addrText(sc.End),
		"preferred_lifetime": int64(sc.PreferredLifetime / time.Second), "valid_lifetime": int64(sc.ValidLifetime / time.Second),
		"rapid_commit": sc.RapidCommit, "preference": sc.Preference, "enabled": sc.Enabled,
		"pd_pool": prefixText(sc.PDPool), "pd_length": sc.PDLength,
	}
}

// ListScopes returns all scopes ordered by name.
func (s *Store) ListScopes(ctx context.Context) ([]Scope, error) {
	rows, err := s.db.R.QueryContext(ctx, `SELECT `+scopeCols+` FROM scopes6 ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Scope{}
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
	sc, err := scanScope(s.db.R.QueryRowContext(ctx, `SELECT `+scopeCols+` FROM scopes6 WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return sc, apperr.NotFoundf("IPv6 scope %d", id)
	}
	return sc, err
}

// checkOverlaps rejects scopes whose prefix or delegation pool overlaps
// another scope's prefix or pool.
func checkOverlaps(ctx context.Context, tx *sql.Tx, sc *Scope) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, name, prefix, pd_pool FROM scopes6 WHERE id != ?`, sc.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id                 int64
			name, prefix, pool string
		)
		if err := rows.Scan(&id, &name, &prefix, &pool); err != nil {
			return err
		}
		others := []netip.Prefix{parsePrefix(prefix), parsePrefix(pool)}
		for _, o := range others {
			if !o.IsValid() {
				continue
			}
			if o.Overlaps(sc.Prefix) {
				return apperr.Conflictf("prefix %s overlaps IPv6 scope %q", sc.Prefix, name)
			}
			if sc.PDPool.IsValid() && o.Overlaps(sc.PDPool) {
				return apperr.Conflictf("delegation pool %s overlaps IPv6 scope %q", sc.PDPool, name)
			}
		}
	}
	return rows.Err()
}

func uniqueName(err error, name string) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return apperr.Conflictf("IPv6 scope %q already exists", name)
	}
	return err
}

// CreateScope adds a scope.
func (s *Store) CreateScope(ctx context.Context, sc Scope) (Scope, error) {
	sc.ID = 0
	if err := sc.validate(); err != nil {
		return sc, err
	}
	now := s.now().UTC().Truncate(time.Second)
	sc.CreatedAt, sc.UpdatedAt = now, now
	err := s.mutate(ctx, func(tx *sql.Tx) error {
		if err := checkOverlaps(ctx, tx, &sc); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO scopes6 (name, description, prefix, start_ip, end_ip, preferred_lifetime, valid_lifetime,
			rapid_commit, preference, enabled, pd_pool, pd_length, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sc.Name, sc.Description, prefixText(sc.Prefix), addrText(sc.Start), addrText(sc.End),
			int64(sc.PreferredLifetime/time.Second), int64(sc.ValidLifetime/time.Second), sc.RapidCommit, int(sc.Preference), sc.Enabled,
			prefixText(sc.PDPool), sc.PDLength, now.Unix(), now.Unix())
		if err != nil {
			return uniqueName(err, sc.Name)
		}
		if sc.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		return rec(ctx, tx, "scope6.create", "scope6", sc.ID, nil, scopeDetails(sc))
	})
	return sc, err
}

// UpdateScope changes a scope; the prefix cannot be changed.
func (s *Store) UpdateScope(ctx context.Context, sc Scope) (Scope, error) {
	if err := sc.validate(); err != nil {
		return sc, err
	}
	err := s.mutate(ctx, func(tx *sql.Tx) error {
		old, err := scanScope(tx.QueryRowContext(ctx, `SELECT `+scopeCols+` FROM scopes6 WHERE id = ?`, sc.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFoundf("IPv6 scope %d", sc.ID)
		}
		if err != nil {
			return err
		}
		if old.Prefix != sc.Prefix {
			var v apperr.Validator
			v.Add("prefix", "the prefix of an existing scope cannot be changed")
			return v.Err()
		}
		if err := checkOverlaps(ctx, tx, &sc); err != nil {
			return err
		}
		sc.CreatedAt, sc.UpdatedAt = old.CreatedAt, s.now().UTC().Truncate(time.Second)
		if _, err := tx.ExecContext(ctx, `UPDATE scopes6 SET name = ?, description = ?, start_ip = ?, end_ip = ?, preferred_lifetime = ?,
			valid_lifetime = ?, rapid_commit = ?, preference = ?, enabled = ?, pd_pool = ?, pd_length = ?, updated_at = ? WHERE id = ?`,
			sc.Name, sc.Description, addrText(sc.Start), addrText(sc.End), int64(sc.PreferredLifetime/time.Second),
			int64(sc.ValidLifetime/time.Second), sc.RapidCommit, int(sc.Preference), sc.Enabled, prefixText(sc.PDPool), sc.PDLength,
			sc.UpdatedAt.Unix(), sc.ID); err != nil {
			return uniqueName(err, sc.Name)
		}
		return rec(ctx, tx, "scope6.update", "scope6", sc.ID, scopeDetails(old), scopeDetails(sc))
	})
	return sc, err
}

// DeleteScope removes a scope with its exclusions, reservations, options
// and leases.
func (s *Store) DeleteScope(ctx context.Context, id int64) error {
	old, err := s.GetScope(ctx, id)
	if err != nil {
		return err
	}
	return s.mutate(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM scopes6 WHERE id = ?`, id); err != nil {
			return err
		}
		return rec(ctx, tx, "scope6.delete", "scope6", id, scopeDetails(old), nil)
	})
}

// ---- exclusions ---------------------------------------------------------------------

// ListExclusions returns the exclusions of a scope (0 = all).
func (s *Store) ListExclusions(ctx context.Context, scopeID int64) ([]Exclusion, error) {
	rows, err := s.db.R.QueryContext(ctx, `SELECT id, scope_id, start_ip, end_ip FROM exclusions6 WHERE ? = 0 OR scope_id = ? ORDER BY scope_id, id`, scopeID, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Exclusion{}
	for rows.Next() {
		var e Exclusion
		var start, end string
		if err := rows.Scan(&e.ID, &e.ScopeID, &start, &end); err != nil {
			return nil, err
		}
		e.Start, e.End = parseAddr(start), parseAddr(end)
		out = append(out, e)
	}
	return out, rows.Err()
}

func exclusionDetails(e Exclusion) map[string]any {
	return map[string]any{"scope_id": e.ScopeID, "start": addrText(e.Start), "end": addrText(e.End)}
}

// AddExclusion excludes an address range of a scope.
func (s *Store) AddExclusion(ctx context.Context, e Exclusion) (Exclusion, error) {
	sc, err := s.GetScope(ctx, e.ScopeID)
	if err != nil {
		return e, err
	}
	var v apperr.Validator
	v.Check(e.Start.IsValid() && sc.Prefix.Contains(e.Start), "start", "must be an address in %s", sc.Prefix)
	v.Check(e.End.IsValid() && sc.Prefix.Contains(e.End), "end", "must be an address in %s", sc.Prefix)
	if e.Start.IsValid() && e.End.IsValid() {
		v.Check(e.Start.Compare(e.End) <= 0, "end", "must not be lower than start")
	}
	if err := v.Err(); err != nil {
		return e, err
	}
	err = s.mutate(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO exclusions6 (scope_id, start_ip, end_ip) VALUES (?, ?, ?)`, e.ScopeID, e.Start.String(), e.End.String())
		if err != nil {
			return err
		}
		if e.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		return rec(ctx, tx, "exclusion6.create", "exclusion6", e.ID, nil, exclusionDetails(e))
	})
	return e, err
}

// DeleteExclusion removes an exclusion of a scope.
func (s *Store) DeleteExclusion(ctx context.Context, scopeID, id int64) error {
	var e Exclusion
	var start, end string
	err := s.db.R.QueryRowContext(ctx, `SELECT id, scope_id, start_ip, end_ip FROM exclusions6 WHERE id = ? AND scope_id = ?`, id, scopeID).
		Scan(&e.ID, &e.ScopeID, &start, &end)
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFoundf("exclusion %d", id)
	}
	if err != nil {
		return err
	}
	e.Start, e.End = parseAddr(start), parseAddr(end)
	return s.mutate(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM exclusions6 WHERE id = ?`, id); err != nil {
			return err
		}
		return rec(ctx, tx, "exclusion6.delete", "exclusion6", id, exclusionDetails(e), nil)
	})
}

// ---- reservations ------------------------------------------------------------------

const resCols = `id, scope_id, duid, iaid, ip, hostname, description, created_at, updated_at`

func scanReservation(r rowScanner) (Reservation, error) {
	var (
		res          Reservation
		ip           string
		created, upd int64
	)
	if err := r.Scan(&res.ID, &res.ScopeID, &res.DUID, &res.IAID, &ip, &res.Hostname, &res.Description, &created, &upd); err != nil {
		return res, err
	}
	res.IP = parseAddr(ip)
	res.CreatedAt, res.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(upd, 0).UTC()
	return res, nil
}

func reservationDetails(r Reservation) map[string]any {
	return map[string]any{"scope_id": r.ScopeID, "duid": r.DUID, "iaid": r.IAID, "ip": addrText(r.IP), "hostname": r.Hostname, "description": r.Description}
}

// ListReservations returns the reservations of a scope (0 = all).
func (s *Store) ListReservations(ctx context.Context, scopeID int64) ([]Reservation, error) {
	rows, err := s.db.R.QueryContext(ctx, `SELECT `+resCols+` FROM reservations6 WHERE ? = 0 OR scope_id = ? ORDER BY scope_id, ip`, scopeID, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Reservation{}
	for rows.Next() {
		r, err := scanReservation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetReservation returns one reservation.
func (s *Store) GetReservation(ctx context.Context, id int64) (Reservation, error) {
	r, err := scanReservation(s.db.R.QueryRowContext(ctx, `SELECT `+resCols+` FROM reservations6 WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, apperr.NotFoundf("IPv6 reservation %d", id)
	}
	return r, err
}

func uniqueReservation(err error, r Reservation) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return apperr.Conflictf("a reservation for %s or for this DUID/IAID already exists in the scope", r.IP)
	}
	return err
}

// CreateReservation adds a reservation.
func (s *Store) CreateReservation(ctx context.Context, r Reservation) (Reservation, error) {
	sc, err := s.GetScope(ctx, r.ScopeID)
	if err != nil {
		return r, err
	}
	if err := r.validate(&sc); err != nil {
		return r, err
	}
	now := s.now().UTC().Truncate(time.Second)
	r.CreatedAt, r.UpdatedAt = now, now
	err = s.mutate(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO reservations6 (scope_id, duid, iaid, ip, hostname, description, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, r.ScopeID, r.DUID, r.IAID, r.IP.String(), r.Hostname, r.Description, now.Unix(), now.Unix())
		if err != nil {
			return uniqueReservation(err, r)
		}
		if r.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		return rec(ctx, tx, "reservation6.create", "reservation6", r.ID, nil, reservationDetails(r))
	})
	return r, err
}

// UpdateReservation changes a reservation (the scope stays).
func (s *Store) UpdateReservation(ctx context.Context, r Reservation) (Reservation, error) {
	old, err := s.GetReservation(ctx, r.ID)
	if err != nil {
		return r, err
	}
	r.ScopeID = old.ScopeID
	sc, err := s.GetScope(ctx, r.ScopeID)
	if err != nil {
		return r, err
	}
	if err := r.validate(&sc); err != nil {
		return r, err
	}
	r.CreatedAt, r.UpdatedAt = old.CreatedAt, s.now().UTC().Truncate(time.Second)
	err = s.mutate(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE reservations6 SET duid = ?, iaid = ?, ip = ?, hostname = ?, description = ?, updated_at = ? WHERE id = ?`,
			r.DUID, r.IAID, r.IP.String(), r.Hostname, r.Description, r.UpdatedAt.Unix(), r.ID); err != nil {
			return uniqueReservation(err, r)
		}
		return rec(ctx, tx, "reservation6.update", "reservation6", r.ID, reservationDetails(old), reservationDetails(r))
	})
	return r, err
}

// DeleteReservation removes a reservation and its options.
func (s *Store) DeleteReservation(ctx context.Context, id int64) error {
	old, err := s.GetReservation(ctx, id)
	if err != nil {
		return err
	}
	return s.mutate(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM reservations6 WHERE id = ?`, id); err != nil {
			return err
		}
		return rec(ctx, tx, "reservation6.delete", "reservation6", id, reservationDetails(old), nil)
	})
}
