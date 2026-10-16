package api

import (
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/meister119/leasorix/internal/api/gen"
	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/audit"
	"github.com/meister119/leasorix/internal/auth"
	"github.com/meister119/leasorix/internal/buildinfo"
	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/lease"
	"github.com/meister119/leasorix/internal/scope"
)

// ---- leases -----------------------------------------------------------------------

func (h *handlers) ListLeases(w http.ResponseWriter, r *http.Request, params gen.ListLeasesParams) {
	snap := h.s.deps.Scopes.Snapshot()
	q := ""
	if params.Q != nil {
		q = strings.ToLower(strings.TrimSpace(*params.Q))
	}
	var items []gen.Lease
	for _, l := range h.s.deps.Leases.All() {
		if params.ScopeId != nil && l.ScopeID != *params.ScopeId {
			continue
		}
		gl := toLease(snap, l)
		if params.State != nil && gl.State != *params.State {
			continue
		}
		if q != "" && !strings.Contains(gl.Ip, q) && !strings.Contains(gl.Mac, q) &&
			!strings.Contains(strings.ToLower(gl.Hostname), q) && !strings.Contains(gl.ClientId, q) {
			continue
		}
		items = append(items, gl)
	}
	total := len(items)
	offset, limit := 0, 100
	if params.Offset != nil && *params.Offset > 0 {
		offset = *params.Offset
	}
	if params.Limit != nil && *params.Limit > 0 && *params.Limit <= 1000 {
		limit = *params.Limit
	}
	if offset > total {
		offset = total
	}
	end := min(offset+limit, total)
	page := items[offset:end]
	if page == nil {
		page = []gen.Lease{}
	}
	writeJSON(w, http.StatusOK, gen.LeaseList{Items: page, Total: total})
}

func parseLeaseIP(w http.ResponseWriter, s string) (uint32, bool) {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() {
		badRequest(w, "ip", "not an IPv4 address")
		return 0, false
	}
	return ip4.MustFromAddr(a), true
}

func (h *handlers) GetLease(w http.ResponseWriter, r *http.Request, ip gen.IP) {
	v, ok := parseLeaseIP(w, ip)
	if !ok {
		return
	}
	l, found := h.s.deps.Leases.Get(v)
	if !found {
		h.fail(w, r, apperr.NotFoundf("lease %s", ip))
		return
	}
	writeJSON(w, http.StatusOK, toLease(h.s.deps.Scopes.Snapshot(), l))
}

func (h *handlers) DeleteLease(w http.ResponseWriter, r *http.Request, ip gen.IP) {
	v, ok := parseLeaseIP(w, ip)
	if !ok {
		return
	}
	found, err := h.s.deps.Leases.Delete(r.Context(), v)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !found {
		h.fail(w, r, apperr.NotFoundf("lease %s", ip))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) ConvertLeaseToReservation(w http.ResponseWriter, r *http.Request, ip gen.IP) {
	var in gen.ConvertLeaseToReservationJSONBody
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	v, ok := parseLeaseIP(w, ip)
	if !ok {
		return
	}
	l, found := h.s.deps.Leases.Get(v)
	if !found || l.State == lease.StateDeclined {
		h.fail(w, r, apperr.NotFoundf("lease %s", ip))
		return
	}
	res := scope.Reservation{
		ScopeID: l.ScopeID, IP: ip4.ToAddr(l.IP), MAC: l.MAC, Hostname: l.Hostname, Description: str(in.Description),
	}
	if res.MAC == "" {
		res.ClientID = l.ClientID
	}
	res, err := h.s.deps.Scopes.CreateReservation(r.Context(), res)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toReservation(res))
}

// ---- stats / audit ------------------------------------------------------------------

