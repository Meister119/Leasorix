package scope

import (
	"context"
	"testing"
	"time"

	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/dhcpopt"
)

// TestEveryMutationIsAudited runs every mutating operation of the store and
// checks that each one writes exactly one audit entry with the expected
// action and the actor from the context, and that rejected operations
// write none.
func TestEveryMutationIsAudited(t *testing.T) {
	s := newStore(t)
	log := audit.NewLog(s.db)
	ctx := audit.WithActor(context.Background(), "alice", "192.0.2.10")

	var (
		sc  Scope
		ex  Exclusion
		res Reservation
		cl  Class
		pol Policy
		flt MACFilter
	)
	steps := []struct {
		action string
		run    func() error
	}{
		{"scope.create", func() (err error) {
			sc, err = s.CreateScope(ctx, lan("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.200"))
			return
		}},
		{"scope.update", func() error { sc.Description = "x"; _, err := s.UpdateScope(ctx, sc); return err }},
		{"exclusion.create", func() (err error) {
			ex, err = s.AddExclusion(ctx, Exclusion{ScopeID: sc.ID, Start: addr("10.0.0.10"), End: addr("10.0.0.20")})
			return
		}},
		{"exclusion.delete", func() error { return s.DeleteExclusion(ctx, sc.ID, ex.ID) }},
		{"reservation.create", func() (err error) {
			res, err = s.CreateReservation(ctx, Reservation{ScopeID: sc.ID, IP: addr("10.0.0.5"), MAC: "02:00:00:00:00:01"})
			return
		}},
		{"reservation.update", func() error { res.Hostname = "h"; _, err := s.UpdateReservation(ctx, res); return err }},
		{"class.create", func() (err error) {
			cl, err = s.CreateClass(ctx, Class{Name: "c", Type: ClassVendor, Data: "x*"})
			return
		}},
		{"class.update", func() error { cl.Data = "y*"; _, err := s.UpdateClass(ctx, cl); return err }},
		{"policy.create", func() (err error) {
			pol, err = s.CreatePolicy(ctx, Policy{ScopeID: sc.ID, Name: "p", Enabled: true, Conditions: []Condition{{Field: FieldClass, Value: "c"}}})
			return
		}},
		{"policy.update", func() error { pol.Enabled = false; _, err := s.UpdatePolicy(ctx, pol); return err }},
		{"option.set", func() error {
			_, err := s.SetOption(ctx, OptionValue{Owner: ServerOwner, Code: 6, Value: "1.1.1.1"})
			return err
		}},
		{"option.set", func() error {
			_, err := s.SetOption(ctx, OptionValue{Owner: ServerOwner, Code: 6, Value: "8.8.8.8"})
			return err
		}},
		{"option.delete", func() error { return s.DeleteOption(ctx, ServerOwner, 6) }},
		{"optiondef.create", func() error {
			_, err := s.CreateOptionDefinition(ctx, dhcpopt.Definition{Code: 224, Name: "x", Type: dhcpopt.TypeString})
			return err
		}},
		{"optiondef.delete", func() error { return s.DeleteOptionDefinition(ctx, 224) }},
		{"filter.create", func() (err error) { flt, err = s.AddFilter(ctx, MACFilter{List: FilterDeny, Pattern: "aa:*"}); return }},
		{"filter.settings", func() error { return s.SetFilterSettings(ctx, FilterSettings{DenyEnabled: true}) }},
		{"filter.delete", func() error { return s.DeleteFilter(ctx, flt.ID) }},
		{"policy.delete", func() error { return s.DeletePolicy(ctx, pol.ID) }},
		{"class.delete", func() error { return s.DeleteClass(ctx, cl.ID) }},
		{"reservation.delete", func() error { return s.DeleteReservation(ctx, res.ID) }},
		{"scope.delete", func() error { return s.DeleteScope(ctx, sc.ID) }},
	}
	for _, st := range steps {
		before, _ := log.Count(ctx)
		if err := st.run(); err != nil {
			t.Fatalf("%s: %v", st.action, err)
		}
		after, _ := log.Count(ctx)
		if after != before+1 {
			t.Fatalf("%s wrote %d audit entries", st.action, after-before)
		}
		latest, err := log.List(ctx, audit.Filter{Limit: 1})
		if err != nil || len(latest) != 1 {
			t.Fatal(err)
		}
		e := latest[0]
		if e.Action != st.action || e.Actor != "alice" || e.Source != "192.0.2.10" || e.ObjectID == "" {
			t.Fatalf("%s: unexpected entry %+v", st.action, e)
		}
		if time.Since(e.Time) > time.Minute {
			t.Fatalf("%s: timestamp %v", st.action, e.Time)
		}
	}

	// before/after are recorded for updates.
	updates, _ := log.List(ctx, audit.Filter{Action: "scope.update"})
	if len(updates) != 1 || updates[0].Details["before"] == nil || updates[0].Details["after"] == nil {
		t.Fatalf("scope.update details: %+v", updates)
	}

	// Rejected operations leave no trace.
	before, _ := log.Count(ctx)
	if _, err := s.CreateScope(ctx, lan("", "10.0.0.0/24", "10.0.0.10", "10.0.0.200")); err == nil {
		t.Fatal("invalid scope accepted")
	}
	if err := s.DeleteScope(ctx, 4242); err == nil {
		t.Fatal("delete of missing scope succeeded")
	}
	if after, _ := log.Count(ctx); after != before {
		t.Fatalf("failed mutations wrote %d entries", after-before)
	}

	// Filters.
	got, _ := log.List(ctx, audit.Filter{Action: "option."})
	if len(got) != 3 {
		t.Fatalf("prefix filter: %d entries", len(got))
	}
	got, _ = log.List(ctx, audit.Filter{ObjectType: "scope", Limit: 2})
	if len(got) != 2 || got[0].ID < got[1].ID {
		t.Fatalf("object type filter / ordering: %+v", got)
	}
	page2, _ := log.List(ctx, audit.Filter{ObjectType: "scope", BeforeID: got[1].ID})
	if len(page2) != 1 || page2[0].Action != "scope.create" {
		t.Fatalf("keyset pagination: %+v", page2)
	}
}

func TestDefaultActorIsSystem(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateScope(context.Background(), lan("lan", "10.0.0.0/24", "10.0.0.10", "10.0.0.200")); err != nil {
		t.Fatal(err)
	}
	got, _ := audit.NewLog(s.db).List(context.Background(), audit.Filter{})
	if len(got) != 1 || got[0].Actor != audit.ActorSystem {
		t.Fatalf("%+v", got)
	}
}
