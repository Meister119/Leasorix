// Package api contains the HTTP management server: REST API, embedded web UI,
// health and metrics endpoints.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/meister119/leasorix/internal/health"
	"github.com/meister119/leasorix/internal/metrics"
)

// Deps are the collaborators of the HTTP server.
type Deps struct {
	Log     *slog.Logger
	Health  *health.Checker
	Metrics *metrics.Registry
}

// Server is the management HTTP server.
type Server struct {
	deps            Deps
	listen          string
	shutdownTimeout time.Duration
	router          chi.Router
	addr            chan net.Addr
}

// NewServer builds the router. Call Run to start serving.
func NewServer(listen string, shutdownTimeout time.Duration, deps Deps) *Server {
	s := &Server{
		deps:            deps,
		listen:          listen,
		shutdownTimeout: shutdownTimeout,
		addr:            make(chan net.Addr, 1),
	}
	s.router = s.routes()
	return s
}

func (s *Server) routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	r.Method(http.MethodGet, "/healthz", health.LivenessHandler())
	r.Method(http.MethodGet, "/readyz", s.deps.Health.ReadinessHandler())
	r.Method(http.MethodGet, "/metrics", s.deps.Metrics.Handler())
	return r
}

// Handler returns the root handler (used by tests).
func (s *Server) Handler() http.Handler { return s.router }

// Addr blocks until the listener is bound and returns its address. Useful
// when listening on port 0.
func (s *Server) Addr(ctx context.Context) (net.Addr, error) {
	select {
	case a := <-s.addr:
		s.addr <- a
		return a, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.listen)
	if err != nil {
		return fmt.Errorf("http listen %s: %w", s.listen, err)
	}
	s.addr <- ln.Addr()
	srv := &http.Server{
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(s.deps.Log.Handler(), slog.LevelWarn),
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	s.deps.Log.Info("management http server listening", "addr", ln.Addr().String())

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()
	s.deps.Log.Info("shutting down management http server")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
