package api

import (
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/meister119/leasorix/internal/api/gen"
	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/auth"
	"github.com/meister119/leasorix/internal/dhcpopt"
	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/lease"
	"github.com/meister119/leasorix/internal/scope"
)

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// parseAddr parses an IPv4 address field and records a validation error.
func parseAddr(v *apperr.Validator, field, s string) netip.Addr {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil || !a.Is4() {
		v.Add(field, "%q is not an IPv4 address", s)
		return netip.Addr{}
	}
	return a
}

func toScope(sc scope.Scope) gen.Scope {
	return gen.Scope{
		Id: sc.ID, Name: sc.Name, Description: sc.Description, Subnet: sc.Subnet.String(),
		Start: sc.Start.String(), End: sc.End.String(), LeaseTime: int(sc.LeaseTime / time.Second),
		Enabled: sc.Enabled, Bootp: sc.BOOTP, SuperscopeId: sc.SuperscopeID, Dns: toDNSSettings(sc.DNS),
		CreatedAt: sc.CreatedAt, UpdatedAt: sc.UpdatedAt,
	}
}

func toDNSSettings(d scope.DNSSettings) gen.DnsSettings {
	ttl := int(d.TTL / time.Second)
	return gen.DnsSettings{
		Mode: gen.DnsSettingsMode(d.Mode), Domain: &d.Domain, Ttl: &ttl, Cleanup: &d.Cleanup, Protect: &d.Protect,
	}
}

// fromDNSSettings applies optional fields over the defaults.
func fromDNSSettings(in gen.DnsSettings) scope.DNSSettings {
	d := scope.DefaultDNSSettings()
	d.Mode = scope.DNSMode(in.Mode)
	d.Domain = str(in.Domain)
	if in.Ttl != nil {
		d.TTL = time.Duration(*in.Ttl) * time.Second
	}
	if in.Cleanup != nil {
		d.Cleanup = *in.Cleanup
	}
	if in.Protect != nil {
		d.Protect = *in.Protect
	}
	return d
}

func fromScopeInput(in gen.ScopeInput) (scope.Scope, error) {
	var v apperr.Validator
	sc := scope.Scope{
		Name: in.Name, Description: str(in.Description), Enabled: in.Enabled,
		LeaseTime: time.Duration(in.LeaseTime) * time.Second,
	}
	if in.Bootp != nil {
		sc.BOOTP = *in.Bootp
	}
	if in.SuperscopeId != nil {
		sc.SuperscopeID = *in.SuperscopeId
	}
	sc.DNS = scope.DefaultDNSSettings()
	if in.Dns != nil {
		sc.DNS = fromDNSSettings(*in.Dns)
	}
	p, err := netip.ParsePrefix(strings.TrimSpace(in.Subnet))
	if err != nil || !p.Addr().Is4() {
		v.Add("subnet", "%q is not an IPv4 prefix like 192.168.1.0/24", in.Subnet)
	}
	sc.Subnet = p
	sc.Start = parseAddr(&v, "start", in.Start)
	sc.End = parseAddr(&v, "end", in.End)
	v.Check(in.LeaseTime >= 0, "lease_time", "must not be negative")
	return sc, v.Err()
}

func toExclusion(e scope.Exclusion) gen.Exclusion {
	return gen.Exclusion{Id: e.ID, ScopeId: e.ScopeID, Start: e.Start.String(), End: e.End.String()}
}

