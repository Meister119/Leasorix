package configio

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/dhcpopt6"
	"github.com/meister119/leasorix/internal/scope6"
)

// Option6 is a DHCPv6 option value. Type is only needed for codes that are
// not well known.
type Option6 struct {
	Code  uint16 `json:"code" yaml:"code"`
	Type  string `json:"type,omitempty" yaml:"type,omitempty"`
	Value string `json:"value" yaml:"value"`
}

// Range6 is an inclusive IPv6 address range.
type Range6 struct {
	Start string `json:"start" yaml:"start"`
	End   string `json:"end" yaml:"end"`
}

// Reservation6 is a DHCPv6 reservation.
type Reservation6 struct {
	DUID string `json:"duid" yaml:"duid"`
	// IAID restricts the reservation to one IA (absent = any).
	IAID        *int64    `json:"iaid,omitempty" yaml:"iaid,omitempty"`
	IP          string    `json:"ip" yaml:"ip"`
	Hostname    string    `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	Description string    `json:"description,omitempty" yaml:"description,omitempty"`
	Options     []Option6 `json:"options,omitempty" yaml:"options,omitempty"`
}

// Scope6 is a DHCPv6 scope with everything below it.
type Scope6 struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Prefix      string `json:"prefix" yaml:"prefix"`
	Start       string `json:"start,omitempty" yaml:"start,omitempty"`
	End         string `json:"end,omitempty" yaml:"end,omitempty"`
	// Lifetimes in seconds.
	PreferredLifetime int64          `json:"preferred_lifetime" yaml:"preferred_lifetime"`
	ValidLifetime     int64          `json:"valid_lifetime" yaml:"valid_lifetime"`
	RapidCommit       bool           `json:"rapid_commit,omitempty" yaml:"rapid_commit,omitempty"`
	Preference        uint8          `json:"preference,omitempty" yaml:"preference,omitempty"`
	Enabled           bool           `json:"enabled" yaml:"enabled"`
	PDPool            string         `json:"pd_pool,omitempty" yaml:"pd_pool,omitempty"`
	PDLength          int            `json:"pd_length,omitempty" yaml:"pd_length,omitempty"`
	Exclusions        []Range6       `json:"exclusions,omitempty" yaml:"exclusions,omitempty"`
	Reservations      []Reservation6 `json:"reservations,omitempty" yaml:"reservations,omitempty"`
	Options           []Option6      `json:"options,omitempty" yaml:"options,omitempty"`
}

func exportOptions6(ctx context.Context, s *scope6.Store, o scope6.Owner) ([]Option6, error) {
	ovs, err := s.ListOptions(ctx, o)
	if err != nil {
		return nil, err
	}
	var out []Option6
	for _, ov := range ovs {
		opt := Option6{Code: ov.Code, Value: ov.Value}
		if _, builtin := dhcpopt6.Builtin(ov.Code); !builtin {
			opt.Type = string(ov.Type)
		}
		out = append(out, opt)
	}
	return out, nil
}

func addrString(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

// export6 adds the DHCPv6 configuration to the document.
func export6(ctx context.Context, s *scope6.Store, d *Document) error {
	var err error
	if d.ServerOptions6, err = exportOptions6(ctx, s, scope6.ServerOwner); err != nil {
		return err
	}
	scopes, err := s.ListScopes(ctx)
	if err != nil {
		return err
	}
	for _, sc := range scopes {
		b := Scope6{
			Name: sc.Name, Description: sc.Description, Prefix: sc.Prefix.String(), Start: addrString(sc.Start), End: addrString(sc.End),
			PreferredLifetime: int64(sc.PreferredLifetime / time.Second), ValidLifetime: int64(sc.ValidLifetime / time.Second),
			RapidCommit: sc.RapidCommit, Preference: sc.Preference, Enabled: sc.Enabled, PDLength: sc.PDLength,
		}
		if sc.PDPool.IsValid() {
			b.PDPool = sc.PDPool.String()
		}
		excl, err := s.ListExclusions(ctx, sc.ID)
		if err != nil {
			return err
		}
		for _, e := range excl {
			b.Exclusions = append(b.Exclusions, Range6{e.Start.String(), e.End.String()})
		}
		res, err := s.ListReservations(ctx, sc.ID)
		if err != nil {
			return err
		}
		for _, r := range res {
			br := Reservation6{DUID: r.DUID, IP: r.IP.String(), Hostname: r.Hostname, Description: r.Description}
			if r.IAID != scope6.AnyIAID {
				id := r.IAID
				br.IAID = &id
			}
			if br.Options, err = exportOptions6(ctx, s, scope6.Owner{Kind: scope6.OwnerReservation, ID: r.ID}); err != nil {
				return err
			}
			b.Reservations = append(b.Reservations, br)
		}
		if b.Options, err = exportOptions6(ctx, s, scope6.Owner{Kind: scope6.OwnerScope, ID: sc.ID}); err != nil {
			return err
		}
		d.Scopes6 = append(d.Scopes6, b)
	}
	return nil
}

func parse6(what, field, s string, prefix bool) (netip.Addr, netip.Prefix, error) {
	if s == "" {
		return netip.Addr{}, netip.Prefix{}, nil
	}
	var v apperr.Validator
	if prefix {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			v.Add("document", "%s: %s %q is not an IPv6 prefix", what, field, s)
			return netip.Addr{}, netip.Prefix{}, v.Err()
		}
		return netip.Addr{}, p, nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		v.Add("document", "%s: %s %q is not an IPv6 address", what, field, s)
		return netip.Addr{}, netip.Prefix{}, v.Err()
	}
	return a, netip.Prefix{}, nil
}

func setOptions6(ctx context.Context, s *scope6.Store, o scope6.Owner, opts []Option6, wrap func(string, error) error, what string) (int, error) {
	for _, opt := range opts {
		if _, err := s.SetOption(ctx, scope6.OptionValue{Owner: o, Code: opt.Code, Type: dhcpopt6.Type(opt.Type), Value: opt.Value}); err != nil {
			return 0, wrap(fmt.Sprintf("%s, option %d", what, opt.Code), err)
		}
	}
	return len(opts), nil
}

// apply6 creates the DHCPv6 part of the document.
func apply6(ctx context.Context, s *scope6.Store, d *Document, sum *Summary, wrap func(string, error) error) error {
	n, err := setOptions6(ctx, s, scope6.ServerOwner, d.ServerOptions6, wrap, "IPv6 server options")
	if err != nil {
		return err
	}
	sum.Options += n
	for _, b := range d.Scopes6 {
		what := fmt.Sprintf("IPv6 scope %q", b.Name)
		_, prefix, err := parse6(what, "prefix", b.Prefix, true)
		if err != nil {
			return err
		}
		start, _, err := parse6(what, "start", b.Start, false)
		if err != nil {
			return err
		}
		end, _, err := parse6(what, "end", b.End, false)
		if err != nil {
			return err
		}
		_, pool, err := parse6(what, "pd_pool", b.PDPool, true)
		if err != nil {
			return err
		}
		sc, err := s.CreateScope(ctx, scope6.Scope{
			Name: b.Name, Description: b.Description, Prefix: prefix, Start: start, End: end,
			PreferredLifetime: time.Duration(b.PreferredLifetime) * time.Second, ValidLifetime: time.Duration(b.ValidLifetime) * time.Second,
			RapidCommit: b.RapidCommit, Preference: b.Preference, Enabled: b.Enabled, PDPool: pool, PDLength: b.PDLength,
		})
		if err != nil {
			return wrap(what, err)
		}
		sum.Scopes6++
		for _, e := range b.Exclusions {
			s0, _, err := parse6(what, "exclusion start", e.Start, false)
			if err != nil {
				return err
			}
			e0, _, err := parse6(what, "exclusion end", e.End, false)
			if err != nil {
				return err
			}
			if _, err := s.AddExclusion(ctx, scope6.Exclusion{ScopeID: sc.ID, Start: s0, End: e0}); err != nil {
				return wrap(what+", exclusion "+e.Start, err)
			}
		}
		for _, r := range b.Reservations {
			ip, _, err := parse6(what, "reservation ip", r.IP, false)
			if err != nil {
				return err
			}
			iaid := int64(scope6.AnyIAID)
			if r.IAID != nil {
				iaid = *r.IAID
			}
			res, err := s.CreateReservation(ctx, scope6.Reservation{ScopeID: sc.ID, DUID: r.DUID, IAID: iaid, IP: ip, Hostname: r.Hostname, Description: r.Description})
			if err != nil {
				return wrap(what+", reservation "+r.IP, err)
			}
			sum.Reservations++
			n, err := setOptions6(ctx, s, scope6.Owner{Kind: scope6.OwnerReservation, ID: res.ID}, r.Options, wrap, what+", reservation "+r.IP)
			if err != nil {
				return err
			}
			sum.Options += n
		}
		n, err := setOptions6(ctx, s, scope6.Owner{Kind: scope6.OwnerScope, ID: sc.ID}, b.Options, wrap, what)
		if err != nil {
			return err
		}
		sum.Options += n
	}
	return nil
}
