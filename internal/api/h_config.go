package api

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/meister119/leasorix/internal/api/gen"
	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/dhcpopt"
	"github.com/meister119/leasorix/internal/scope"
)

// ---- scopes ---------------------------------------------------------------------

func (h *handlers) ListScopes(w http.ResponseWriter, r *http.Request) {
	scs, err := h.s.deps.Scopes.ListScopes(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]gen.Scope, len(scs))
	for i, sc := range scs {
		out[i] = toScope(sc)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) CreateScope(w http.ResponseWriter, r *http.Request) {
	var in gen.ScopeInput
	if !decode(w, r, &in) {
		return
	}
	sc, err := fromScopeInput(in)
	if err == nil {
		sc, err = h.s.deps.Scopes.CreateScope(r.Context(), sc)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toScope(sc))
}

func (h *handlers) GetScope(w http.ResponseWriter, r *http.Request, id gen.Id) {
	sc, err := h.s.deps.Scopes.GetScope(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toScope(sc))
}

func (h *handlers) UpdateScope(w http.ResponseWriter, r *http.Request, id gen.Id) {
	var in gen.ScopeInput
	if !decode(w, r, &in) {
		return
	}
	sc, err := fromScopeInput(in)
	if err == nil && (in.Bootp == nil || in.SuperscopeId == nil) {
		// Optional fields that are absent keep their current value.
		var cur scope.Scope
		if cur, err = h.s.deps.Scopes.GetScope(r.Context(), id); err == nil {
			if in.Bootp == nil {
				sc.BOOTP = cur.BOOTP
			}
			if in.SuperscopeId == nil {
				sc.SuperscopeID = cur.SuperscopeID
			}
		}
	}
	if err == nil {
		sc.ID = id
		sc, err = h.s.deps.Scopes.UpdateScope(r.Context(), sc)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toScope(sc))
}

