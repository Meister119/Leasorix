package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/health"
	"github.com/meister119/leasorix/internal/metrics"
)

func TestServerEndpointsAndShutdown(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewServer("127.0.0.1:0", 2*time.Second, Deps{Log: log, Health: health.NewChecker(), Metrics: metrics.New()})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	addrCtx, addrCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer addrCancel()
	addr, err := s.Addr(addrCtx)
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + addr.String()

	for path, want := range map[string]string{
		"/healthz": `"ok"`,
		"/readyz":  `"ok"`,
		"/metrics": "leasorix_build_info",
	} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), want) {
			t.Errorf("%s: code=%d body=%.200s", path, resp.StatusCode, body)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
}