func (h *handlers) GetStats(w http.ResponseWriter, _ *http.Request) {
	stats := h.s.deps.Leases.Stats(h.s.deps.Scopes.Snapshot())
	out := gen.Stats{
		Scopes:          make([]gen.ScopeStats, len(stats)),
		PacketsReceived: h.s.deps.Metrics.CounterValues("leasorix_dhcp4_packets_received_total", "type"),
		PacketsSent:     h.s.deps.Metrics.CounterValues("leasorix_dhcp4_packets_sent_total", "type"),
		PacketsDropped:  h.s.deps.Metrics.CounterValues("leasorix_dhcp4_packets_dropped_total", "reason"),
		UptimeSeconds:   int64(time.Since(h.s.deps.Info.StartedAt).Seconds()),
	}
	for i, st := range stats {
		out.Scopes[i] = toStats(st)
		out.ActiveLeases += int64(st.ActiveLeases)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) ListAudit(w http.ResponseWriter, r *http.Request, params gen.ListAuditParams) {
	f := audit.Filter{}
	if params.Since != nil {
		f.Since = *params.Since
	}
	if params.Until != nil {
		f.Until = *params.Until
	}
	f.Actor, f.Action, f.ObjectType, f.ObjectID = str(params.Actor), str(params.Action), str(params.ObjectType), str(params.ObjectId)
	if params.BeforeId != nil {
		f.BeforeID = *params.BeforeId
	}
	f.Limit = 100
	if params.Limit != nil {
		f.Limit = *params.Limit
	}
	entries, err := h.s.deps.Audit.List(r.Context(), f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := gen.AuditList{Items: make([]gen.AuditEntry, len(entries))}
	for i, e := range entries {
		out.Items[i] = toAudit(e)
	}
	if len(entries) > 0 && len(entries) == min(max(f.Limit, 1), 1000) {
		next := entries[len(entries)-1].ID
		out.NextBeforeId = &next
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- users -------------------------------------------------------------------------

func (h *handlers) ListUsers(w http.ResponseWriter, r *http.Request) {
	us, err := h.s.deps.Auth.ListUsers(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]gen.User, len(us))
	for i, u := range us {
		out[i] = toUser(u)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) CreateUser(w http.ResponseWriter, r *http.Request) {
	var in gen.UserCreate
	if !decode(w, r, &in) {
		return
	}
	u, err := h.s.deps.Auth.CreateUser(r.Context(), in.Username, in.Password, auth.Role(in.Role))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toUser(u))
}

func (h *handlers) UpdateUser(w http.ResponseWriter, r *http.Request, id gen.Id) {
	var in gen.UserUpdate
	if !decode(w, r, &in) {
		return
	}
	up := auth.UserUpdate{Disabled: in.Disabled, Password: in.Password}
	if in.Role != nil {
		role := auth.Role(*in.Role)
		up.Role = &role
	}
	u, err := h.s.deps.Auth.UpdateUser(r.Context(), id, up)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUser(u))
}

func (h *handlers) DeleteUser(w http.ResponseWriter, r *http.Request, id gen.Id) {
	if p := principalFrom(r.Context()); p.User.ID == id {
		h.fail(w, r, apperr.Conflictf("you cannot delete your own account"))
		return
	}
	if err := h.s.deps.Auth.DeleteUser(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- settings / system ------------------------------------------------------------------

func (h *handlers) GetSettings(w http.ResponseWriter, r *http.Request) {
	fs, err := h.s.deps.Scopes.FilterSettings(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gen.Settings{MacFilterAllowEnabled: fs.AllowEnabled, MacFilterDenyEnabled: fs.DenyEnabled})
}

func (h *handlers) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var in gen.Settings
	if !decode(w, r, &in) {
		return
	}
	if err := h.s.deps.Scopes.SetFilterSettings(r.Context(), scope.FilterSettings{AllowEnabled: in.MacFilterAllowEnabled, DenyEnabled: in.MacFilterDenyEnabled}); err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (h *handlers) configLeader() *string {
	if h.s.deps.HA == nil {
		return nil
	}
	st := h.s.deps.HA.Status()
	id := st.NodeID
	if st.ConfigReadOnly {
		id = st.PeerID
	}
	return &id
}

func (h *handlers) GetSystemInfo(w http.ResponseWriter, _ *http.Request) {
	ifaces := append([]string{}, h.s.deps.Info.Interfaces...)
	sort.Strings(ifaces)
	writeJSON(w, http.StatusOK, gen.SystemInfo{
		Version: buildinfo.Version, Commit: buildinfo.Commit, GoVersion: buildinfo.GoVersion(),
		StartedAt: h.s.deps.Info.StartedAt, Dhcp4Interfaces: ifaces, Tls: h.s.deps.TLS != nil,
		ConflictDetection: h.s.deps.Info.ConflictDetection,
		HaEnabled:         h.s.deps.HA != nil,
		ConfigReadOnly:    h.s.deps.HA != nil && h.s.deps.HA.ConfigReadOnly(),
		ConfigLeader:      h.configLeader(),
	})
}
