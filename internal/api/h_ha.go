package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/meister119/leasorix/internal/api/gen"
	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/ha"
	"github.com/meister119/leasorix/internal/lease"
	"github.com/meister119/leasorix/internal/scope"
)

func ptr[T any](v T) *T { return &v }

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return ptr(t.UTC())
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toHAStatus(s ha.Status) gen.HAStatus {
	return gen.HAStatus{
		Enabled:               true,
		NodeId:                ptr(s.NodeID),
		PeerId:                ptr(s.PeerID),
		PeerAddress:           ptr(s.PeerAddress),
		Mode:                  ptr(gen.HAStatusMode(s.Mode)),
		Role:                  ptr(gen.HAStatusRole(s.Role)),
		Replication:           ptr(gen.HAStatusReplication(s.Replication)),
		TlsMode:               ptr(gen.HAStatusTlsMode(s.TLSMode)),
		State:                 ptr(gen.HAState(s.State)),
		StateSince:            timePtr(s.StateSince),
		Serving:               ptr(s.Serving),
		Connected:             ptr(s.Connected),
		PeerState:             strPtr(s.PeerState),
		PeerVersion:           strPtr(s.PeerVersion),
		LastContact:           timePtr(s.LastContact),
		PartnerDownSince:      timePtr(s.PartnerDownSince),
		TakeoverAt:            timePtr(s.TakeoverAt),
		ConfigLeader:          ptr(s.ConfigLeader),
		ConfigReadOnly:        ptr(s.ConfigReadOnly),
		ConfigVersion:         ptr(int64(s.ConfigVersion)),
		ConfigInSync:          ptr(s.ConfigInSync),
		LoadBalancePercent:    ptr(s.LoadBalancePercent),
		StandbyReservePercent: ptr(s.StandbyReservePercent),
		Mclt:                  ptr(int(s.MCLT / time.Second)),
		AutoPartnerDown:       ptr(int(s.AutoPartnerDown / time.Second)),
		LastResync:            timePtr(s.LastResync),
		Error:                 strPtr(s.Error),
		ResyncError:           strPtr(s.ResyncError),
		ConfigError:           strPtr(s.ConfigError),
		CertExpires:           timePtr(s.CertExpires),
	}
}

func (h *handlers) GetHAStatus(w http.ResponseWriter, _ *http.Request) {
	if h.s.deps.HA == nil {
		writeJSON(w, http.StatusOK, gen.HAStatus{Enabled: false})
		return
	}
	writeJSON(w, http.StatusOK, toHAStatus(h.s.deps.HA.Status()))
}

func (h *handlers) DeclarePartnerDown(w http.ResponseWriter, r *http.Request) {
	if h.s.deps.HA == nil {
		h.fail(w, r, apperr.Conflictf("high availability is not configured"))
		return
	}
	if err := h.s.deps.HA.PartnerDown(r.Context()); err != nil {
		if errors.Is(err, ha.ErrPartnerReachable) || errors.Is(err, ha.ErrAlreadyDown) {
			h.fail(w, r, apperr.Conflictf("%s", err.Error()))
			return
		}
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toHAStatus(h.s.deps.HA.Status()))
}

func (h *handlers) GenerateHACertificates(w http.ResponseWriter, r *http.Request) {
	var in gen.GenerateHACertificatesJSONRequestBody
	if !decode(w, r, &in) {
		return
	}
	b, err := ha.GenerateCerts(in.Nodes)
	if err != nil {
		badRequest(w, "nodes", err.Error())
		return
	}
	out := gen.HACertificates{CaCert: string(b.CAPEM), NotAfter: b.NotAfter.UTC()}
	for _, n := range b.Nodes {
		out.Nodes = append(out.Nodes, struct {
			Cert   string `json:"cert"`
			Key    string `json:"key"`
			NodeId string `json:"node_id"`
		}{Cert: string(n.CertPEM), Key: string(n.KeyPEM), NodeId: n.NodeID})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- lease export / import ----------------------------------------------------------------

func (h *handlers) nodeID() string {
	if h.s.deps.HA == nil {
		return ""
	}
	return h.s.deps.HA.Status().NodeID
}

func (h *handlers) ExportLeases(w http.ResponseWriter, _ *http.Request) {
	f := lease.NewExport(h.nodeID(), h.s.deps.Leases.All(), time.Now())
	w.Header().Set("Content-Disposition", `attachment; filename="leasorix-leases-`+time.Now().UTC().Format("20060102-150405")+`.json"`)
	writeJSON(w, http.StatusOK, f)
}

func (h *handlers) ImportLeases(w http.ResponseWriter, r *http.Request) {
	f, err := lease.ReadExport(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large", nil)
			return
		}
		badRequest(w, "leases", err.Error())
		return
	}
	recs, err := f.Records()
	if err != nil {
		badRequest(w, "leases", err.Error())
		return
	}
	changed, conflicts, err := h.s.deps.Leases.Import(r.Context(), h.s.deps.Scopes.Snapshot(), recs)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.s.deps.Log.Info("lease export imported", "records", len(recs), "changed", changed, "conflicts", conflicts)
	writeJSON(w, http.StatusOK, gen.LeaseImportResult{Records: len(recs), Changed: changed, Conflicts: conflicts})
}

// ---- scope bundles -------------------------------------------------------------------------

func (h *handlers) ExportScope(w http.ResponseWriter, r *http.Request, id gen.Id) {
	b, err := h.s.deps.Scopes.ExportScope(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (h *handlers) ImportScope(w http.ResponseWriter, r *http.Request) {
	var b scope.Bundle
	if !decode(w, r, &b) {
		return
	}
	sc, err := h.s.deps.Scopes.ImportScope(r.Context(), &b)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toScope(sc))
}

func (h *handlers) SplitScope(w http.ResponseWriter, r *http.Request, id gen.Id) {
	var in gen.SplitScopeInput
	if !decode(w, r, &in) {
		return
	}
	res, err := h.s.deps.Scopes.SplitScope(r.Context(), id, in.LocalPercent)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		LocalRange      scope.BundleRange `json:"local_range"`
		PartnerRange    scope.BundleRange `json:"partner_range"`
		ExclusionsAdded []gen.Exclusion   `json:"exclusions_added"`
		Partner         *scope.Bundle     `json:"partner"`
	}{LocalRange: res.LocalRange, PartnerRange: res.PartnerRange, ExclusionsAdded: []gen.Exclusion{}, Partner: res.Partner}
	for _, e := range res.Added {
		out.ExclusionsAdded = append(out.ExclusionsAdded, toExclusion(e))
	}
	writeJSON(w, http.StatusOK, out)
}
