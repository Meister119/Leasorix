package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/meister119/leasorix/internal/config"
	"github.com/meister119/leasorix/internal/lease"
	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/store"
)

// ExportLeases writes the lease table as JSON (lease.ExportFile). It reads
// the database directly and works while the daemon is running.
func ExportLeases(ctx context.Context, cfg *config.Config, w io.Writer) (int, error) {
	db, err := store.Open(ctx, filepath.Join(cfg.DataDir, store.FileName))
	if err != nil {
		return 0, err
	}
	defer db.Close()
	all, err := lease.NewDBStore(db).LoadAll(ctx)
	if err != nil {
		return 0, fmt.Errorf("read leases: %w", err)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return len(all), enc.Encode(lease.NewExport(cfg.HA.NodeID, all, time.Now()))
}

// ImportLeases merges a lease export into the database. The daemon must be
// stopped (the data directory lock is taken); on a running server use the
// web UI or POST /api/v1/leases/import instead.
func ImportLeases(ctx context.Context, cfg *config.Config, log *slog.Logger, r io.Reader) (records, changed, conflicts int, err error) {
	f, err := lease.ReadExport(r)
	if err != nil {
		return 0, 0, 0, err
	}
	recs, err := f.Records()
	if err != nil {
		return 0, 0, 0, err
	}
	lock, err := store.LockDataDir(cfg.DataDir)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("%w (stop leasorixd or import through the web UI / API)", err)
	}
	defer func() { _ = lock.Release() }()
	db, err := store.Open(ctx, filepath.Join(cfg.DataDir, store.FileName))
	if err != nil {
		return 0, 0, 0, err
	}
	defer db.Close()
	if err := db.Migrate(ctx, log); err != nil {
		return 0, 0, 0, err
	}
	scopes := scope.NewStore(db)
	if err := scopes.Load(ctx); err != nil {
		return 0, 0, 0, err
	}
	m := lease.NewManager(lease.Config{ExpiryInterval: time.Hour, AuditEvents: cfg.Audit.LeaseEvents, NodeID: cfg.HA.NodeID}, log, lease.NewDBStore(db), nil)
	if err := m.Load(ctx); err != nil {
		return 0, 0, 0, err
	}
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- m.Run(rctx) }()
	changed, conflicts, err = m.Import(ctx, scopes.Snapshot(), recs)
	cancel()
	if rerr := <-done; err == nil {
		err = rerr
	}
	return len(recs), changed, conflicts, err
}
