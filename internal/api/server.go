// Package api contains the HTTP management server: REST API (generated from
// openapi.yaml), embedded web UI, health and metrics endpoints.
package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/meister119/leasorix/internal/api/gen"
	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/auth"
	"github.com/meister119/leasorix/internal/ha"
	"github.com/meister119/leasorix/internal/health"
	"github.com/meister119/leasorix/internal/lease"
	"github.com/meister119/leasorix/internal/metrics"
	"github.com/meister119/leasorix/internal/scope"
	"github.com/meister119/leasorix/internal/store"
)

// SystemInfo is static runtime information shown in the UI.
type SystemInfo struct {
	StartedAt         time.Time
	Interfaces        []string
	ConflictDetection bool
}

// HA is the part of the high availability node used by the API.
type HA interface {
	Status() ha.Status
	PartnerDown(ctx context.Context) error
	ConfigReadOnly() bool
}

// Deps are the collaborators of the HTTP server. Only Log, Health and
// Metrics are mandatory; without Auth the REST API is not mounted.
type Deps struct {
	Log     *slog.Logger
	Health  *health.Checker
	Metrics *metrics.Registry

	Auth *auth.Service
	// DB is needed for configuration import and backup/restore.
	DB     *store.DB
	Scopes *scope.Store
	Leases *lease.Manager
	Audit  *audit.Log
	Info   SystemInfo
	// HA is the high availability node (nil = not configured).
	HA HA
	// UI serves the embedded web interface (nil = placeholder page).
	UI http.Handler
	// TLS enables HTTPS when set.
	TLS *tls.Config
	// Login rate limit.
	LoginMaxFailures int
	LoginWindow      time.Duration
}

// Server is the management HTTP server.
type Server struct {
	deps            Deps
	listen          string
	shutdownTimeout time.Duration
	router          chi.Router
	addr            chan net.Addr
	ipLimiter       *auth.Limiter
	userLimiter     *auth.Limiter
}

// NewServer builds the router. Call Run to start serving.
func NewServer(listen string, shutdownTimeout time.Duration, deps Deps) *Server {
	if deps.LoginMaxFailures <= 0 {
		deps.LoginMaxFailures = 10
	}
	if deps.LoginWindow <= 0 {
		deps.LoginWindow = 15 * time.Minute
	}
	s := &Server{
		deps:            deps,
		listen:          listen,
		shutdownTimeout: shutdownTimeout,
		addr:            make(chan net.Addr, 1),
		ipLimiter:       auth.NewLimiter(deps.LoginMaxFailures, deps.LoginWindow),
		userLimiter:     auth.NewLimiter(deps.LoginMaxFailures, deps.LoginWindow),
	}
	s.router = s.routes()
	return s
}

func (s *Server) routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders(s.deps.TLS != nil))

	r.Method(http.MethodGet, "/healthz", health.LivenessHandler())
	r.Method(http.MethodGet, "/readyz", s.deps.Health.ReadinessHandler())
	r.Method(http.MethodGet, "/metrics", s.deps.Metrics.Handler())

	if s.deps.Auth != nil {
		r.Route("/api/v1", func(api chi.Router) {
			api.Use(noStore)
			api.Use(s.limitBody)
			api.Use(s.authenticate)
			api.Use(s.authorize)
			api.Use(s.configWritable)
			api.NotFound(func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, http.StatusNotFound, "no such endpoint", nil)
			})
			api.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed", nil)
			})
			gen.HandlerWithOptions(&handlers{s: s}, gen.ChiServerOptions{
				BaseRouter: api,
				ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
					writeError(w, http.StatusBadRequest, err.Error(), nil)
				},
			})
		})
	}
	ui := s.deps.UI
	if ui == nil {
		ui = placeholderUI()
	}
	r.Handle("/*", ui)
	return r
}

// httpErrorWriter routes net/http server errors to slog. TLS handshake
// failures (browsers rejecting the self-signed certificate on first
// contact, port scanners) are routine and logged at debug level.
type httpErrorWriter struct{ log *slog.Logger }

func (w httpErrorWriter) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	if strings.Contains(msg, "TLS handshake error") {
		w.log.Debug(msg)
	} else {
		w.log.Warn(msg)
	}
	return len(p), nil
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
	if s.deps.TLS != nil {
		ln = tls.NewListener(ln, s.deps.TLS)
	}
	srv := &http.Server{
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          log.New(httpErrorWriter{s.deps.Log}, "", 0),
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	s.deps.Log.Info("management http server listening", "addr", ln.Addr().String(), "tls", s.deps.TLS != nil)

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

func placeholderUI() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>Leasorix</title></head>
<body><h1>Leasorix</h1><p>The web UI is not part of this build. Build with <code>make build</code>
(which runs <code>make ui</code> and the <code>ui</code> build tag). The REST API is available under <code>/api/v1</code>.</p></body></html>`))
	})
}
