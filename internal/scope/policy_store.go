package scope

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/dhcpopt"
	"github.com/meister119/leasorix/internal/ip4"
)

// ---- classes ----------------------------------------------------------------

// ListClasses returns all classes ordered by name.
func (s *Store) ListClasses(ctx context.Context) ([]Class, error) {
	rows, err := s.db.R.QueryContext(ctx, `SELECT id, name, type, data, description FROM classes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Class
	for rows.Next() {
		var c Class
		var typ string
		if err := rows.Scan(&c.ID, &c.Name, &typ, &c.Data, &c.Description); err != nil {
			return nil, err
		}
		c.Type = ClassType(typ)
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateClass adds a vendor or user class.
func (s *Store) CreateClass(ctx context.Context, c Class) (Class, error) {
	if err := c.validate(); err != nil {
		return c, err
	}
	err := s.mutate(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM classes WHERE name = ?`, c.Name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return apperr.Conflictf("class %q already exists", c.Name)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO classes (name, type, data, description) VALUES (?, ?, ?, ?)`,
			c.Name, string(c.Type), c.Data, c.Description)
		if err != nil {
			return err
		}
		c.ID, err = res.LastInsertId()
		return err
	})
	return c, err
}

// UpdateClass changes a class. Renaming updates referencing conditions.
func (s *Store) UpdateClass(ctx context.Context, c Class) (Class, error) {
	if err := c.validate(); err != nil {
		return c, err
	}
	err := s.mutate(ctx, func(tx *sql.Tx) error {
		var oldName string
		err := tx.QueryRowContext(ctx, `SELECT name FROM classes WHERE id = ?`, c.ID).Scan(&oldName)
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFoundf("class %d", c.ID)
		}
		if err != nil {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM classes WHERE name = ? AND id <> ?`, c.Name, c.ID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return apperr.Conflictf("class %q already exists", c.Name)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE classes SET name = ?, type = ?, data = ?, description = ? WHERE id = ?`,
			c.Name, string(c.Type), c.Data, c.Description, c.ID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE policy_conditions SET value = ? WHERE field = 'class' AND value = ? COLLATE NOCASE`, c.Name, oldName)
		return err
	})
	return c, err
}

// DeleteClass removes a class that no policy refers to.
func (s *Store) DeleteClass(ctx context.Context, id int64) error {
	return s.mutate(ctx, func(tx *sql.Tx) error {
		var name string
		err := tx.QueryRowContext(ctx, `SELECT name FROM classes WHERE id = ?`, id).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFoundf("class %d", id)
		}
		if err != nil {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM policy_conditions WHERE field = 'class' AND value = ? COLLATE NOCASE`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return apperr.Conflictf("class %q is used by %d policy condition(s)", name, n)
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM classes WHERE id = ?`, id)
		return err
	})
}

// ---- policies ----------------------------------------------------------------

