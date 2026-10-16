package lease

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/metrics"
	"github.com/meister119/leasorix/internal/store"
)

// ErrClosed is returned when the persistence writer has stopped.
var ErrClosed = errors.New("lease store closed")

// Op is one persistence operation: upsert of a lease, deletion of the
// lease with the given IP, or an audit entry written in the same
// transaction.
type Op struct {
	Delete bool
	Lease  Lease
	Audit  *audit.Entry
}

// AuditOp returns an op that writes an audit entry.
func AuditOp(e audit.Entry) Op { return Op{Audit: &e} }

// Upsert returns an upsert op.
func Upsert(l Lease) Op { return Op{Lease: l} }

// Delete returns a delete op.
func Delete(ip uint32) Op { return Op{Delete: true, Lease: Lease{IP: ip}} }

type writeReq struct {
	ops  []Op
	done chan error
}

// DBStore persists leases. Writes are funnelled through a single goroutine
// that batches concurrent requests into one transaction ("group commit"):
// with synchronous=FULL every commit costs an fsync, batching keeps the
// throughput high under load while every caller still waits until its
// change is durable.
type DBStore struct {
	db      *store.DB
	queue   chan *writeReq
	stopped chan struct{}
	metrics *metrics.Leases
}

// SetMetrics enables commit metrics.
func (s *DBStore) SetMetrics(m *metrics.Leases) { s.metrics = m }

// NewDBStore creates the store. Run must be started for Apply to work.
func NewDBStore(db *store.DB) *DBStore {
	return &DBStore{db: db, queue: make(chan *writeReq), stopped: make(chan struct{})}
}

// LoadAll reads every lease record.
func (s *DBStore) LoadAll(ctx context.Context) ([]Lease, error) {
	rows, err := s.db.R.QueryContext(ctx, `
		SELECT ip, scope_id, mac, client_id, hostname, state, starts_at, expires_at, updated_at, origin FROM leases`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Lease
	for rows.Next() {
		var (
			l                        Lease
			ip                       int64
			state                    string
			starts, expires, updated int64
		)
		if err := rows.Scan(&ip, &l.ScopeID, &l.MAC, &l.ClientID, &l.Hostname, &state, &starts, &expires, &updated, &l.Origin); err != nil {
			return nil, err
		}
		l.IP = uint32(ip)
		l.State = State(state)
		l.Start = time.Unix(starts, 0).UTC()
		l.Expires = time.Unix(expires, 0).UTC()
		l.Updated = time.UnixMilli(updated).UTC()
		out = append(out, l)
	}
	return out, rows.Err()
}

// Apply persists ops and blocks until they are durable.
func (s *DBStore) Apply(ctx context.Context, ops ...Op) error {
	if len(ops) == 0 {
		return nil
	}
	req := &writeReq{ops: ops, done: make(chan error, 1)}
	select {
	case s.queue <- req:
	case <-s.stopped:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	// The writer always answers a request it has received.
	return <-req.done
}

// Run processes writes until ctx is cancelled.
func (s *DBStore) Run(ctx context.Context) error {
	defer close(s.stopped)
	const maxBatch = 512
	for {
		var first *writeReq
		select {
		case first = <-s.queue:
		case <-ctx.Done():
			return nil
		}
		batch := []*writeReq{first}
	drain:
		for len(batch) < maxBatch {
			select {
			case r := <-s.queue:
				batch = append(batch, r)
			default:
				break drain
			}
		}
		if err := s.flush(batch); err == nil || len(batch) == 1 {
			for _, r := range batch {
				r.done <- err
			}
			continue
		}
		// One bad request (e.g. its scope was deleted concurrently) must not
		// fail the others: retry them one by one.
		for _, r := range batch {
			r.done <- s.flush([]*writeReq{r})
		}
	}
}

func (s *DBStore) flush(batch []*writeReq) (err error) {
	start := time.Now()
	defer func() { s.metrics.Commit(time.Since(start), len(batch), err) }()
	// Use a fresh context: a request that has been accepted must be written
	// even if shutdown starts in the meantime.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.db.WriteTx(ctx, func(tx *sql.Tx) error {
		up, err := tx.PrepareContext(ctx, `
			INSERT INTO leases (ip, scope_id, mac, client_id, hostname, state, starts_at, expires_at, updated_at, origin)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (ip) DO UPDATE SET
				scope_id = excluded.scope_id, mac = excluded.mac, client_id = excluded.client_id,
				hostname = excluded.hostname, state = excluded.state, starts_at = excluded.starts_at,
				expires_at = excluded.expires_at, updated_at = excluded.updated_at, origin = excluded.origin`)
		if err != nil {
			return err
		}
		defer up.Close()
		del, err := tx.PrepareContext(ctx, `DELETE FROM leases WHERE ip = ?`)
		if err != nil {
			return err
		}
		defer del.Close()
		for _, r := range batch {
			for _, op := range r.ops {
				if op.Audit != nil {
					if err := audit.Insert(ctx, tx, *op.Audit); err != nil {
						return err
					}
					continue
				}
				l := op.Lease
				if op.Delete {
					if _, err := del.ExecContext(ctx, int64(l.IP)); err != nil {
						return err
					}
					continue
				}
				if _, err := up.ExecContext(ctx, int64(l.IP), l.ScopeID, l.MAC, l.ClientID, l.Hostname, string(l.State),
					l.Start.Unix(), l.Expires.Unix(), l.Updated.UnixMilli(), l.Origin); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
