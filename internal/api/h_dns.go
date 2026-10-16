package api

import (
	"net/http"

	"github.com/meister119/leasorix/internal/api/gen"
	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/scope"
)

// ---- TSIG keys --------------------------------------------------------------------

func toDNSKey(k scope.DNSKey) gen.DnsKey {
	return gen.DnsKey{Id: k.ID, Name: k.Name, Algorithm: k.Algorithm}
}

func fromDNSKeyInput(id int64, in gen.DnsKeyInput) scope.DNSKey {
	k := scope.DNSKey{ID: id, Name: in.Name, Secret: str(in.Secret)}
	if in.Algorithm != nil {
		k.Algorithm = string(*in.Algorithm)
	}
	return k
}

func (h *handlers) ListDnsKeys(w http.ResponseWriter, r *http.Request) {
	all, err := h.s.deps.Scopes.ListDNSKeys(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]gen.DnsKey, 0, len(all))
	for _, k := range all {
		out = append(out, toDNSKey(k))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) CreateDnsKey(w http.ResponseWriter, r *http.Request) {
	var in gen.DnsKeyInput
	if !decode(w, r, &in) {
		return
	}
	k, err := h.s.deps.Scopes.CreateDNSKey(r.Context(), fromDNSKeyInput(0, in))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toDNSKey(k))
}

func (h *handlers) UpdateDnsKey(w http.ResponseWriter, r *http.Request, id gen.Id) {
	var in gen.DnsKeyInput
	if !decode(w, r, &in) {
		return
	}
	k, err := h.s.deps.Scopes.UpdateDNSKey(r.Context(), fromDNSKeyInput(id, in))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDNSKey(k))
}

func (h *handlers) DeleteDnsKey(w http.ResponseWriter, r *http.Request, id gen.Id) {
	if err := h.s.deps.Scopes.DeleteDNSKey(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- zones ------------------------------------------------------------------------

func toDNSZone(z scope.DNSZone) gen.DnsZone {
	return gen.DnsZone{Id: z.ID, Name: z.Name, Server: z.Server, KeyId: z.KeyID, KeyName: z.KeyName}
}

func fromDNSZoneInput(id int64, in gen.DnsZoneInput) scope.DNSZone {
	z := scope.DNSZone{ID: id, Name: in.Name, Server: in.Server}
	if in.KeyId != nil {
		z.KeyID = *in.KeyId
	}
	return z
}

func (h *handlers) ListDnsZones(w http.ResponseWriter, r *http.Request) {
	all, err := h.s.deps.Scopes.ListDNSZones(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]gen.DnsZone, 0, len(all))
	for _, z := range all {
		out = append(out, toDNSZone(z))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) CreateDnsZone(w http.ResponseWriter, r *http.Request) {
	var in gen.DnsZoneInput
	if !decode(w, r, &in) {
		return
	}
	z, err := h.s.deps.Scopes.CreateDNSZone(r.Context(), fromDNSZoneInput(0, in))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toDNSZone(z))
}

func (h *handlers) UpdateDnsZone(w http.ResponseWriter, r *http.Request, id gen.Id) {
	var in gen.DnsZoneInput
	if !decode(w, r, &in) {
		return
	}
	z, err := h.s.deps.Scopes.UpdateDNSZone(r.Context(), fromDNSZoneInput(id, in))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDNSZone(z))
}

func (h *handlers) DeleteDnsZone(w http.ResponseWriter, r *http.Request, id gen.Id) {
	if err := h.s.deps.Scopes.DeleteDNSZone(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- registrations ------------------------------------------------------------------

func (h *handlers) ListDnsRegistrations(w http.ResponseWriter, _ *http.Request) {
	out := []gen.DnsRegistration{}
	if h.s.deps.DNS != nil {
		for _, reg := range h.s.deps.DNS.Registrations() {
			g := gen.DnsRegistration{
				Ip: ip4.String(reg.IP), Fqdn: reg.WantFQDN, Forward: reg.WantFwd, Reverse: reg.WantRev,
				Protected: ptr(reg.WantDHCID != ""), ARecord: reg.HaveFwd, PtrRecord: reg.HaveRev,
				State: gen.DnsRegistrationState(reg.State()), Attempts: reg.Attempts, LastError: reg.LastError,
				NextAttempt: timePtr(reg.NextAttempt), UpdatedAt: timePtr(reg.Updated),
			}
			if !reg.WantFwd && !reg.WantRev {
				g.Fqdn = ""
			}
			out = append(out, g)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
