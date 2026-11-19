// Package app wires all leasorixd components together and runs them until
// the context is cancelled.
package app

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/meister119/leasorix/internal/api"
	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/auth"
	"github.com/meister119/leasorix/internal/buildinfo"
	"github.com/meister119/leasorix/internal/config"
	"github.com/meister119/leasorix/internal/dhcp4"
	"github.com/meister119/leasorix/internal/dhcp6"
	"github.com/meister119/leasorix/internal/dnsupdate"
	"github.com/meister119/leasorix/internal/ha"
	"github.com/meister119/leasorix/internal/health"
	"github.com/meister119/leasorix/internal/lease"
	"github.com/meister119/leasorix/internal/lease6"
	"github.com/meister119/leasorix/internal/metrics"
	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/scope6"
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
	audit  *audit.Log
	auth   *auth.Service
	ha     *ha.Node
	dns    *dnsupdate.Updater

	scopes6 *scope6.Store
	duid6   string
	leases6 *lease6.Manager
	dhcp6   *dhcp6.Server
	lock    *store.Lock
	// UI is the embedded web UI handler (set by main when built with it).
	ui http.Handler
}

// Option customises the App.
type Option func(*App)

// WithUI serves the given handler as web UI.
func WithUI(h http.Handler) Option { return func(a *App) { a.ui = h } }

// New opens the database, applies migrations and constructs all components.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger, opts ...Option) (*App, error) {
	log.Info("starting leasorixd", "version", buildinfo.Version, "commit", buildinfo.Commit, "go", buildinfo.GoVersion())

	lock, err := store.LockDataDir(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(ctx, filepath.Join(cfg.DataDir, store.FileName))
	if err != nil {
		_ = lock.Release()
		return nil, err
	}
	a := &App{
		cfg:     cfg,
		log:     log,
		db:      db,
		lock:    lock,
		health:  health.NewChecker(),
		metrics: metrics.New(),
	}
	for _, o := range opts {
		o(a)
	}
	if err := a.init(ctx); err != nil {
		_ = db.Close()
		_ = lock.Release()
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
		AuditEvents:    a.cfg.Audit.LeaseEvents,
		AuditRenewals:  a.cfg.Audit.LeaseRenewals,
		NodeID:         a.cfg.HA.NodeID,
	}, a.log.With("component", "lease"), lease.NewDBStore(a.db), prober)
	a.leases.SetMetrics(a.metrics.NewLeases())
	if err := a.leases.Load(ctx); err != nil {
		return fmt.Errorf("load leases: %w", err)
	}
	a.scopes.OnChange(a.leases.ScopesChanged)
	a.metrics.RegisterScopeCollector(a.scopeSamples)
	a.audit = audit.NewLog(a.db)

	d6 := a.cfg.DHCP6
	a.scopes6 = scope6.NewStore(a.db)
	if err := a.scopes6.Load(ctx); err != nil {
		return fmt.Errorf("load IPv6 scopes: %w", err)
	}
	a.leases6 = lease6.NewManager(lease6.Config{
		OfferTimeout: d6.OfferTimeout, DeclineTime: d6.DeclineTime, ExpiredGrace: d6.ExpiredGrace,
		ExpiryInterval: d6.ExpiryInterval, AuditEvents: a.cfg.Audit.LeaseEvents,
	}, a.log.With("component", "lease6"), a.db)
	if err := a.leases6.Load(ctx); err != nil {
		return fmt.Errorf("load IPv6 leases: %w", err)
	}
	a.scopes6.OnChange(a.leases6.ScopesChanged)

	if a.cfg.HA.Enabled() {
		n, err := ha.NewNode(ctx, ha.Options{
			Config: a.cfg.HA, Log: a.log.With("component", "ha"), DB: a.db, Leases: a.leases, Scopes: a.scopes,
			Reload: a.scopes6.Load, Metrics: a.metrics.NewHA(), Version: buildinfo.Version,
		})
		if err != nil {
			return fmt.Errorf("high availability: %w", err)
		}
		a.ha = n
		a.leases.SetCoordinator(n)
		a.leases6.SetCoordinator(n)
		a.scopes.OnChange(func(*scope.Snapshot) { n.Kick() })
		a.scopes6.OnChange(func(*scope6.Snapshot) { n.Kick() })
	}

	a.dns = dnsupdate.NewUpdater(a.log.With("component", "dns"), a.db, a.scopes, a.leases, dnsupdate.NewClient())
	a.dns.SetMetrics(a.metrics.NewDNS())

	if len(d.Interfaces) > 0 {
		dm := a.metrics.NewDHCP4()
		h := dhcp4.NewHandler(a.log.With("component", "dhcp4"), a.scopes, a.leases)
		h.SetMetrics(dm)
		h.SetBOOTPLeaseTime(d.BOOTPLeaseTime)
		if a.ha != nil {
			h.SetPartner(a.ha)
		}
		h.SetDNS(a.dns)
		a.dhcp4 = dhcp4.NewServer(a.log.With("component", "dhcp4"), h, d.Interfaces, d.Workers)
		a.dhcp4.SetMetrics(dm)
		a.health.Add("dhcp4", a.dhcp4.Ready)
	} else {
		a.log.Warn("no dhcp4.interfaces configured; DHCPv4 service is disabled")
	}

	if len(d6.Interfaces) > 0 {
		duid, err := dhcp6.ServerDUID(ctx, a.db, d6.Interfaces, time.Now())
		if err != nil {
			return fmt.Errorf("DHCPv6 server DUID: %w", err)
		}
		dm6 := a.metrics.NewDHCP6()
		a.leases6.SetEvents(dm6)
		h6 := dhcp6.NewHandler(a.log.With("component", "dhcp6"), a.scopes6, a.leases6, duid)
		h6.SetMetrics(dm6)
		h6.SetFilter(func(mac string) (bool, string) { return a.scopes.Snapshot().Filtered(mac) })
		if a.ha != nil {
			h6.SetPartner(a.ha)
		}
		a.dhcp6 = dhcp6.NewServer(a.log.With("component", "dhcp6"), h6, d6.Interfaces, d6.Workers)
		a.dhcp6.SetMetrics(dm6)
		a.health.Add("dhcp6", a.dhcp6.Ready)
		a.duid6 = hex.EncodeToString(duid.ToBytes())
		a.log.Info("DHCPv6 enabled", "server_duid", a.duid6)
	}

	a.auth = auth.NewService(a.db, auth.Config{
		SessionTTL:  a.cfg.Auth.SessionTTL,
		IdleTimeout: a.cfg.Auth.IdleTimeout,
		SetupToken:  a.cfg.Auth.SetupToken,
	})
	if required, err := a.auth.SetupRequired(ctx); err != nil {
		return err
	} else if required {
		a.log.Warn("no users exist yet: open the web UI and create the first admin with this one-time setup token",
			"setup_token", a.auth.SetupToken())
	}
	tlsCfg, err := api.TLSConfig(a.cfg.HTTP.TLS, a.cfg.DataDir, a.cfg.HTTP.Listen)
	if err != nil {
		return err
	}
	var haDep api.HA
	if a.ha != nil {
		haDep = a.ha
	}
	a.http = api.NewServer(a.cfg.HTTP.Listen, a.cfg.HTTP.ShutdownTimeout, api.Deps{
		Log:     a.log.With("component", "http"),
		Health:  a.health,
		Metrics: a.metrics,
		Auth:    a.auth,
		DB:      a.db,
		Scopes:  a.scopes,
		Leases:  a.leases,
		Audit:   a.audit,
		Info: api.SystemInfo{
			StartedAt:         time.Now(),
			Interfaces:        d.Interfaces,
			ConflictDetection: a.prober != nil,
			Interfaces6:       d6.Interfaces,
			ServerDUID6:       a.duid6,
		},
		HA:               haDep,
		DNS:              a.dns,
		Scopes6:          a.scopes6,
		Leases6:          a.leases6,
		UI:               a.ui,
		TLS:              tlsCfg,
		LoginMaxFailures: a.cfg.Auth.LoginMaxFailures,
		LoginWindow:      a.cfg.Auth.LoginWindow,
		MetricsAuth:      a.cfg.HTTP.MetricsAuth,
	})
	return nil
}

