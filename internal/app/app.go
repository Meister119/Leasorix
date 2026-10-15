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
	"github.com/meister119/leasorix/internal/dhcp4"
	"github.com/meister119/leasorix/internal/health"
	"github.com/meister119/leasorix/internal/lease"
	"github.com/meister119/leasorix/internal/metrics"
	"github.com/meister119/leasorix/internal/scope"
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

	scopes *scope.Store
	leases *lease.Manager
	prober *lease.ICMPProber
	dhcp4  *dhcp4.Server
}

// New opens the database, applies migrations and constructs all components.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	log.Info("starting leasorixd", "version", buildinfo.Version, "commit", buildinfo.Commit, "go", buildinfo.GoVersion())

	db, err := store.Open(ctx, filepath.Join(cfg.DataDir, store.FileName))
	if err != nil {
		return nil, err
	}
	a := &App{
		cfg:     cfg,
		log:     log,
		db:      db,
		health:  health.NewChecker(),
		metrics: metrics.New(),
	}
	if err := a.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return a, nil
}

func (a *App) init(ctx context.Context) error {
	if err := a.db.Migrate(ctx, a.log); err != nil {
		return err
	}
	a.health.Add("database", a.db.Ping)

	a.scopes = scope.NewStore(a.db)
	if err := a.scopes.Load(ctx); err != nil {
		return fmt.Errorf("load scopes: %w", err)
	}

	d := a.cfg.DHCP4
	if d.ConflictDetection.Enabled {
		p, err := lease.NewICMPProber(a.log.With("component", "conflict"), d.ConflictDetection.Timeout, d.ConflictDetection.Attempts)
		if err != nil {
			a.log.Warn("conflict detection disabled: no ICMP socket", "err", err)
		} else {
			a.prober = p
		}
	}
	var prober lease.Prober
	if a.prober != nil {
		prober = a.prober
	}
	a.leases = lease.NewManager(lease.Config{
		OfferTimeout:   d.OfferTimeout,
		DeclineTime:    d.DeclineTime,
		ExpiredGrace:   d.ExpiredGrace,
		ExpiryInterval: d.ExpiryInterval,
	}, a.log.With("component", "lease"), lease.NewDBStore(a.db), prober)
	if err := a.leases.Load(ctx); err != nil {
		return fmt.Errorf("load leases: %w", err)
	}
	a.scopes.OnChange(a.leases.ScopesChanged)

	if len(d.Interfaces) > 0 {
		h := dhcp4.NewHandler(a.log.With("component", "dhcp4"), a.scopes, a.leases)
		a.dhcp4 = dhcp4.NewServer(a.log.With("component", "dhcp4"), h, d.Interfaces, d.Workers)
		a.health.Add("dhcp4", a.dhcp4.Ready)
	} else {
		a.log.Warn("no dhcp4.interfaces configured; DHCPv4 service is disabled")
	}

	a.http = api.NewServer(a.cfg.HTTP.Listen, a.cfg.HTTP.ShutdownTimeout, api.Deps{
		Log:     a.log.With("component", "http"),
		Health:  a.health,
		Metrics: a.metrics,
	})
	return nil
}

// HTTP exposes the management server (used by tests to discover the port).
func (a *App) HTTP() *api.Server { return a.http }

// Scopes exposes the scope store.
func (a *App) Scopes() *scope.Store { return a.scopes }

// Leases exposes the lease manager.
func (a *App) Leases() *lease.Manager { return a.leases }

// Run blocks until ctx is cancelled or a component fails, then shuts all
// components down and closes the database.
func (a *App) Run(ctx context.Context) error {
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return a.http.Run(gctx) })
	g.Go(func() error { return a.leases.Run(gctx) })
	if a.prober != nil {
		g.Go(func() error { return a.prober.Run(gctx) })
	}
	if a.dhcp4 != nil {
		g.Go(func() error { return a.dhcp4.Run(gctx) })
	}

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
