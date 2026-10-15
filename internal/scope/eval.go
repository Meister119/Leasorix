package scope

import (
	"encoding/hex"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/meister119/leasorix/internal/dhcpopt"
	"github.com/meister119/leasorix/internal/ip4"
)

// ClientInfo are the request attributes policies and filters look at.
type ClientInfo struct {
	MAC          string   // canonical
	ClientID     string   // option 61, hex
	Hostname     string   // option 12
	VendorClass  string   // option 60
	UserClasses  []string // option 77 (RFC 3004 entries or the raw value)
	CircuitID    []byte   // option 82.1
	RemoteID     []byte   // option 82.2
	SubscriberID []byte   // option 82.6
}

// ParseUserClass splits option 77 into its entries. RFC 3004 encodes a list
// of length-prefixed entries; Windows clients send a plain string. If the
// data is not a valid RFC 3004 list it is treated as a single entry.
func ParseUserClass(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	var out []string
	for i := 0; i < len(b); {
		n := int(b[i])
		if n == 0 || i+1+n > len(b) {
			return []string{string(b)}
		}
		out = append(out, string(b[i+1:i+1+n]))
		i += 1 + n
	}
	return out
}

// Glob reports whether s matches pattern. '*' matches any sequence, '?' any
// single character; comparison is case-insensitive.
func Glob(pattern, s string) bool {
	return glob(strings.ToLower(pattern), strings.ToLower(s))
}

func glob(p, s string) bool {
	// Iterative matcher with backtracking on the last '*'.
	var pi, si int
	star, match := -1, 0
	for si < len(s) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == s[si]):
			pi++
			si++
		case pi < len(p) && p[pi] == '*':
			star, match = pi, si
			pi++
		case star >= 0:
			pi = star + 1
			match++
			si = match
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// matchBytes compares binary relay data: a pattern prefixed with 0x matches
// the hex form, otherwise the text form (if printable).
func matchBytes(pattern string, b []byte) bool {
	if len(b) == 0 {
		return false
	}
	if hp, ok := strings.CutPrefix(strings.ToLower(pattern), "0x"); ok {
		return Glob(hp, hex.EncodeToString(b))
	}
	if !utf8.Valid(b) {
		return false
	}
	return Glob(pattern, string(b))
}

// PolicyView is the evaluated, read-only form of a policy in a snapshot.
type PolicyView struct {
	Policy
	ranges  []ip4.Range
	options map[uint8][]byte
}

func (s *Snapshot) condMatches(c Condition, ci *ClientInfo) bool {
	var m bool
	switch c.Field {
	case FieldMAC:
		m = ci.MAC != "" && Glob(c.Value, ci.MAC)
	case FieldClientID:
		m = ci.ClientID != "" && Glob(strings.TrimPrefix(c.Value, "0x"), ci.ClientID)
	case FieldHostname:
		m = ci.Hostname != "" && Glob(c.Value, ci.Hostname)
	case FieldVendorClass:
		m = ci.VendorClass != "" && Glob(c.Value, ci.VendorClass)
	case FieldUserClass:
		m = anyGlob(c.Value, ci.UserClasses)
	case FieldClass:
		if cl := s.classes[strings.ToLower(c.Value)]; cl != nil {
			if cl.Type == ClassVendor {
				m = ci.VendorClass != "" && Glob(cl.Data, ci.VendorClass)
			} else {
				m = anyGlob(cl.Data, ci.UserClasses)
			}
		}
	case FieldCircuitID:
		m = matchBytes(c.Value, ci.CircuitID)
	case FieldRemoteID:
		m = matchBytes(c.Value, ci.RemoteID)
	case FieldSubscriberID:
		m = matchBytes(c.Value, ci.SubscriberID)
	}
	if c.Operator == OpNotEqual {
		return !m
	}
	return m
}

func anyGlob(pattern string, vals []string) bool {
	for _, v := range vals {
		if Glob(pattern, v) {
			return true
		}
	}
	return false
}

func (s *Snapshot) policyMatches(p *PolicyView, ci *ClientInfo) bool {
	if !p.Enabled || len(p.Conditions) == 0 {
		return false
	}
	if p.Operator == CombineOr {
		for _, c := range p.Conditions {
			if s.condMatches(c, ci) {
				return true
			}
		}
		return false
	}
	for _, c := range p.Conditions {
		if !s.condMatches(c, ci) {
			return false
		}
	}
	return true
}

