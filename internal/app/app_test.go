package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/config"
)

func TestAppStartsAndStops(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.HTTP.Listen = "127.0.0.1:0"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := New(ctx, &cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	addrCtx, addrCancel := context.WithTimeout(ctx, 5*time.Second)
	defer addrCancel()
	addr, err := a.HTTP().Addr(addrCtx)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + addr.String() + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz = %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("app did not stop")
	}

	// A second start against the same data dir must succeed (idempotent migrations).
	ctx2, cancel2 := context.WithCancel(context.Background())
	a2, err := New(ctx2, &cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	cancel2()
	if err := a2.Run(ctx2); err != nil {
		t.Fatal(err)
	}
}