func toReservation(r scope.Reservation) gen.Reservation {
	return gen.Reservation{
		Id: r.ID, ScopeId: r.ScopeID, Ip: r.IP.String(), Mac: r.MAC, ClientId: r.ClientID,
		Hostname: r.Hostname, Description: r.Description, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func fromReservationInput(in gen.ReservationInput) (scope.Reservation, error) {
	var v apperr.Validator
	r := scope.Reservation{
		ScopeID: in.ScopeId, MAC: str(in.Mac), ClientID: str(in.ClientId),
		Hostname: str(in.Hostname), Description: str(in.Description),
	}
	r.IP = parseAddr(&v, "ip", in.Ip)
	return r, v.Err()
}

func toClass(c scope.Class) gen.Class {
	return gen.Class{Id: c.ID, Name: c.Name, Type: gen.ClassType(c.Type), Data: c.Data, Description: c.Description}
}

func fromClassInput(in gen.ClassInput) scope.Class {
	return scope.Class{Name: in.Name, Type: scope.ClassType(in.Type), Data: in.Data, Description: str(in.Description)}
}

func toPolicy(p scope.Policy) gen.Policy {
	out := gen.Policy{
		Id: p.ID, ScopeId: p.ScopeID, Name: p.Name, Description: p.Description, Order: p.Order,
		Enabled: p.Enabled, Operator: gen.PolicyOperator(p.Operator), CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		Conditions: make([]gen.Condition, len(p.Conditions)), Ranges: make([]gen.AddrRange, len(p.Ranges)),
	}
	if p.LeaseTime != nil {
		lt := int(*p.LeaseTime / time.Second)
		out.LeaseTime = &lt
	}
	for i, c := range p.Conditions {
		out.Conditions[i] = gen.Condition{Field: gen.ConditionField(c.Field), Operator: gen.ConditionOperator(c.Operator), Value: c.Value}
	}
	for i, r := range p.Ranges {
		out.Ranges[i] = gen.AddrRange{Start: r.Start.String(), End: r.End.String()}
	}
	return out
}

func fromPolicyInput(in gen.PolicyInput) (scope.Policy, error) {
	var v apperr.Validator
	p := scope.Policy{
		Name: in.Name, Description: str(in.Description), Enabled: in.Enabled, Operator: string(in.Operator),
	}
	if in.ScopeId != nil {
		p.ScopeID = *in.ScopeId
	}
	if in.Order != nil {
		p.Order = *in.Order
	}
	if in.LeaseTime != nil {
		v.Check(*in.LeaseTime >= 0, "lease_time", "must not be negative")
		lt := time.Duration(*in.LeaseTime) * time.Second
		p.LeaseTime = &lt
	}
	for _, c := range in.Conditions {
		p.Conditions = append(p.Conditions, scope.Condition{Field: scope.ConditionField(c.Field), Operator: string(c.Operator), Value: c.Value})
	}
	if in.Ranges != nil {
		for i, r := range *in.Ranges {
			field := "ranges[" + strconv.Itoa(i) + "]"
			p.Ranges = append(p.Ranges, scope.AddrRange{Start: parseAddr(&v, field+".start", r.Start), End: parseAddr(&v, field+".end", r.End)})
		}
	}
	return p, v.Err()
}

func toFilter(f scope.MACFilter) gen.MacFilter {
	return gen.MacFilter{Id: f.ID, List: gen.MacFilterList(f.List), Pattern: f.Pattern, Description: f.Description, CreatedAt: f.CreatedAt}
}

func toDefinition(d dhcpopt.Definition) gen.OptionDefinition {
	return gen.OptionDefinition{Code: int(d.Code), Name: d.Name, Type: gen.OptionType(d.Type), Description: d.Description, Builtin: d.Builtin}
}

func toOption(snap *scope.Snapshot, ov scope.OptionValue) gen.OptionValue {
	name := ""
	if d, ok := snap.Definition(ov.Code); ok {
		name = d.Name
	}
	return gen.OptionValue{
		Owner: gen.OptionOwner(ov.Owner.Kind), OwnerId: ov.Owner.ID, Code: int(ov.Code),
		Name: name, Type: gen.OptionType(ov.Type), Value: ov.Value,
	}
}

func toLease(snap *scope.Snapshot, l lease.Lease) gen.Lease {
	out := gen.Lease{
		Ip: ip4.String(l.IP), ScopeId: l.ScopeID, Mac: l.MAC, ClientId: l.ClientID, Hostname: l.Hostname,
		State: gen.LeaseState(l.State), StartsAt: l.Start, ExpiresAt: l.Expires, UpdatedAt: l.Updated,
	}
	if p := snap.ByID(l.ScopeID); p != nil {
		out.ScopeName = p.Name
		out.Reserved = p.ReservationAt(l.IP) != nil
	}
	return out
}

func toStats(st lease.ScopeStats) gen.ScopeStats {
	by := make(map[string]int64, len(st.ByState))
	for k, v := range st.ByState {
		by[string(k)] = int64(v)
	}
	return gen.ScopeStats{
		ScopeId: st.ScopeID, Name: st.Name, Subnet: st.Subnet, Total: int64(st.Total), Excluded: int64(st.Excluded),
		Usable: int64(st.Usable), InUse: int64(st.InUse), Reserved: int64(st.Reserved), Declined: int64(st.Declined),
		Available: int64(st.Available), UtilizationPercent: st.Utilization, ActiveLeases: int64(st.ActiveLeases), ByState: by,
	}
}

func toUser(u auth.User) gen.User {
	return gen.User{Id: u.ID, Username: u.Username, Role: gen.Role(u.Role), Disabled: u.Disabled, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt}
}

func toToken(t auth.Token) gen.ApiToken {
	return gen.ApiToken{Id: t.ID, Name: t.Name, Prefix: t.Prefix, Role: gen.Role(t.Role), CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt}
}

func toAudit(e audit.Entry) gen.AuditEntry {
	out := gen.AuditEntry{Id: e.ID, Time: e.Time, Actor: e.Actor, Action: e.Action, ObjectType: e.ObjectType, ObjectId: e.ObjectID}
	if e.Source != "" {
		src := e.Source
		out.Source = &src
	}
	if len(e.Details) > 0 {
		d := e.Details
		out.Details = &d
	}
	return out
}
