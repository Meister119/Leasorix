// Package audit records who changed what and when: configuration changes
// (written in the same transaction as the change) and lease events (written
// together with the lease in the lease store's group commit).
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/meister119/leasorix/internal/store"
)

// Actors used by the server itself.
const (
	ActorSystem = "system"
	ActorDHCP   = "dhcp"
)

// Entry is one audit record.
type Entry struct {
	ID         int64          `json:"id"`
	Time       time.Time      `json:"time"`
	Actor      string         `json:"actor"`
	Source     string         `json:"source,omitempty"`
	Action     string         `json:"action"`
	ObjectType string         `json:"object_type"`
	ObjectID   string         `json:"object_id"`
	Details    map[string]any `json:"details,omitempty"`
}

type ctxKey struct{}

type principal struct {
	actor  string
	source string
}

// WithActor returns a context carrying the acting user (and optionally the
// source, e.g. the client IP of an API request).
func WithActor(ctx context.Context, actor, source string) context.Context {
	return context.WithValue(ctx, ctxKey{}, principal{actor: actor, source: source})
}

// ActorFrom returns the actor and source stored in ctx; ActorSystem if none.
func ActorFrom(ctx context.Context) (actor, source string) {
	if p, ok := ctx.Value(ctxKey{}).(principal); ok && p.actor != "" {
		return p.actor, p.source
	}
	return ActorSystem, ""
}

// New builds an entry for the actor in ctx.
func New(ctx context.Context, action, objectType, objectID string, details map[string]any) Entry {
	actor, source := ActorFrom(ctx)
	return Entry{Actor: actor, Source: source, Action: action, ObjectType: objectType, ObjectID: objectID, Details: details}
}

// Execer is satisfied by *sql.Tx and *sql.DB.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Insert writes e using x (typically the transaction of the change).
func Insert(ctx context.Context, x Execer, e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	details := e.Details
	if e.Source != "" {
		if details == nil {
			details = map[string]any{}
		}
		details["source"] = e.Source
	}
	b := []byte("{}")
	if len(details) > 0 {
		var err error
		if b, err = json.Marshal(details); err != nil {
			return fmt.Errorf("audit details: %w", err)
		}
	}
	_, err := x.ExecContext(ctx, `INSERT INTO audit_log (ts, actor, action, object_type, object_id, details) VALUES (?, ?, ?, ?, ?, ?)`,
		e.Time.UnixMilli(), e.Actor, e.Action, e.ObjectType, e.ObjectID, string(b))
	return err
}

// Log reads and maintains the audit log.
type Log struct {
	db *store.DB
}

// NewLog creates the reader/maintainer.
func NewLog(db *store.DB) *Log { return &Log{db: db} }

// Record writes a standalone entry.
func (l *Log) Record(ctx context.Context, e Entry) error {
	return l.db.WriteTx(ctx, func(tx *sql.Tx) error { return Insert(ctx, tx, e) })
}

// Filter selects audit entries.
type Filter struct {
	Since      time.Time
	Until      time.Time
	Actor      string
	Action     string // prefix match, e.g. "lease." or "scope.create"
	ObjectType string
	ObjectID   string
	Limit      int   // default 100, max 1000
	BeforeID   int64 // keyset pagination: entries with id < BeforeID
}

// List returns matching entries, newest first.
func (l *Log) List(ctx context.Context, f Filter) ([]Entry, error) {
	var (
		where []string
		args  []any
	)
	if !f.Since.IsZero() {
		where, args = append(where, "ts >= ?"), append(args, f.Since.UnixMilli())
	}
	if !f.Until.IsZero() {
		where, args = append(where, "ts < ?"), append(args, f.Until.UnixMilli())
	}
	if f.Actor != "" {
		where, args = append(where, "actor = ?"), append(args, f.Actor)
	}
	if f.Action != "" {
		where, args = append(where, "action LIKE ? ESCAPE '\\'"), append(args, escapeLike(f.Action)+"%")
	}
	if f.ObjectType != "" {
		where, args = append(where, "object_type = ?"), append(args, f.ObjectType)
	}
	if f.ObjectID != "" {
		where, args = append(where, "object_id = ?"), append(args, f.ObjectID)
	}
	if f.BeforeID > 0 {
		where, args = append(where, "id < ?"), append(args, f.BeforeID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	q := `SELECT id, ts, actor, action, object_type, object_id, details FROM audit_log`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ") //nolint:gosec // only constant conditions, values are bound parameters
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := l.db.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var (
			e       Entry
			ts      int64
			details string
		)
		if err := rows.Scan(&e.ID, &ts, &e.Actor, &e.Action, &e.ObjectType, &e.ObjectID, &details); err != nil {
			return nil, err
		}
		e.Time = time.UnixMilli(ts).UTC()
		if details != "" && details != "{}" {
			_ = json.Unmarshal([]byte(details), &e.Details)
			if src, ok := e.Details["source"].(string); ok {
				e.Source = src
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Purge deletes entries older than before, in batches. It returns the
// number of deleted entries.
func (l *Log) Purge(ctx context.Context, before time.Time) (int64, error) {
	var total int64
	for {
		var n int64
		err := l.db.WriteTx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `DELETE FROM audit_log WHERE id IN (SELECT id FROM audit_log WHERE ts < ? LIMIT 10000)`, before.UnixMilli())
			if err != nil {
				return err
			}
			n, err = res.RowsAffected()
			return err
		})
		if err != nil {
			return total, err
		}
		total += n
		if n < 10000 {
			return total, nil
		}
	}
}

// Count returns the number of entries (used for metrics and tests).
func (l *Log) Count(ctx context.Context) (int64, error) {
	var n int64
	err := l.db.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&n)
	return n, err
}