func nullScope(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// ListPolicies returns policies of a scope (scopeID > 0), server level
// policies (scopeID == 0) or all policies (scopeID < 0), ordered by level
// and processing order.
func (s *Store) ListPolicies(ctx context.Context, scopeID int64) ([]Policy, error) {
	return listPolicies(ctx, s.db.R, scopeID)
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func listPolicies(ctx context.Context, q querier, scopeID int64) ([]Policy, error) {
	query := `SELECT id, IFNULL(scope_id, 0), name, description, processing_order, enabled, operator, lease_time, created_at, updated_at FROM policies`
	var args []any
	switch {
	case scopeID > 0:
		query += ` WHERE scope_id = ?`
		args = append(args, scopeID)
	case scopeID == 0:
		query += ` WHERE scope_id IS NULL`
	}
	query += ` ORDER BY IFNULL(scope_id, 0), processing_order, id`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var out []Policy
	byID := map[int64]int{}
	for rows.Next() {
		var (
			p                Policy
			lease            sql.NullInt64
			created, updated int64
		)
		if err := rows.Scan(&p.ID, &p.ScopeID, &p.Name, &p.Description, &p.Order, &p.Enabled, &p.Operator, &lease, &created, &updated); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if lease.Valid {
			lt := time.Duration(lease.Int64) * time.Second
			p.LeaseTime = &lt
		}
		p.CreatedAt, p.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		byID[p.ID] = len(out)
		out = append(out, p)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	crows, err := q.QueryContext(ctx, `SELECT policy_id, field, operator, value FROM policy_conditions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for crows.Next() {
		var pid int64
		var c Condition
		var field string
		if err := crows.Scan(&pid, &field, &c.Operator, &c.Value); err != nil {
			_ = crows.Close()
			return nil, err
		}
		c.Field = ConditionField(field)
		if i, ok := byID[pid]; ok {
			out[i].Conditions = append(out[i].Conditions, c)
		}
	}
	_ = crows.Close()
	rrows, err := q.QueryContext(ctx, `SELECT policy_id, start_ip, end_ip FROM policy_ranges ORDER BY start_ip`)
	if err != nil {
		return nil, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var pid, start, end int64
		if err := rrows.Scan(&pid, &start, &end); err != nil {
			return nil, err
		}
		if i, ok := byID[pid]; ok {
			out[i].Ranges = append(out[i].Ranges, AddrRange{Start: ip4.ToAddr(uint32(start)), End: ip4.ToAddr(uint32(end))})
		}
	}
	return out, rrows.Err()
}

// GetPolicy returns one policy.
func (s *Store) GetPolicy(ctx context.Context, id int64) (Policy, error) {
	all, err := s.ListPolicies(ctx, -1)
	if err != nil {
		return Policy{}, err
	}
	for _, p := range all {
		if p.ID == id {
			return p, nil
		}
	}
	return Policy{}, apperr.NotFoundf("policy %d", id)
}

func (s *Store) checkPolicy(ctx context.Context, tx *sql.Tx, p *Policy) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM policies WHERE IFNULL(scope_id, 0) = ? AND name = ? AND id <> ?`,
		p.ScopeID, p.Name, p.ID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return apperr.Conflictf("policy %q already exists at this level", p.Name)
	}
	for _, c := range p.Conditions {
		if c.Field != FieldClass {
			continue
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM classes WHERE name = ?`, c.Value).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			var v apperr.Validator
			v.Add("conditions", "class %q does not exist", c.Value)
			return v.Err()
		}
	}
	if p.ScopeID == 0 {
		return nil
	}
	sc, err := scanScope(tx.QueryRowContext(ctx, `SELECT `+scopeCols+` FROM scopes WHERE id = ?`, p.ScopeID))
	if errors.Is(err, sql.ErrNoRows) {
		return apperr.NotFoundf("scope %d", p.ScopeID)
	}
	if err != nil {
		return err
	}
	scopeRange := sc.Range()
	for _, r := range p.Ranges {
		ri := r.ints()
		if !scopeRange.Contains(ri.Start) || !scopeRange.Contains(ri.End) {
			var v apperr.Validator
			v.Add("ranges", "range %s must lie within the scope range %s", ri, scopeRange)
			return v.Err()
		}
		var other string
		err := tx.QueryRowContext(ctx, `
			SELECT p.name FROM policy_ranges r JOIN policies p ON p.id = r.policy_id
			WHERE p.scope_id = ? AND p.id <> ? AND r.start_ip <= ? AND r.end_ip >= ? LIMIT 1`,
			p.ScopeID, p.ID, ri.End, ri.Start).Scan(&other)
		if err == nil {
			return apperr.Conflictf("range %s overlaps a range of policy %q", ri, other)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

func writePolicyChildren(ctx context.Context, tx *sql.Tx, p *Policy) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM policy_conditions WHERE policy_id = ?`, p.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM policy_ranges WHERE policy_id = ?`, p.ID); err != nil {
		return err
	}
	for _, c := range p.Conditions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO policy_conditions (policy_id, field, operator, value) VALUES (?, ?, ?, ?)`,
			p.ID, string(c.Field), c.Operator, c.Value); err != nil {
			return err
		}
	}
	for _, r := range p.Ranges {
		ri := r.ints()
		if _, err := tx.ExecContext(ctx, `INSERT INTO policy_ranges (policy_id, start_ip, end_ip) VALUES (?, ?, ?)`, p.ID, ri.Start, ri.End); err != nil {
			return err
		}
	}
	return nil
}

func leaseArg(lt *time.Duration) any {
	if lt == nil {
		return nil
	}
	return int64(*lt / time.Second)
}

// CreatePolicy adds a policy with its conditions and ranges. Order 0 means
// "append after the last policy of the level".
func (s *Store) CreatePolicy(ctx context.Context, p Policy) (Policy, error) {
	p.ID = 0
	if err := p.normalize(); err != nil {
		return p, err
	}
	now := s.now().UTC().Truncate(time.Second)
	p.CreatedAt, p.UpdatedAt = now, now
	err := s.mutate(ctx, func(tx *sql.Tx) error {
		if err := s.checkPolicy(ctx, tx, &p); err != nil {
			return err
		}
		if p.Order <= 0 {
			if err := tx.QueryRowContext(ctx, `SELECT IFNULL(MAX(processing_order), 0) + 1 FROM policies WHERE IFNULL(scope_id, 0) = ?`, p.ScopeID).Scan(&p.Order); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO policies (scope_id, name, description, processing_order, enabled, operator, lease_time, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nullScope(p.ScopeID), p.Name, p.Description, p.Order, p.Enabled, p.Operator, leaseArg(p.LeaseTime), now.Unix(), now.Unix())
		if err != nil {
			return err
		}
		if p.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		return writePolicyChildren(ctx, tx, &p)
	})
	return p, err
}

// UpdatePolicy replaces a policy including its conditions and ranges. The
// level (scope) of a policy cannot change.
func (s *Store) UpdatePolicy(ctx context.Context, p Policy) (Policy, error) {
	err := s.mutate(ctx, func(tx *sql.Tx) error {
		var scopeID int64
		var created int64
		err := tx.QueryRowContext(ctx, `SELECT IFNULL(scope_id, 0), created_at FROM policies WHERE id = ?`, p.ID).Scan(&scopeID, &created)
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFoundf("policy %d", p.ID)
		}
		if err != nil {
			return err
		}
		p.ScopeID = scopeID
		if err := p.normalize(); err != nil {
			return err
		}
		if err := s.checkPolicy(ctx, tx, &p); err != nil {
			return err
		}
		if p.Order <= 0 {
			var v apperr.Validator
			v.Add("order", "must be positive")
			return v.Err()
		}
		p.CreatedAt = time.Unix(created, 0).UTC()
		p.UpdatedAt = s.now().UTC().Truncate(time.Second)
		if _, err := tx.ExecContext(ctx, `
			UPDATE policies SET name = ?, description = ?, processing_order = ?, enabled = ?, operator = ?, lease_time = ?, updated_at = ?
			WHERE id = ?`,
			p.Name, p.Description, p.Order, p.Enabled, p.Operator, leaseArg(p.LeaseTime), p.UpdatedAt.Unix(), p.ID); err != nil {
			return err
		}
		return writePolicyChildren(ctx, tx, &p)
	})
	return p, err
}

// DeletePolicy removes a policy, its conditions, ranges and options.
func (s *Store) DeletePolicy(ctx context.Context, id int64) error {
	return s.mutate(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM policies WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.NotFoundf("policy %d", id)
		}
		return nil
	})
}

// ---- option definitions -------------------------------------------------------

// ListOptionDefinitions returns built-in and custom definitions by code.
func (s *Store) ListOptionDefinitions(ctx context.Context) ([]dhcpopt.Definition, error) {
	custom, err := s.listCustomDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	out := dhcpopt.BuiltinDefinitions()
	out = append(out, custom...)
	sortDefs(out)
	return out, nil
}

func (s *Store) listCustomDefinitions(ctx context.Context) ([]dhcpopt.Definition, error) {
	rows, err := s.db.R.QueryContext(ctx, `SELECT code, name, type, description FROM option_definitions ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dhcpopt.Definition
	for rows.Next() {
		var d dhcpopt.Definition
		var code int
		var typ string
		if err := rows.Scan(&code, &d.Name, &typ, &d.Description); err != nil {
			return nil, err
		}
		d.Code, d.Type = uint8(code), dhcpopt.Type(typ)
		out = append(out, d)
	}
	return out, rows.Err()
}

// CreateOptionDefinition defines a custom option code.
func (s *Store) CreateOptionDefinition(ctx context.Context, d dhcpopt.Definition) (dhcpopt.Definition, error) {
	var v apperr.Validator
	d.Name = strings.TrimSpace(d.Name)
	d.Builtin = false
	v.Check(d.Code >= 1 && d.Code <= 254, "code", "must be between 1 and 254")
	if _, ok := dhcpopt.Builtin(d.Code); ok {
		v.Add("code", "option %d is a built-in option", d.Code)
	}
	if why, ok := dhcpopt.Managed[d.Code]; ok {
		v.Add("code", "option %d is managed by the server (%s)", d.Code, why)
	}
	v.Check(d.Name != "" && len(d.Name) <= maxNameLen, "name", "must be 1-%d characters", maxNameLen)
	v.Check(d.Type.Valid(), "type", "unknown type %q", d.Type)
	if err := v.Err(); err != nil {
		return d, err
	}
	err := s.mutate(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM option_definitions WHERE code = ? OR name = ?`, d.Code, d.Name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return apperr.Conflictf("option %d or name %q already defined", d.Code, d.Name)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO option_definitions (code, name, type, description) VALUES (?, ?, ?, ?)`,
			d.Code, d.Name, string(d.Type), d.Description)
		return err
	})
	return d, err
}

// DeleteOptionDefinition removes a custom definition that has no values.
func (s *Store) DeleteOptionDefinition(ctx context.Context, code uint8) error {
	return s.mutate(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM options WHERE code = ?`, code).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return apperr.Conflictf("option %d still has %d configured value(s)", code, n)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM option_definitions WHERE code = ?`, code)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.NotFoundf("custom option definition %d", code)
		}
		return nil
	})
}

// ---- option values -------------------------------------------------------------

func ownerCols(o Owner) (scopeID, resID, policyID any) {
	switch o.Kind {
	case OwnerScope:
		return o.ID, nil, nil
	case OwnerReservation:
		return nil, o.ID, nil
	case OwnerPolicy:
		return nil, nil, o.ID
	}
	return nil, nil, nil
}

func ownerFrom(scopeID, resID, policyID sql.NullInt64) Owner {
	switch {
	case scopeID.Valid:
		return Owner{Kind: OwnerScope, ID: scopeID.Int64}
	case resID.Valid:
		return Owner{Kind: OwnerReservation, ID: resID.Int64}
	case policyID.Valid:
		return Owner{Kind: OwnerPolicy, ID: policyID.Int64}
	}
	return ServerOwner
}

func ownerWhere(o Owner) (string, []any) {
	switch o.Kind {
	case OwnerScope:
		return `scope_id = ?`, []any{o.ID}
	case OwnerReservation:
		return `reservation_id = ?`, []any{o.ID}
	case OwnerPolicy:
		return `policy_id = ?`, []any{o.ID}
	}
	return `scope_id IS NULL AND reservation_id IS NULL AND policy_id IS NULL`, nil
}

func (s *Store) checkOwner(ctx context.Context, tx *sql.Tx, o Owner) error {
	table := map[OwnerKind]string{OwnerScope: "scopes", OwnerReservation: "reservations", OwnerPolicy: "policies"}[o.Kind]
	switch o.Kind {
	case OwnerServer:
		return nil
	case OwnerScope, OwnerReservation, OwnerPolicy:
	default:
		var v apperr.Validator
		v.Add("owner", "unknown owner kind %q", o.Kind)
		return v.Err()
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE id = ?`, o.ID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return apperr.NotFoundf("%s %d", o.Kind, o.ID)
	}
	return nil
}

