package api

import (
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/meister119/leasorix/internal/api/gen"
	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/dhcpopt6"
	"github.com/meister119/leasorix/internal/lease6"
	"github.com/meister119/leasorix/internal/scope6"
)

func (h *handlers) v6Available(w http.ResponseWriter) bool {
	if h.s.deps.Scopes6 == nil || h.s.deps.Leases6 == nil {
		writeError(w, http.StatusServiceUnavailable, "DHCPv6 is not available", nil)
		return false
	}
	return true
}

func text(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

func prefixString(p netip.Prefix) string {
	if !p.IsValid() {
		return ""
	}
	return p.String()
}

// ---- scopes ------------------------------------------------------------------------

func toScope6(sc scope6.Scope) gen.Scope6 {
	return gen.Scope6{
		Id: sc.ID, Name: sc.Name, Description: sc.Description, Prefix: prefixString(sc.Prefix), Start: text(sc.Start), End: text(sc.End),
		PreferredLifetime: int(sc.PreferredLifetime / time.Second), ValidLifetime: int(sc.ValidLifetime / time.Second),
		RapidCommit: sc.RapidCommit, Preference: int(sc.Preference), Enabled: sc.Enabled, PdPool: prefixString(sc.PDPool), PdLength: sc.PDLength,
		CreatedAt: sc.CreatedAt, UpdatedAt: sc.UpdatedAt,
	}
}

func parseAddr6(v *apperr.Validator, field, s string) netip.Addr {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Addr{}
	}
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is6() || a.Is4In6() {
		v.Add(field, "%q is not an IPv6 address", s)
		return netip.Addr{}
	}
	return a
}

func parsePrefix6(v *apperr.Validator, field, s string) netip.Prefix {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Prefix{}
	}
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is6() {
		v.Add(field, "%q is not an IPv6 prefix like 2001:db8:1::/64", s)
		return netip.Prefix{}
	}
	return p
}

func fromScope6Input(id int64, in gen.Scope6Input) (scope6.Scope, error) {
	var v apperr.Validator
	sc := scope6.Scope{
		ID: id, Name: in.Name, Description: str(in.Description), Enabled: in.Enabled,
		PreferredLifetime: time.Duration(in.PreferredLifetime) * time.Second, ValidLifetime: time.Duration(in.ValidLifetime) * time.Second,
	}
	sc.Prefix = parsePrefix6(&v, "prefix", in.Prefix)
	if !sc.Prefix.IsValid() && v.Err() == nil {
		v.Add("prefix", "is required")
	}
	sc.Start = parseAddr6(&v, "start", str(in.Start))
	sc.End = parseAddr6(&v, "end", str(in.End))
	sc.PDPool = parsePrefix6(&v, "pd_pool", str(in.PdPool))
	if in.PdLength != nil {
		sc.PDLength = *in.PdLength
	}
	if in.RapidCommit != nil {
		sc.RapidCommit = *in.RapidCommit
	}
	if in.Preference != nil {
		v.Check(*in.Preference >= 0 && *in.Preference <= 255, "preference", "must be 0-255")
		sc.Preference = uint8(*in.Preference)
	}
	return sc, v.Err()
}

func (h *handlers) ListScopes6(w http.ResponseWriter, r *http.Request) {
	if !h.v6Available(w) {
		return
	}
	all, err := h.s.deps.Scopes6.ListScopes(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]gen.Scope6, 0, len(all))
	for _, sc := range all {
		out = append(out, toScope6(sc))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) GetScope6(w http.ResponseWriter, r *http.Request, id gen.Id) {
	if !h.v6Available(w) {
		return
	}
	sc, err := h.s.deps.Scopes6.GetScope(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toScope6(sc))
}

func (h *handlers) CreateScope6(w http.ResponseWriter, r *http.Request) {
	if !h.v6Available(w) {
		return
	}
	var in gen.Scope6Input
	if !decode(w, r, &in) {
		return
	}
	sc, err := fromScope6Input(0, in)
	if err == nil {
		sc, err = h.s.deps.Scopes6.CreateScope(r.Context(), sc)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toScope6(sc))
}

func (h *handlers) UpdateScope6(w http.ResponseWriter, r *http.Request, id gen.Id) {
	if !h.v6Available(w) {
		return
	}
	var in gen.Scope6Input
	if !decode(w, r, &in) {
		return
	}
	sc, err := fromScope6Input(id, in)
	if err == nil {
		sc, err = h.s.deps.Scopes6.UpdateScope(r.Context(), sc)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toScope6(sc))
}

