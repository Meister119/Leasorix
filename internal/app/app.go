// Package app wires all leasorixd components together and runs them until
// the context is cancelled.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"golang.org/x/sync/errgroup"

	"github.com/meister119/leasorix/internal/api"
	"github.com/meister119/leasorix/internal/buildinfo"
	"github.com/meister119/leasorix/internal/config"
	"github.com/meister119/leasorix/internal/health"
	"github.com/meister119/leasorix/internal/metrics"
	"github.com/meister119/leasorix/internal/store"
)

// App holds the long-lived components of the daemon.
type App struct {
	cfg     *config.Config
	log     *slog.Logger
	db      *store.DB
	health  *health.Checker
	metrics *metrics.Registry
	http    *api.Server
}

// New opens the database, applies migrations and constructs all components.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	log.Info("starting leasorixd", "version", buildinfo.Version, "commit", buildinfo.Commit, "go", buildinfo.GoVersion())

	db, err := store.Open(ctx, filepath.Join(cfg.DataDir, store.FileName))
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx, log); err != nil {
		_ = db.Close()
		return nil, err
	}

	a := &App{
		cfg:     cfg,
		log:     log,
		db:      db,
		health:  health.NewChecker(),
		metrics: metrics.New(),
	}
	a.health.Add("database", db.Ping)
	a.http = api.NewServer(cfg.HTTP.Listen, cfg.HTTP.ShutdownTimeout, api.Deps{
		Log:     log.With("component", "http"),
		Health:  a.health,
		Metrics: a.metrics,
	})
	return a, nil
}

// HTTP exposes the management server (used by tests to discover the port).
func (a *App) HTTP() *api.Server { return a.http }

// Run blocks until ctx is cancelled or a component fails, then shuts all
// components down and closes the database.
func (a *App) Run(ctx context.Context) error {
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return a.http.Run(gctx) })

	err := g.Wait()
	if cerr := a.db.Close(); cerr != nil {
		a.log.Error("closing database", "err", cerr)
	}
	if err != nil {
		return fmt.Errorf("leasorixd: %w", err)
	}
	a.log.Info("leasorixd stopped")
	return nil
}
