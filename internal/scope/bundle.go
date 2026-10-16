package scope

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/dhcpopt"
	"github.com/meister119/leasorix/internal/ip4"
)

// BundleFormat identifies the scope bundle JSON format.
const BundleFormat = "leasorix-scope/v1"

// Bundle is a self-contained, portable description of one scope with
// everything that belongs to it. It is used to copy a scope to another
// server (split scope) and as building block of the configuration export.
// Objects are identified by content, never by database id.
type Bundle struct {
	Format       string              `json:"format,omitempty" yaml:"format,omitempty"`
	Scope        BundleScope         `json:"scope" yaml:"scope"`
	Exclusions   []BundleRange       `json:"exclusions" yaml:"exclusions"`
	Reservations []BundleReservation `json:"reservations" yaml:"reservations"`
	Options      []BundleOption      `json:"options" yaml:"options"`
	Policies     []BundlePolicy      `json:"policies" yaml:"policies"`
}

// BundleScope holds the scope settings.
type BundleScope struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
	Subnet      string `json:"subnet" yaml:"subnet"`
	Start       string `json:"start" yaml:"start"`
	End         string `json:"end" yaml:"end"`
	// LeaseTime in seconds, 0 = unlimited.
	LeaseTime int64 `json:"lease_time" yaml:"lease_time"`
	Enabled   bool  `json:"enabled" yaml:"enabled"`
	BOOTP     bool  `json:"bootp,omitempty" yaml:"bootp,omitempty"`
	// Superscope is the name of the superscope (created on import if
	// missing).
	Superscope string `json:"superscope,omitempty" yaml:"superscope,omitempty"`
	// DNS are the dynamic DNS settings (absent = defaults: no updates).
	DNS *BundleDNS `json:"dns,omitempty" yaml:"dns,omitempty"`
}

// BundleDNS are the DNS update settings of a scope.
type BundleDNS struct {
	Mode   string `json:"mode" yaml:"mode"`
	Domain string `json:"domain,omitempty" yaml:"domain,omitempty"`
	// TTL in seconds, 0 = automatic.
	TTL     int64 `json:"ttl,omitempty" yaml:"ttl,omitempty"`
	Cleanup bool  `json:"cleanup" yaml:"cleanup"`
	Protect bool  `json:"protect" yaml:"protect"`
}

func bundleDNS(d DNSSettings) *BundleDNS {
	if d == DefaultDNSSettings() {
		return nil
	}
	return &BundleDNS{Mode: string(d.Mode), Domain: d.Domain, TTL: int64(d.TTL / time.Second), Cleanup: d.Cleanup, Protect: d.Protect}
}

// Settings returns the DNS settings of a bundle (nil = defaults).
func (b *BundleDNS) Settings() DNSSettings {
	if b == nil {
		return DefaultDNSSettings()
	}
	return DNSSettings{Mode: DNSMode(b.Mode), Domain: b.Domain, TTL: time.Duration(b.TTL) * time.Second, Cleanup: b.Cleanup, Protect: b.Protect}
}

// BundleRange is an inclusive address range.
type BundleRange struct {
	Start string `json:"start" yaml:"start"`
	End   string `json:"end" yaml:"end"`
}

// BundleOption is an option value (canonical text form).
type BundleOption struct {
	Code  uint8  `json:"code" yaml:"code"`
	Value string `json:"value" yaml:"value"`
}