func (s *Store) definition(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, code uint8) (dhcpopt.Definition, bool, error) {
	if d, ok := dhcpopt.Builtin(code); ok {
		return d, true, nil
	}
	var d dhcpopt.Definition
	var typ string
	err := q.QueryRowContext(ctx, `SELECT name, type, description FROM option_definitions WHERE code = ?`, code).Scan(&d.Name, &typ, &d.Description)
	if errors.Is(err, sql.ErrNoRows) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	d.Code, d.Type = code, dhcpopt.Type(typ)
	return d, true, nil
}

// ListOptions returns the option values of an owner ordered by code.
func (s *Store) ListOptions(ctx context.Context, o Owner) ([]OptionValue, error) {
	where, args := ownerWhere(o)
	return s.queryOptions(ctx, `WHERE `+where, args...)
}

func (s *Store) queryOptions(ctx context.Context, where string, args ...any) ([]OptionValue, error) {
	rows, err := s.db.R.QueryContext(ctx, `SELECT id, scope_id, reservation_id, policy_id, code, type, value FROM options `+where+` ORDER BY code`, args...) //nolint:gosec // where is a constant fragment from ownerWhere
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OptionValue
	for rows.Next() {
		var (
			ov            OptionValue
			sid, rid, pid sql.NullInt64
			code          int
			typ           string
		)
		if err := rows.Scan(&ov.ID, &sid, &rid, &pid, &code, &typ, &ov.Value); err != nil {
			return nil, err
		}
		ov.Owner, ov.Code, ov.Type = ownerFrom(sid, rid, pid), uint8(code), dhcpopt.Type(typ)
		out = append(out, ov)
	}
	return out, rows.Err()
}

