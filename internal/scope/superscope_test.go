package scope

import (
	"context"
	"errors"
	"testing"

	"github.com/meister119/leasorix/internal/apperr"
)

func TestSuperscopes(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := mustScope(t, s, "a", "10.40.0.0/24", "10.40.0.10", "10.40.0.20")
	b := mustScope(t, s, "b", "10.40.1.0/24", "10.40.1.10", "10.40.1.20")
	ss, err := s.CreateSuperscope(ctx, Superscope{Name: "Gebäude 1", Description: "multinet"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSuperscope(ctx, Superscope{Name: "gebäude 1"}); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := s.CreateSuperscope(ctx, Superscope{Name: " "}); err == nil {
		t.Fatal("empty name accepted")
	}
	for _, sc := range []Scope{a, b} {
		sc.SuperscopeID = ss.ID
		if _, err := s.UpdateScope(ctx, sc); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetSuperscope(ctx, ss.ID)
	if err != nil || len(got.ScopeIDs) != 2 || got.ScopeIDs[0] != a.ID {
		t.Fatalf("members %+v %v", got, err)
	}
	if pools := s.Snapshot().SuperscopePools(ss.ID); len(pools) != 2 || pools[1].Name != "b" {
		t.Fatalf("snapshot members %v", pools)
	}
	if sc, _ := s.GetScope(ctx, a.ID); sc.SuperscopeID != ss.ID {
		t.Fatal("membership not reported on the scope")
	}
	bad := a
	bad.SuperscopeID = 999
	if _, err := s.UpdateScope(ctx, bad); err == nil {
		t.Fatal("unknown superscope accepted")
	}
	// Deleting a member scope removes it from the superscope.
	if err := s.DeleteScope(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSuperscope(ctx, ss.ID); len(got.ScopeIDs) != 1 {
		t.Fatalf("members after scope delete: %v", got.ScopeIDs)
	}
	// Deleting the superscope keeps the scopes.
	if err := s.DeleteSuperscope(ctx, ss.ID); err != nil {
		t.Fatal(err)
	}
	if sc, err := s.GetScope(ctx, a.ID); err != nil || sc.SuperscopeID != 0 {
		t.Fatalf("scope after superscope delete: %+v %v", sc, err)
	}
	if err := s.DeleteSuperscope(ctx, ss.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}