func (h *handlers) DeleteScope6(w http.ResponseWriter, r *http.Request, id gen.Id) {
	if !h.v6Available(w) {
		return
	}
	if err := h.s.deps.Scopes6.DeleteScope(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- exclusions -------------------------------------------------------------------

func toExclusion6(e scope6.Exclusion) gen.Exclusion6 {
	return gen.Exclusion6{Id: e.ID, ScopeId: e.ScopeID, Start: text(e.Start), End: text(e.End)}
}

func (h *handlers) ListExclusions6(w http.ResponseWriter, r *http.Request, id gen.Id) {
	if !h.v6Available(w) {
		return
	}
	all, err := h.s.deps.Scopes6.ListExclusions(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]gen.Exclusion6, 0, len(all))
	for _, e := range all {
		out = append(out, toExclusion6(e))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) CreateExclusion6(w http.ResponseWriter, r *http.Request, id gen.Id) {
	if !h.v6Available(w) {
		return
	}
	var in gen.Range6
	if !decode(w, r, &in) {
		return
	}
	var v apperr.Validator
	e := scope6.Exclusion{ScopeID: id, Start: parseAddr6(&v, "start", in.Start), End: parseAddr6(&v, "end", in.End)}
	err := v.Err()
	if err == nil {
		e, err = h.s.deps.Scopes6.AddExclusion(r.Context(), e)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toExclusion6(e))
}

func (h *handlers) DeleteExclusion6(w http.ResponseWriter, r *http.Request, id gen.Id, exclusionID int64) {
	if !h.v6Available(w) {
		return
	}
	if err := h.s.deps.Scopes6.DeleteExclusion(r.Context(), id, exclusionID); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- reservations ------------------------------------------------------------------

func toReservation6(res scope6.Reservation) gen.Reservation6 {
	return gen.Reservation6{Id: res.ID, ScopeId: res.ScopeID, Duid: res.DUID, Iaid: res.IAID, Ip: text(res.IP), Hostname: res.Hostname, Description: res.Description}
}

func fromReservation6Input(id int64, in gen.Reservation6Input) (scope6.Reservation, error) {
	var v apperr.Validator
	res := scope6.Reservation{ID: id, DUID: in.Duid, IAID: scope6.AnyIAID, Hostname: str(in.Hostname), Description: str(in.Description)}
	if in.ScopeId != nil {
		res.ScopeID = *in.ScopeId
	}
	if in.Iaid != nil {
		res.IAID = *in.Iaid
	}
	res.IP = parseAddr6(&v, "ip", in.Ip)
	return res, v.Err()
}

func (h *handlers) ListReservations6(w http.ResponseWriter, r *http.Request, params gen.ListReservations6Params) {
	if !h.v6Available(w) {
		return
	}
	var scopeID int64
	if params.ScopeId != nil {
		scopeID = *params.ScopeId
	}
	all, err := h.s.deps.Scopes6.ListReservations(r.Context(), scopeID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]gen.Reservation6, 0, len(all))
	for _, res := range all {
		out = append(out, toReservation6(res))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) CreateReservation6(w http.ResponseWriter, r *http.Request) {
	if !h.v6Available(w) {
		return
	}
	var in gen.Reservation6Input
	if !decode(w, r, &in) {
		return
	}
	res, err := fromReservation6Input(0, in)
	if err == nil {
		res, err = h.s.deps.Scopes6.CreateReservation(r.Context(), res)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toReservation6(res))
}

func (h *handlers) UpdateReservation6(w http.ResponseWriter, r *http.Request, id gen.Id) {
	if !h.v6Available(w) {
		return
	}
	var in gen.Reservation6Input
	if !decode(w, r, &in) {
		return
	}
	res, err := fromReservation6Input(id, in)
	if err == nil {
		res, err = h.s.deps.Scopes6.UpdateReservation(r.Context(), res)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toReservation6(res))
}

func (h *handlers) DeleteReservation6(w http.ResponseWriter, r *http.Request, id gen.Id) {
	if !h.v6Available(w) {
		return
	}
	if err := h.s.deps.Scopes6.DeleteReservation(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- options ----------------------------------------------------------------------------

func optionName6(code uint16) string {
	if d, ok := dhcpopt6.Builtin(code); ok {
		return d.Name
	}
	return "option-" + strconv.Itoa(int(code))
}

func toOption6(ov scope6.OptionValue) gen.Option6Value {
	return gen.Option6Value{
		Owner: gen.Option6Owner(ov.Owner.Kind), OwnerId: ov.Owner.ID, Code: int(ov.Code), Name: optionName6(ov.Code),
		Type: gen.Option6Type(ov.Type), Value: ov.Value,
	}
}

func owner6(owner gen.Option6Owner, id int64) scope6.Owner {
	if owner == gen.Option6OwnerServer {
		return scope6.ServerOwner
	}
	return scope6.Owner{Kind: scope6.OwnerKind(owner), ID: id}
}

func (h *handlers) ListOptionDefinitions6(w http.ResponseWriter, _ *http.Request) {
	defs := dhcpopt6.Definitions()
	out := make([]gen.Option6Definition, 0, len(defs))
	for _, d := range defs {
		out = append(out, gen.Option6Definition{Code: int(d.Code), Name: d.Name, Type: gen.Option6Type(d.Type), Description: d.Description})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) ListOptions6(w http.ResponseWriter, r *http.Request, owner gen.Option6Owner, ownerID gen.OwnerId) {
	if !h.v6Available(w) {
		return
	}
	all, err := h.s.deps.Scopes6.ListOptions(r.Context(), owner6(owner, ownerID))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]gen.Option6Value, 0, len(all))
	for _, ov := range all {
		out = append(out, toOption6(ov))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) SetOption6(w http.ResponseWriter, r *http.Request, owner gen.Option6Owner, ownerID gen.OwnerId, code int) {
	if !h.v6Available(w) {
		return
	}
	var in gen.Option6Input
	if !decode(w, r, &in) {
		return
	}
	ov := scope6.OptionValue{Owner: owner6(owner, ownerID), Code: uint16(code), Value: in.Value}
	if in.Type != nil {
		ov.Type = dhcpopt6.Type(*in.Type)
	}
	ov, err := h.s.deps.Scopes6.SetOption(r.Context(), ov)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOption6(ov))
}

func (h *handlers) DeleteOption6(w http.ResponseWriter, r *http.Request, owner gen.Option6Owner, ownerID gen.OwnerId, code int) {
	if !h.v6Available(w) {
		return
	}
	if err := h.s.deps.Scopes6.DeleteOption(r.Context(), owner6(owner, ownerID), uint16(code)); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- leases ------------------------------------------------------------------------------

func toLease6(l lease6.Lease) gen.Lease6 {
	return gen.Lease6{
		Address: l.Prefix.Addr().String(), PrefixLen: l.Prefix.Bits(), Kind: gen.Lease6Kind(l.Kind), ScopeId: l.ScopeID, Duid: l.DUID,
		Iaid: int64(l.IAID), Hostname: l.Hostname, State: gen.Lease6State(l.State), StartsAt: l.Start, PreferredUntil: l.Preferred, ExpiresAt: l.Expires,
	}
}

func (h *handlers) ListLeases6(w http.ResponseWriter, r *http.Request, params gen.ListLeases6Params) {
	if !h.v6Available(w) {
		return
	}
	q := strings.ToLower(strings.TrimSpace(str(params.Q)))
	out := []gen.Lease6{}
	for _, l := range h.s.deps.Leases6.All() {
		if params.ScopeId != nil && l.ScopeID != *params.ScopeId {
			continue
		}
		if q != "" && !strings.Contains(l.Prefix.String(), q) && !strings.Contains(l.DUID, strings.ReplaceAll(q, ":", "")) && !strings.Contains(strings.ToLower(l.Hostname), q) {
			continue
		}
		out = append(out, toLease6(l))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) DeleteLease6(w http.ResponseWriter, r *http.Request, address string) {
	if !h.v6Available(w) {
		return
	}
	a, err := netip.ParseAddr(address)
	if err != nil {
		writeError(w, http.StatusNotFound, "no binding for "+address, nil)
		return
	}
	for _, l := range h.s.deps.Leases6.All() {
		if l.Prefix.Addr() == a {
			if _, err := h.s.deps.Leases6.Delete(r.Context(), l.Prefix); err != nil {
				h.fail(w, r, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	writeError(w, http.StatusNotFound, "no binding for "+address, nil)
}

func (h *handlers) GetStats6(w http.ResponseWriter, _ *http.Request) {
	if !h.v6Available(w) {
		return
	}
	st := h.s.deps.Leases6.Stats(h.s.deps.Scopes6.Snapshot())
	out := make([]gen.Scope6Stats, 0, len(st))
	for _, s := range st {
		out = append(out, gen.Scope6Stats{
			ScopeId: s.ScopeID, AddressesInUse: s.Active, PrefixesInUse: s.Prefixes, Declined: s.Declined,
			Size: strconv.FormatUint(s.Size, 10), PrefixCapacity: strconv.FormatUint(s.PrefixCapacity, 10),
		})
	}
	writeJSON(w, http.StatusOK, out)
}
