package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/meister119/leasorix/internal/api/gen"
	"github.com/meister119/leasorix/internal/backup"
	"github.com/meister119/leasorix/internal/configio"
	"github.com/meister119/leasorix/internal/scope"
)

// ---- superscopes -----------------------------------------------------------------------

func toSuperscope(ss scope.Superscope) gen.Superscope {
	ids := ss.ScopeIDs
	if ids == nil {
		ids = []int64{}
	}
	return gen.Superscope{Id: ss.ID, Name: ss.Name, Description: ss.Description, ScopeIds: ids}
}

func (h *handlers) ListSuperscopes(w http.ResponseWriter, r *http.Request) {
	all, err := h.s.deps.Scopes.ListSuperscopes(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]gen.Superscope, 0, len(all))
	for _, ss := range all {
		out = append(out, toSuperscope(ss))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) GetSuperscope(w http.ResponseWriter, r *http.Request, id gen.Id) {
	ss, err := h.s.deps.Scopes.GetSuperscope(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSuperscope(ss))
}

func (h *handlers) CreateSuperscope(w http.ResponseWriter, r *http.Request) {
	var in gen.SuperscopeInput
	if !decode(w, r, &in) {
		return
	}
	ss, err := h.s.deps.Scopes.CreateSuperscope(r.Context(), scope.Superscope{Name: in.Name, Description: str(in.Description)})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSuperscope(ss))
}

func (h *handlers) UpdateSuperscope(w http.ResponseWriter, r *http.Request, id gen.Id) {
	var in gen.SuperscopeInput
	if !decode(w, r, &in) {
		return
	}
	ss, err := h.s.deps.Scopes.UpdateSuperscope(r.Context(), scope.Superscope{ID: id, Name: in.Name, Description: str(in.Description)})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSuperscope(ss))
}

func (h *handlers) DeleteSuperscope(w http.ResponseWriter, r *http.Request, id gen.Id) {
	if err := h.s.deps.Scopes.DeleteSuperscope(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- configuration export / import ---------------------------------------------------------

func stamp() string { return time.Now().UTC().Format("20060102-150405") }

func (h *handlers) ExportConfig(w http.ResponseWriter, r *http.Request, params gen.ExportConfigParams) {
	d, err := configio.Export(r.Context(), h.s.deps.Scopes, time.Now())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	asYAML := params.Format == nil || *params.Format == "yaml"
	var buf bytes.Buffer
	if err := configio.Encode(&buf, d, asYAML); err != nil {
		h.fail(w, r, err)
		return
	}
	ext, ct := "json", "application/json"
	if asYAML {
		ext, ct = "yaml", "application/yaml; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="leasorix-config-`+stamp()+`.`+ext+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large", nil)
		} else {
			writeError(w, http.StatusBadRequest, "reading request body failed", nil)
		}
		return nil, false
	}
	return b, true
}

func importOptions(mode *string, dryRun *bool) configio.Options {
	o := configio.Options{Mode: configio.ModeMerge}
	if mode != nil {
		o.Mode = configio.Mode(*mode)
	}
	if dryRun != nil {
		o.DryRun = *dryRun
	}
	return o
}

func toImportResult(s *configio.Summary) gen.ConfigImportResult {
	return gen.ConfigImportResult{
		Mode: gen.ConfigImportResultMode(s.Mode), DryRun: s.DryRun, Scopes: s.Scopes, Reservations: s.Reservations,
		Policies: s.Policies, Classes: s.Classes, OptionDefinitions: s.OptionDefinitions, Options: s.Options,
		MacFilters: s.MACFilters, Superscopes: s.Superscopes, DnsZones: s.DNSZones,
	}
}

func (h *handlers) ImportConfig(w http.ResponseWriter, r *http.Request, params gen.ImportConfigParams) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	d, err := configio.Decode(body)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	sum, err := configio.Import(r.Context(), h.s.deps.DB, h.s.deps.Scopes, d, importOptions((*string)(params.Mode), params.DryRun))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toImportResult(sum))
}

func (h *handlers) ImportNetsh(w http.ResponseWriter, r *http.Request, params gen.ImportNetshParams) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	d, warnings, err := configio.ParseNetsh(bytes.NewReader(body))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	sum, err := configio.Import(r.Context(), h.s.deps.DB, h.s.deps.Scopes, d, importOptions((*string)(params.Mode), params.DryRun))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := gen.NetshImportResult{Result: toImportResult(sum), Warnings: []struct {
		Line   int    `json:"line"`
		Reason string `json:"reason"`
		Text   string `json:"text"`
	}{}}
	for _, wr := range warnings {
		out.Warnings = append(out.Warnings, struct {
			Line   int    `json:"line"`
			Reason string `json:"reason"`
			Text   string `json:"text"`
		}{Line: wr.Line, Reason: wr.Reason, Text: wr.Text})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- backup / restore -----------------------------------------------------------------------

func (h *handlers) DownloadBackup(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	if _, err := backup.Create(r.Context(), h.s.deps.DB, &buf); err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="leasorix-backup-`+stamp()+`.db"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func (h *handlers) RestoreBackup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sum, err := backup.Restore(ctx, h.s.deps.DB, r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large", nil)
			return
		}
		h.fail(w, r, err)
		return
	}
	if err := h.s.deps.Scopes.Load(ctx); err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.s.deps.Leases.Reload(ctx); err != nil {
		h.fail(w, r, err)
		return
	}
	h.s.deps.Log.Warn("backup restored; all sessions ended", "scopes", sum.Scopes, "users", sum.Users, "leases", sum.Leases)
	writeJSON(w, http.StatusOK, gen.RestoreResult{SchemaVersion: sum.SchemaVersion, Scopes: sum.Scopes, Reservations: sum.Reservations, Users: sum.Users, Leases: sum.Leases})
}