// SetOption creates or replaces the value of an option for an owner. An
// empty type defaults to the type of the option definition.
func (s *Store) SetOption(ctx context.Context, ov OptionValue) (OptionValue, error) {
	var v apperr.Validator
	if why, ok := dhcpopt.Managed[ov.Code]; ok || ov.Code == 0 {
		v.Add("code", "option %d cannot be configured (%s)", ov.Code, why)
		return ov, v.Err()
	}
	err := s.mutate(ctx, func(tx *sql.Tx) error {
		if err := s.checkOwner(ctx, tx, ov.Owner); err != nil {
			return err
		}
		def, ok, err := s.definition(ctx, tx, ov.Code)
		if err != nil {
			return err
		}
		var v apperr.Validator
		if !ok {
			v.Add("code", "option %d is not defined; create an option definition first", ov.Code)
			return v.Err()
		}
		if ov.Type == "" {
			ov.Type = def.Type
		}
		if ov.Type != def.Type {
			v.Add("type", "option %d (%s) has type %s", ov.Code, def.Name, def.Type)
			return v.Err()
		}
		canon, _, err := dhcpopt.Parse(ov.Type, ov.Value)
		if err != nil {
			v.Add("value", "%v", err)
			return v.Err()
		}
		ov.Value = canon
		where, args := ownerWhere(ov.Owner)
		if _, err := tx.ExecContext(ctx, `DELETE FROM options WHERE code = ? AND `+where, append([]any{ov.Code}, args...)...); err != nil { //nolint:gosec // constant fragment from ownerWhere
			return err
		}
		sid, rid, pid := ownerCols(ov.Owner)
		res, err := tx.ExecContext(ctx, `INSERT INTO options (scope_id, reservation_id, policy_id, code, type, value) VALUES (?, ?, ?, ?, ?, ?)`,
			sid, rid, pid, ov.Code, string(ov.Type), ov.Value)
		if err != nil {
			return err
		}
		ov.ID, err = res.LastInsertId()
		return err
	})
	return ov, err
}