func (h *handlers) DeleteScope(w http.ResponseWriter, r *http.Request, id gen.Id) {
	if err := h.s.deps.Scopes.DeleteScope(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) GetScopeStats(w http.ResponseWriter, r *http.Request, id gen.Id) {
	p := h.s.deps.Scopes.Snapshot().ByID(id)
	if p == nil {
		h.fail(w, r, apperr.NotFoundf("scope %d", id))
		return
	}
	writeJSON(w, http.StatusOK, toStats(h.s.deps.Leases.ScopeStats(p)))
}

func (h *handlers) ListExclusions(w http.ResponseWriter, r *http.Request, id gen.Id) {
	ex, err := h.s.deps.Scopes.ListExclusions(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]gen.Exclusion, len(ex))
	for i, e := range ex {
		out[i] = toExclusion(e)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) CreateExclusion(w http.ResponseWriter, r *http.Request, id gen.Id) {
	var in gen.AddrRange
	if !decode(w, r, &in) {
		return
	}
	var v apperr.Validator
	e := scope.Exclusion{ScopeID: id, Start: parseAddr(&v, "start", in.Start), End: parseAddr(&v, "end", in.End)}
	err := v.Err()
	if err == nil {
		e, err = h.s.deps.Scopes.AddExclusion(r.Context(), e)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toExclusion(e))
}

func (h *handlers) DeleteExclusion(w http.ResponseWriter, r *http.Request, id gen.Id, exclusionID int64) {
	if err := h.s.deps.Scopes.DeleteExclusion(r.Context(), id, exclusionID); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- reservations -------------------------------------------------------------------

func (h *handlers) ListReservations(w http.ResponseWriter, r *http.Request, params gen.ListReservationsParams) {
	var scopeID int64
	if params.ScopeId != nil {
		scopeID = *params.ScopeId
	}
	rs, err := h.s.deps.Scopes.ListReservations(r.Context(), scopeID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]gen.Reservation, len(rs))
	for i, res := range rs {
		out[i] = toReservation(res)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) CreateReservation(w http.ResponseWriter, r *http.Request) {
	var in gen.ReservationInput
	if !decode(w, r, &in) {
		return
	}
	res, err := fromReservationInput(in)
	if err == nil {
		res, err = h.s.deps.Scopes.CreateReservation(r.Context(), res)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toReservation(res))
}

func (h *handlers) GetReservation(w http.ResponseWriter, r *http.Request, id gen.Id) {
	res, err := h.s.deps.Scopes.GetReservation(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toReservation(res))
}

func (h *handlers) UpdateReservation(w http.ResponseWriter, r *http.Request, id gen.Id) {
	var in gen.ReservationInput
	if !decode(w, r, &in) {
		return
	}
	res, err := fromReservationInput(in)
	if err == nil {
		res.ID = id
		res, err = h.s.deps.Scopes.UpdateReservation(r.Context(), res)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toReservation(res))
}

func (h *handlers) DeleteReservation(w http.ResponseWriter, r *http.Request, id gen.Id) {
	if err := h.s.deps.Scopes.DeleteReservation(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type importErr = struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

func (h *handlers) ImportReservations(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "CSV too large", nil)
			return
		}
		h.fail(w, r, err)
		return
	}
	rows, lines, perr := parseReservationCSV(body, h.s.deps.Scopes.Snapshot())
	if len(perr) > 0 {
		writeJSON(w, http.StatusOK, gen.ImportResult{Created: 0, Errors: perr})
		return
	}
	if len(rows) == 0 {
		badRequest(w, "csv", "no data rows")
		return
	}
	n, rowErrs, err := h.s.deps.Scopes.ImportReservations(r.Context(), rows)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	res := gen.ImportResult{Created: n, Errors: []importErr{}}
	for _, e := range rowErrs {
		res.Errors = append(res.Errors, importErr{Line: lines[e.Row], Message: e.Message})
	}
	writeJSON(w, http.StatusOK, res)
}

// parseReservationCSV parses the import format. lines maps row index to the
// line number in the file.
func parseReservationCSV(body []byte, snap *scope.Snapshot) ([]scope.Reservation, []int, []importErr) {
	body = bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")) // BOM
	first, _, _ := bufio.NewReader(bytes.NewReader(body)).ReadLine()
	rd := csv.NewReader(bytes.NewReader(body))
	if bytes.Count(first, []byte(";")) > bytes.Count(first, []byte(",")) {
		rd.Comma = ';'
	}
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true
	header, err := rd.Read()
	if err != nil {
		return nil, nil, []importErr{{Line: 1, Message: "cannot read header: " + err.Error()}}
	}
	col := map[string]int{}
	for i, name := range header {
		col[strings.ToLower(strings.TrimSpace(name))] = i
	}
	for _, need := range []string{"scope", "ip"} {
		if _, ok := col[need]; !ok {
			return nil, nil, []importErr{{Line: 1, Message: "missing column " + need}}
		}
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	byName := map[string]int64{}
	for _, p := range snap.Pools() {
		byName[strings.ToLower(p.Name)] = p.ID
	}
	var (
		rows  []scope.Reservation
		lines []int
		errs  []importErr
	)
	for {
		rec, err := rd.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line, _ := rd.FieldPos(0)
		if err != nil {
			errs = append(errs, importErr{Line: line, Message: err.Error()})
			continue
		}
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue
		}
		sc := get(rec, "scope")
		scopeID, ok := byName[strings.ToLower(sc)]
		if !ok {
			if id, err := strconv.ParseInt(sc, 10, 64); err == nil && snap.ByID(id) != nil {
				scopeID, ok = id, true
			}
		}
		if !ok {
			errs = append(errs, importErr{Line: line, Message: fmt.Sprintf("unknown scope %q", sc)})
			continue
		}
		ip, err := netip.ParseAddr(get(rec, "ip"))
		if err != nil || !ip.Is4() {
			errs = append(errs, importErr{Line: line, Message: fmt.Sprintf("invalid IPv4 address %q", get(rec, "ip"))})
			continue
		}
		rows = append(rows, scope.Reservation{
			ScopeID: scopeID, IP: ip, MAC: get(rec, "mac"), ClientID: get(rec, "client_id"),
			Hostname: get(rec, "hostname"), Description: get(rec, "description"),
		})
		lines = append(lines, line)
	}
	return rows, lines, errs
}

// ---- options ---------------------------------------------------------------------

func (h *handlers) ListOptionDefinitions(w http.ResponseWriter, r *http.Request) {
	defs, err := h.s.deps.Scopes.ListOptionDefinitions(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]gen.OptionDefinition, len(defs))
	for i, d := range defs {
		out[i] = toDefinition(d)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) CreateOptionDefinition(w http.ResponseWriter, r *http.Request) {
	var in gen.OptionDefinitionInput
	if !decode(w, r, &in) {
		return
	}
	if in.Code < 1 || in.Code > 254 {
		badRequest(w, "code", "must be between 1 and 254")
		return
	}
	d, err := h.s.deps.Scopes.CreateOptionDefinition(r.Context(), dhcpopt.Definition{
		Code: uint8(in.Code), Name: in.Name, Type: dhcpopt.Type(in.Type), Description: str(in.Description),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toDefinition(d))
}

func (h *handlers) DeleteOptionDefinition(w http.ResponseWriter, r *http.Request, code gen.Code) {
	if code < 1 || code > 254 {
		badRequest(w, "code", "must be between 1 and 254")
		return
	}
	if err := h.s.deps.Scopes.DeleteOptionDefinition(r.Context(), uint8(code)); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func ownerOf(kind gen.OptionOwner, id int64) (scope.Owner, error) {
	switch kind {
	case gen.OptionOwnerServer:
		return scope.ServerOwner, nil
	case gen.OptionOwnerScope, gen.OptionOwnerPolicy, gen.OptionOwnerReservation:
		if id <= 0 {
			var v apperr.Validator
			v.Add("ownerId", "must be positive for %s options", kind)
			return scope.Owner{}, v.Err()
		}
		return scope.Owner{Kind: scope.OwnerKind(kind), ID: id}, nil
	}
	var v apperr.Validator
	v.Add("owner", "unknown owner %q", kind)
	return scope.Owner{}, v.Err()
}

func (h *handlers) ListOptions(w http.ResponseWriter, r *http.Request, owner gen.Owner, ownerID gen.OwnerId) {
	o, err := ownerOf(owner, ownerID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	opts, err := h.s.deps.Scopes.ListOptions(r.Context(), o)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	snap := h.s.deps.Scopes.Snapshot()
	out := make([]gen.OptionValue, len(opts))
	for i, ov := range opts {
		out[i] = toOption(snap, ov)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) SetOption(w http.ResponseWriter, r *http.Request, owner gen.Owner, ownerID gen.OwnerId, code gen.Code) {
	var in gen.OptionValueInput
	if !decode(w, r, &in) {
		return
	}
	o, err := ownerOf(owner, ownerID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if code < 1 || code > 254 {
		badRequest(w, "code", "must be between 1 and 254")
		return
	}
	ov := scope.OptionValue{Owner: o, Code: uint8(code), Value: in.Value}
	if in.Type != nil {
		ov.Type = dhcpopt.Type(*in.Type)
	}
	ov, err = h.s.deps.Scopes.SetOption(r.Context(), ov)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOption(h.s.deps.Scopes.Snapshot(), ov))
}

func (h *handlers) DeleteOption(w http.ResponseWriter, r *http.Request, owner gen.Owner, ownerID gen.OwnerId, code gen.Code) {
	o, err := ownerOf(owner, ownerID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if code < 1 || code > 254 {
		badRequest(w, "code", "must be between 1 and 254")
		return
	}
	if err := h.s.deps.Scopes.DeleteOption(r.Context(), o, uint8(code)); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- classes / policies / filters ---------------------------------------------------

func (h *handlers) ListClasses(w http.ResponseWriter, r *http.Request) {
	cs, err := h.s.deps.Scopes.ListClasses(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]gen.Class, len(cs))
	for i, c := range cs {
		out[i] = toClass(c)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) CreateClass(w http.ResponseWriter, r *http.Request) {
	var in gen.ClassInput
	if !decode(w, r, &in) {
		return
	}
	c, err := h.s.deps.Scopes.CreateClass(r.Context(), fromClassInput(in))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toClass(c))
}

func (h *handlers) UpdateClass(w http.ResponseWriter, r *http.Request, id gen.Id) {
	var in gen.ClassInput
	if !decode(w, r, &in) {
		return
	}
	c := fromClassInput(in)
	c.ID = id
	c, err := h.s.deps.Scopes.UpdateClass(r.Context(), c)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toClass(c))
}

func (h *handlers) DeleteClass(w http.ResponseWriter, r *http.Request, id gen.Id) {
	if err := h.s.deps.Scopes.DeleteClass(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) ListPolicies(w http.ResponseWriter, r *http.Request, params gen.ListPoliciesParams) {
	scopeID := int64(-1)
	if params.ScopeId != nil {
		scopeID = *params.ScopeId
	}
	ps, err := h.s.deps.Scopes.ListPolicies(r.Context(), scopeID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]gen.Policy, len(ps))
	for i, p := range ps {
		out[i] = toPolicy(p)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	var in gen.PolicyInput
	if !decode(w, r, &in) {
		return
	}
	p, err := fromPolicyInput(in)
	if err == nil {
		p, err = h.s.deps.Scopes.CreatePolicy(r.Context(), p)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPolicy(p))
}

func (h *handlers) GetPolicy(w http.ResponseWriter, r *http.Request, id gen.Id) {
	p, err := h.s.deps.Scopes.GetPolicy(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPolicy(p))
}

func (h *handlers) UpdatePolicy(w http.ResponseWriter, r *http.Request, id gen.Id) {
	var in gen.PolicyInput
	if !decode(w, r, &in) {
		return
	}
	p, err := fromPolicyInput(in)
	if err == nil {
		p.ID = id
		p, err = h.s.deps.Scopes.UpdatePolicy(r.Context(), p)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPolicy(p))
}

func (h *handlers) DeletePolicy(w http.ResponseWriter, r *http.Request, id gen.Id) {
	if err := h.s.deps.Scopes.DeletePolicy(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) ListFilters(w http.ResponseWriter, r *http.Request) {
	fs, err := h.s.deps.Scopes.ListFilters(r.Context(), "")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]gen.MacFilter, len(fs))
	for i, f := range fs {
		out[i] = toFilter(f)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) CreateFilter(w http.ResponseWriter, r *http.Request) {
	var in gen.MacFilterInput
	if !decode(w, r, &in) {
		return
	}
	f, err := h.s.deps.Scopes.AddFilter(r.Context(), scope.MACFilter{List: string(in.List), Pattern: in.Pattern, Description: str(in.Description)})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toFilter(f))
}

func (h *handlers) DeleteFilter(w http.ResponseWriter, r *http.Request, id gen.Id) {
	if err := h.s.deps.Scopes.DeleteFilter(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