// BundleReservation is a reservation with its options.
type BundleReservation struct {
	IP          string         `json:"ip" yaml:"ip"`
	MAC         string         `json:"mac,omitempty" yaml:"mac,omitempty"`
	ClientID    string         `json:"client_id,omitempty" yaml:"client_id,omitempty"`
	Hostname    string         `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	Description string         `json:"description,omitempty" yaml:"description,omitempty"`
	Options     []BundleOption `json:"options,omitempty" yaml:"options,omitempty"`
}

// BundlePolicy is a scope level policy.
type BundlePolicy struct {
	Name        string            `json:"name" yaml:"name"`
	Description string            `json:"description,omitempty" yaml:"description,omitempty"`
	Order       int               `json:"order" yaml:"order"`
	Enabled     bool              `json:"enabled" yaml:"enabled"`
	Operator    string            `json:"operator" yaml:"operator"`
	LeaseTime   *int64            `json:"lease_time,omitempty" yaml:"lease_time,omitempty"`
	Conditions  []BundleCondition `json:"conditions" yaml:"conditions"`
	Ranges      []BundleRange     `json:"ranges,omitempty" yaml:"ranges,omitempty"`
	Options     []BundleOption    `json:"options,omitempty" yaml:"options,omitempty"`
}

// BundleCondition is a policy condition.
type BundleCondition struct {
	Field    string `json:"field" yaml:"field"`
	Operator string `json:"operator" yaml:"operator"`
	Value    string `json:"value" yaml:"value"`
}

func bundleOptions(vs []OptionValue) []BundleOption {
	out := make([]BundleOption, 0, len(vs))
	for _, v := range vs {
		out = append(out, BundleOption{Code: v.Code, Value: v.Value})
	}
	return out
}

// ExportScope builds the bundle of a scope.
func (s *Store) ExportScope(ctx context.Context, id int64) (*Bundle, error) {
	sc, err := s.GetScope(ctx, id)
	if err != nil {
		return nil, err
	}
	b := &Bundle{
		Format: BundleFormat,
		Scope: BundleScope{
			Name: sc.Name, Description: sc.Description, Subnet: sc.Subnet.String(),
			Start: sc.Start.String(), End: sc.End.String(), LeaseTime: int64(sc.LeaseTime / time.Second), Enabled: sc.Enabled,
			BOOTP: sc.BOOTP, DNS: bundleDNS(sc.DNS),
		},
		Exclusions: []BundleRange{}, Reservations: []BundleReservation{}, Policies: []BundlePolicy{},
	}
	if sc.SuperscopeID != 0 {
		ss, err := s.GetSuperscope(ctx, sc.SuperscopeID)
		if err != nil {
			return nil, err
		}
		b.Scope.Superscope = ss.Name
	}
	excl, err := s.listExclusions(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, e := range excl {
		b.Exclusions = append(b.Exclusions, BundleRange{Start: e.Start.String(), End: e.End.String()})
	}
	opts, err := s.ListOptions(ctx, Owner{Kind: OwnerScope, ID: id})
	if err != nil {
		return nil, err
	}
	b.Options = bundleOptions(opts)
	res, err := s.listReservations(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, r := range res {
		ro, err := s.ListOptions(ctx, Owner{Kind: OwnerReservation, ID: r.ID})
		if err != nil {
			return nil, err
		}
		b.Reservations = append(b.Reservations, BundleReservation{
			IP: r.IP.String(), MAC: r.MAC, ClientID: r.ClientID, Hostname: r.Hostname, Description: r.Description,
			Options: bundleOptions(ro),
		})
	}
	pols, err := s.ListPolicies(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, p := range pols {
		bp, err := s.PolicyBundle(ctx, p)
		if err != nil {
			return nil, err
		}
		b.Policies = append(b.Policies, bp)
	}
	return b, nil
}

// PolicyBundle converts a policy with its options into the portable form.
func (s *Store) PolicyBundle(ctx context.Context, p Policy) (BundlePolicy, error) {
	po, err := s.ListOptions(ctx, Owner{Kind: OwnerPolicy, ID: p.ID})
	if err != nil {
		return BundlePolicy{}, err
	}
	bp := BundlePolicy{
		Name: p.Name, Description: p.Description, Order: p.Order, Enabled: p.Enabled, Operator: p.Operator,
		Conditions: []BundleCondition{}, Options: bundleOptions(po),
	}
	if p.LeaseTime != nil {
		v := int64(*p.LeaseTime / time.Second)
		bp.LeaseTime = &v
	}
	for _, c := range p.Conditions {
		bp.Conditions = append(bp.Conditions, BundleCondition{Field: string(c.Field), Operator: c.Operator, Value: c.Value})
	}
	for _, r := range p.Ranges {
		bp.Ranges = append(bp.Ranges, BundleRange{Start: r.Start.String(), End: r.End.String()})
	}
	return bp, nil
}

// ImportPolicy creates a policy from its portable form; scopeID 0 creates
// a server level policy.
func (s *Store) ImportPolicy(ctx context.Context, scopeID int64, bp BundlePolicy) (Policy, error) {
	p := Policy{ScopeID: scopeID, Name: bp.Name, Description: bp.Description, Order: bp.Order, Enabled: bp.Enabled, Operator: bp.Operator}
	if bp.LeaseTime != nil {
		d := time.Duration(*bp.LeaseTime) * time.Second
		p.LeaseTime = &d
	}
	for _, c := range bp.Conditions {
		p.Conditions = append(p.Conditions, Condition{Field: ConditionField(c.Field), Operator: c.Operator, Value: c.Value})
	}
	for _, r := range bp.Ranges {
		st, err := parseAddr("ranges", r.Start)
		if err != nil {
			return p, err
		}
		en, err := parseAddr("ranges", r.End)
		if err != nil {
			return p, err
		}
		p.Ranges = append(p.Ranges, AddrRange{Start: st, End: en})
	}
	created, err := s.CreatePolicy(ctx, p)
	if err != nil {
		return created, fmt.Errorf("policy %q: %w", bp.Name, err)
	}
	for _, ov := range bp.Options {
		if _, err := s.SetOption(ctx, OptionValue{Owner: Owner{Kind: OwnerPolicy, ID: created.ID}, Code: ov.Code, Value: ov.Value}); err != nil {
			return created, fmt.Errorf("policy %q option %d: %w", bp.Name, ov.Code, err)
		}
	}
	return created, nil
}

// CustomOptionDefinitions returns the user defined option definitions.
func (s *Store) CustomOptionDefinitions(ctx context.Context) ([]dhcpopt.Definition, error) {
	return s.listCustomDefinitions(ctx)
}

func parseAddr(field, s string) (netip.Addr, error) {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() {
		var v apperr.Validator
		v.Add(field, "%q is not an IPv4 address", s)
		return a, v.Err()
	}
	return a, nil
}

// ImportScope creates a scope from a bundle. Either everything is created
// or nothing: if a part fails, the partially created scope is removed again.
func (s *Store) ImportScope(ctx context.Context, b *Bundle) (Scope, error) {
	if b.Format != BundleFormat {
		var v apperr.Validator
		v.Add("format", "unsupported bundle format %q (expected %s)", b.Format, BundleFormat)
		return Scope{}, v.Err()
	}
	prefix, err := netip.ParsePrefix(b.Scope.Subnet)
	if err != nil {
		var v apperr.Validator
		v.Add("subnet", "%q is not a subnet", b.Scope.Subnet)
		return Scope{}, v.Err()
	}
	start, err := parseAddr("start", b.Scope.Start)
	if err != nil {
		return Scope{}, err
	}
	end, err := parseAddr("end", b.Scope.End)
	if err != nil {
		return Scope{}, err
	}
	var superscopeID int64
	if b.Scope.Superscope != "" {
		if superscopeID, err = s.superscopeByName(ctx, b.Scope.Superscope, true); err != nil {
			return Scope{}, err
		}
	}
	sc, err := s.CreateScope(ctx, Scope{
		Name: b.Scope.Name, Description: b.Scope.Description, Subnet: prefix, Start: start, End: end,
		LeaseTime: time.Duration(b.Scope.LeaseTime) * time.Second, Enabled: b.Scope.Enabled,
		BOOTP: b.Scope.BOOTP, SuperscopeID: superscopeID, DNS: b.Scope.DNS.Settings(),
	})
	if err != nil {
		return sc, err
	}
	if err := s.importParts(ctx, sc.ID, b); err != nil {
		if derr := s.DeleteScope(ctx, sc.ID); derr != nil {
			return sc, errors.Join(err, fmt.Errorf("removing partially imported scope: %w", derr))
		}
		return Scope{}, err
	}
	return sc, nil
}

func (s *Store) importParts(ctx context.Context, id int64, b *Bundle) error {
	setOpts := func(o Owner, opts []BundleOption, what string) error {
		for _, ov := range opts {
			if _, err := s.SetOption(ctx, OptionValue{Owner: o, Code: ov.Code, Value: ov.Value}); err != nil {
				return fmt.Errorf("%s option %d: %w", what, ov.Code, err)
			}
		}
		return nil
	}
	for _, e := range b.Exclusions {
		st, err := parseAddr("exclusions", e.Start)
		if err != nil {
			return err
		}
		en, err := parseAddr("exclusions", e.End)
		if err != nil {
			return err
		}
		if _, err := s.AddExclusion(ctx, Exclusion{ScopeID: id, Start: st, End: en}); err != nil {
			return fmt.Errorf("exclusion %s-%s: %w", e.Start, e.End, err)
		}
	}
	if err := setOpts(Owner{Kind: OwnerScope, ID: id}, b.Options, "scope"); err != nil {
		return err
	}
	for _, r := range b.Reservations {
		a, err := parseAddr("reservations", r.IP)
		if err != nil {
			return err
		}
		res, err := s.CreateReservation(ctx, Reservation{ScopeID: id, IP: a, MAC: r.MAC, ClientID: r.ClientID, Hostname: r.Hostname, Description: r.Description})
		if err != nil {
			return fmt.Errorf("reservation %s: %w", r.IP, err)
		}
		if err := setOpts(Owner{Kind: OwnerReservation, ID: res.ID}, r.Options, "reservation "+r.IP); err != nil {
			return err
		}
	}
	for _, bp := range b.Policies {
		if _, err := s.ImportPolicy(ctx, id, bp); err != nil {
			return err
		}
	}
	return nil
}

// SplitResult describes a split scope: the exclusions added locally and the
// bundle for the second server.
type SplitResult struct {
	LocalRange   BundleRange
	PartnerRange BundleRange
	Added        []Exclusion
	Partner      *Bundle
}

// SplitScope prepares a classic split scope for two independent servers
// without replication: this server keeps the first localPercent of the
// dynamic range, the rest is excluded here. The returned bundle describes
// the same scope for the second server, with the local part excluded.
func (s *Store) SplitScope(ctx context.Context, id int64, localPercent int) (*SplitResult, error) {
	if localPercent < 1 || localPercent > 99 {
		var v apperr.Validator
		v.Add("local_percent", "must be between 1 and 99")
		return nil, v.Err()
	}
	b, err := s.ExportScope(ctx, id)
	if err != nil {
		return nil, err
	}
	sc, err := s.GetScope(ctx, id)
	if err != nil {
		return nil, err
	}
	r := sc.Range()
	size := uint64(r.End) - uint64(r.Start) + 1
	if size < 2 {
		return nil, apperr.Conflictf("the address range is too small to split")
	}
	n := max(1, min(size-1, size*uint64(localPercent)/100))
	local := ip4.Range{Start: r.Start, End: r.Start + uint32(n) - 1}
	partner := ip4.Range{Start: local.End + 1, End: r.End}

	excl, err := s.listExclusions(ctx, id)
	if err != nil {
		return nil, err
	}
	var existing []ip4.Range
	for _, e := range excl {
		existing = append(existing, ip4.Range{Start: ip4.MustFromAddr(e.Start), End: ip4.MustFromAddr(e.End)})
	}
	res := &SplitResult{
		LocalRange:   BundleRange{Start: ip4.String(local.Start), End: ip4.String(local.End)},
		PartnerRange: BundleRange{Start: ip4.String(partner.Start), End: ip4.String(partner.End)},
		Partner:      b,
	}
	// The partner excludes the local part (minus what is excluded anyway).
	for _, g := range gaps(local, existing) {
		b.Exclusions = append(b.Exclusions, BundleRange{Start: ip4.String(g.Start), End: ip4.String(g.End)})
	}
	for _, g := range gaps(partner, existing) {
		e, err := s.AddExclusion(ctx, Exclusion{ScopeID: id, Start: ip4.ToAddr(g.Start), End: ip4.ToAddr(g.End)})
		if err != nil {
			for _, done := range res.Added {
				_ = s.DeleteExclusion(ctx, id, done.ID)
			}
			return nil, err
		}
		res.Added = append(res.Added, e)
	}
	return res, nil
}

// gaps returns the parts of r not covered by any of the ranges in cover.
func gaps(r ip4.Range, cover []ip4.Range) []ip4.Range {
	out := []ip4.Range{r}
	for _, c := range cover {
		var next []ip4.Range
		for _, g := range out {
			if c.End < g.Start || c.Start > g.End {
				next = append(next, g)
				continue
			}
			if c.Start > g.Start {
				next = append(next, ip4.Range{Start: g.Start, End: c.Start - 1})
			}
			if c.End < g.End {
				next = append(next, ip4.Range{Start: c.End + 1, End: g.End})
			}
		}
		out = next
	}
	return out
}

// superscopeByName returns the id of the superscope with that name
// (case-insensitive), creating it if requested.
func (s *Store) superscopeByName(ctx context.Context, name string, create bool) (int64, error) {
	all, err := s.ListSuperscopes(ctx)
	if err != nil {
		return 0, err
	}
	for _, ss := range all {
		if strings.EqualFold(ss.Name, name) {
			return ss.ID, nil
		}
	}
	if !create {
		return 0, apperr.NotFoundf("superscope %q", name)
	}
	ss, err := s.CreateSuperscope(ctx, Superscope{Name: name})
	return ss.ID, err
}