// DeleteOption removes an option value of an owner.
func (s *Store) DeleteOption(ctx context.Context, o Owner, code uint8) error {
	return s.mutate(ctx, func(tx *sql.Tx) error {
		where, args := ownerWhere(o)
		res, err := tx.ExecContext(ctx, `DELETE FROM options WHERE code = ? AND `+where, append([]any{code}, args...)...) //nolint:gosec // constant fragment from ownerWhere
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.NotFoundf("option %d of %s", code, o.Kind)
		}
		return nil
	})
}

// ---- MAC filters ---------------------------------------------------------------

// ListFilters returns the entries of a filter list ("" = both).
func (s *Store) ListFilters(ctx context.Context, list string) ([]MACFilter, error) {
	q := `SELECT id, list, pattern, description, created_at FROM mac_filters`
	var args []any
	if list != "" {
		q += ` WHERE list = ?`
		args = append(args, list)
	}
	rows, err := s.db.R.QueryContext(ctx, q+` ORDER BY list, pattern`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MACFilter
	for rows.Next() {
		var f MACFilter
		var created int64
		if err := rows.Scan(&f.ID, &f.List, &f.Pattern, &f.Description, &created); err != nil {
			return nil, err
		}
		f.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, f)
	}
	return out, rows.Err()
}