// Auth exposes the auth service.
func (a *App) Auth() *auth.Service { return a.auth }

func (a *App) scopeSamples() []metrics.ScopeSample {
	stats := a.leases.Stats(a.scopes.Snapshot())
	out := make([]metrics.ScopeSample, len(stats))
	for i, st := range stats {
		by := make(map[string]uint64, len(st.ByState))
		for k, v := range st.ByState {
			by[string(k)] = v
		}
		out[i] = metrics.ScopeSample{
			Scope: st.Name, Subnet: st.Subnet, Usable: st.Usable, InUse: st.InUse, Reserved: st.Reserved,
			Declined: st.Declined, Available: st.Available, Utilization: st.Utilization / 100, ByState: by,
		}
	}
	return out
}

// purgeAudit deletes audit entries older than the retention period, at
// startup and every 6 hours.
func (a *App) purgeAudit(ctx context.Context) error {
	if a.cfg.Audit.Retention <= 0 {
		return nil
	}
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		n, err := a.audit.Purge(ctx, time.Now().Add(-a.cfg.Audit.Retention))
		switch {
		case err != nil && ctx.Err() == nil:
			a.log.Error("purging audit log", "err", err)
		case n > 0:
			a.log.Info("purged old audit entries", "count", n, "retention", a.cfg.Audit.Retention.String())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Audit exposes the audit log.
func (a *App) Audit() *audit.Log { return a.audit }

// HTTP exposes the management server (used by tests to discover the port).
func (a *App) HTTP() *api.Server { return a.http }

// HA exposes the high availability node (nil without HA).
func (a *App) HA() *ha.Node { return a.ha }

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
	g.Go(func() error { return a.purgeAudit(gctx) })
	g.Go(func() error { return a.dns.Run(gctx) })
	g.Go(func() error { return a.leases6.Run(gctx) })
	if a.dhcp6 != nil {
		g.Go(func() error { return a.dhcp6.Run(gctx) })
	}
	if a.prober != nil {
		g.Go(func() error { return a.prober.Run(gctx) })
	}
	if a.dhcp4 != nil {
		g.Go(func() error { return a.dhcp4.Run(gctx) })
	}
	if a.ha != nil {
		g.Go(func() error { return a.ha.Run(gctx) })
	}

	err := g.Wait()
	if cerr := a.db.Close(); cerr != nil {
		a.log.Error("closing database", "err", cerr)
	}
	if lerr := a.lock.Release(); lerr != nil {
		a.log.Error("releasing data directory lock", "err", lerr)
	}
	if err != nil {
		return fmt.Errorf("leasorixd: %w", err)
	}
	a.log.Info("leasorixd stopped")
	return nil
}