// Evaluation is the result of applying policies to a client in a scope.
type Evaluation struct {
	Pool *Pool
	// ScopePolicies and ServerPolicies are the matching policies in
	// processing order.
	ScopePolicies  []*PolicyView
	ServerPolicies []*PolicyView
	// Ranges are the dynamic ranges the client may get an address from, in
	// preference order.
	Ranges []ip4.Range
	// LeaseTime is the effective lease duration (0 = unlimited).
	LeaseTime time.Duration
}

// PolicyNames returns the names of all matching policies.
func (e *Evaluation) PolicyNames() []string {
	var out []string
	for _, p := range e.ScopePolicies {
		out = append(out, p.Name)
	}
	for _, p := range e.ServerPolicies {
		out = append(out, p.Name)
	}
	return out
}

// Evaluate applies the scope and server level policies of p to the client.
func (s *Snapshot) Evaluate(p *Pool, ci *ClientInfo) Evaluation {
	ev := Evaluation{Pool: p, LeaseTime: p.LeaseTime}
	for _, pv := range p.policies {
		if s.policyMatches(pv, ci) {
			ev.ScopePolicies = append(ev.ScopePolicies, pv)
		}
	}
	for _, pv := range s.serverPolicies {
		if s.policyMatches(pv, ci) {
			ev.ServerPolicies = append(ev.ServerPolicies, pv)
		}
	}
	for _, pv := range ev.ScopePolicies {
		ev.Ranges = append(ev.Ranges, pv.ranges...)
	}
	if len(ev.Ranges) == 0 {
		ev.Ranges = p.generalRanges
	}
	for _, pv := range append(append([]*PolicyView{}, ev.ScopePolicies...), ev.ServerPolicies...) {
		if pv.LeaseTime != nil {
			ev.LeaseTime = *pv.LeaseTime
			break
		}
	}
	return ev
}

// Allowed reports whether ip lies in one of the evaluation's ranges.
func (e *Evaluation) Allowed(ip uint32) bool {
	for _, r := range e.Ranges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}

// Options resolves the option values for a client: server < scope <
// server policies < scope policies < reservation. Within one level the
// policy with the lowest processing order wins.
func (s *Snapshot) Options(ev *Evaluation, res *Reservation) map[uint8][]byte {
	out := make(map[uint8][]byte, 16)
	overlay := func(m map[uint8][]byte) {
		for k, v := range m {
			out[k] = v
		}
	}
	overlay(s.serverOptions)
	if ev.Pool != nil {
		overlay(ev.Pool.options)
	}
	for i := len(ev.ServerPolicies) - 1; i >= 0; i-- {
		overlay(ev.ServerPolicies[i].options)
	}
	for i := len(ev.ScopePolicies) - 1; i >= 0; i-- {
		overlay(ev.ScopePolicies[i].options)
	}
	if res != nil {
		overlay(s.reservationOptions[res.ID])
	}
	return out
}

// Filtered reports whether the MAC filters reject the client.
func (s *Snapshot) Filtered(mac string) (bool, string) {
	if s.filterSettings.DenyEnabled {
		for _, p := range s.denyList {
			if Glob(p, mac) {
				return true, "on deny list (" + p + ")"
			}
		}
	}
	if s.filterSettings.AllowEnabled {
		for _, p := range s.allowList {
			if Glob(p, mac) {
				return false, ""
			}
		}
		return true, "not on allow list"
	}
	return false, ""
}

// Definition returns the option definition for code (built-in or custom).
func (s *Snapshot) Definition(code uint8) (dhcpopt.Definition, bool) {
	if d, ok := dhcpopt.Builtin(code); ok {
		return d, true
	}
	d, ok := s.customDefs[code]
	return d, ok
}

// subtractRanges returns r minus all ranges in sub, in ascending order.
func subtractRanges(r ip4.Range, sub []ip4.Range) []ip4.Range {
	sorted := append([]ip4.Range(nil), sub...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	out := []ip4.Range{}
	cur := r.Start
	done := false
	for _, s := range sorted {
		if s.End < cur || s.Start > r.End {
			continue
		}
		if s.Start > cur {
			out = append(out, ip4.Range{Start: cur, End: s.Start - 1})
		}
		if s.End >= r.End {
			done = true
			break
		}
		cur = s.End + 1
	}
	if !done && cur <= r.End {
		out = append(out, ip4.Range{Start: cur, End: r.End})
	}
	return out
}

func sortDefs(d []dhcpopt.Definition) {
	sort.Slice(d, func(i, j int) bool { return d[i].Code < d[j].Code })
}
