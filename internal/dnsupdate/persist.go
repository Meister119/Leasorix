package dnsupdate

import (
	"context"
	"database/sql"
	"time"
)

const regCols = `ip, scope_id, client, cleanup, ttl, want_fqdn, want_forward, want_reverse, want_dhcid,
	have_forward, have_dhcid, have_reverse, conflict, attempts, next_attempt, last_error, updated_at`

func msTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func unixMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// load reads the persisted registrations.
func (u *Updater) load(ctx context.Context) error {
	rows, err := u.db.R.QueryContext(ctx, `SELECT `+regCols+` FROM dns_registrations`)
	if err != nil {
		return err
	}
	defer rows.Close()
	regs := map[uint32]*Registration{}
	for rows.Next() {
		var (
			r         Registration
			ip, ttl   int64
			next, upd int64
		)
		if err := rows.Scan(&ip, &r.ScopeID, &r.Client, &r.Cleanup, &ttl, &r.WantFQDN, &r.WantFwd, &r.WantRev, &r.WantDHCID,
			&r.HaveFwd, &r.HaveDHCID, &r.HaveRev, &r.Conflict, &r.Attempts, &next, &r.LastError, &upd); err != nil {
			return err
		}
		r.IP, r.TTL = uint32(ip), uint32(ttl)
		r.NextAttempt, r.Updated = msTime(next), msTime(upd)
		regs[r.IP] = &r
	}
	if err := rows.Err(); err != nil {
		return err
	}
	u.mu.Lock()
	u.regs = regs
	u.mu.Unlock()
	return nil
}

// persist writes changed registrations and deletes dropped ones.
func (u *Updater) persist(ctx context.Context) {
	u.mu.Lock()
	var rows []Registration
	for _, r := range u.regs {
		if r.dirty {
			rows = append(rows, *r)
			r.dirty = false
		}
	}
	del := u.deleted
	u.deleted = nil
	u.mu.Unlock()
	if len(rows) == 0 && len(del) == 0 {
		return
	}
	err := u.db.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, ip := range del {
			if _, err := tx.ExecContext(ctx, `DELETE FROM dns_registrations WHERE ip = ?`, int64(ip)); err != nil {
				return err
			}
		}
		for _, r := range rows {
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO dns_registrations (`+regCols+`)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				int64(r.IP), r.ScopeID, r.Client, r.Cleanup, int64(r.TTL), r.WantFQDN, r.WantFwd, r.WantRev, r.WantDHCID,
				r.HaveFwd, r.HaveDHCID, r.HaveRev, r.Conflict, r.Attempts, unixMS(r.NextAttempt), r.LastError, unixMS(r.Updated)); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil || ctx.Err() != nil {
		return
	}
	u.log.Error("persisting DNS registrations", "err", err)
	u.mu.Lock()
	for _, r := range rows {
		if cur := u.regs[r.IP]; cur != nil {
			cur.dirty = true
		}
	}
	u.deleted = append(u.deleted, del...)
	u.mu.Unlock()
}