// AddFilter adds a MAC (or wildcard pattern) to the allow or deny list.
func (s *Store) AddFilter(ctx context.Context, f MACFilter) (MACFilter, error) {
	var v apperr.Validator
	v.Check(f.List == FilterAllow || f.List == FilterDeny, "list", "must be allow or deny")
	pat, err := normalizeMACPattern(f.Pattern)
	if err != nil {
		v.Add("pattern", "%v", err)
	}
	if err := v.Err(); err != nil {
		return f, err
	}
	f.Pattern = pat
	f.CreatedAt = s.now().UTC().Truncate(time.Second)
	err = s.mutate(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mac_filters WHERE list = ? AND pattern = ?`, f.List, f.Pattern).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return apperr.Conflictf("%s is already on the %s list", f.Pattern, f.List)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO mac_filters (list, pattern, description, created_at) VALUES (?, ?, ?, ?)`,
			f.List, f.Pattern, f.Description, f.CreatedAt.Unix())
		if err != nil {
			return err
		}
		f.ID, err = res.LastInsertId()
		return err
	})
	return f, err
}

// DeleteFilter removes a filter entry.
func (s *Store) DeleteFilter(ctx context.Context, id int64) error {
	return s.mutate(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM mac_filters WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.NotFoundf("filter %d", id)
		}
		return nil
	})
}

// Settings keys for the filter lists.
const (
	settingAllowEnabled = "filter.allow_enabled"
	settingDenyEnabled  = "filter.deny_enabled"
)

func (s *Store) readFilterSettings(ctx context.Context) (FilterSettings, error) {
	var fs FilterSettings
	rows, err := s.db.R.QueryContext(ctx, `SELECT key, value FROM settings WHERE key IN (?, ?)`, settingAllowEnabled, settingDenyEnabled)
	if err != nil {
		return fs, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return fs, err
		}
		switch k {
		case settingAllowEnabled:
			fs.AllowEnabled = v == "true"
		case settingDenyEnabled:
			fs.DenyEnabled = v == "true"
		}
	}
	return fs, rows.Err()
}

// FilterSettings returns which filter lists are enabled.
func (s *Store) FilterSettings(ctx context.Context) (FilterSettings, error) {
	return s.readFilterSettings(ctx)
}

// SetFilterSettings enables or disables the filter lists.
func (s *Store) SetFilterSettings(ctx context.Context, fs FilterSettings) error {
	return s.mutate(ctx, func(tx *sql.Tx) error {
		for k, v := range map[string]bool{settingAllowEnabled: fs.AllowEnabled, settingDenyEnabled: fs.DenyEnabled} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
				ON CONFLICT (key) DO UPDATE SET value = excluded.value`, k, fmt.Sprint(v)); err != nil {
				return err
			}
		}
		return nil
	})
}
