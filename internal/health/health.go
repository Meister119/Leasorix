// Package health implements liveness (/healthz) and readiness (/readyz)
// endpoints. Components register named readiness checks; the process is
// ready only when all checks pass.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

// CheckFunc returns nil when the component is ready.
type CheckFunc func(ctx context.Context) error

// Checker aggregates readiness checks.
type Checker struct {
	mu     sync.RWMutex
	checks map[string]CheckFunc
}

// NewChecker returns an empty checker.
func NewChecker() *Checker {
	return &Checker{checks: map[string]CheckFunc{}}
}

// Add registers (or replaces) a named readiness check.
func (c *Checker) Add(name string, fn CheckFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks[name] = fn
}

// Remove unregisters a check.
func (c *Checker) Remove(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.checks, name)
}

// Result is the outcome of a readiness evaluation.
type Result struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// Evaluate runs all checks and reports whether all passed.
func (c *Checker) Evaluate(ctx context.Context) (Result, bool) {
	c.mu.RLock()
	names := make([]string, 0, len(c.checks))
	for n := range c.checks {
		names = append(names, n)
	}
	sort.Strings(names)
	fns := make([]CheckFunc, len(names))
	for i, n := range names {
		fns[i] = c.checks[n]
	}
	c.mu.RUnlock()

	res := Result{Status: "ok", Checks: make(map[string]string, len(names))}
	ok := true
	for i, n := range names {
		if err := fns[i](ctx); err != nil {
			res.Checks[n] = err.Error()
			ok = false
		} else {
			res.Checks[n] = "ok"
		}
	}
	if !ok {
		res.Status = "unavailable"
	}
	return res, ok
}

// LivenessHandler always answers 200 while the process serves HTTP.
func LivenessHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}

// ReadinessHandler answers 200 when all checks pass and 503 otherwise.
func (c *Checker) ReadinessHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		res, ok := c.Evaluate(ctx)
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(res)
	})
}
