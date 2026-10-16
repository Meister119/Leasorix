package scope

import (
	"context"
	"database/sql"
	"strings"

	"github.com/meister119/leasorix/internal/apperr"
)

// Superscope groups scopes that serve the same physical network
// (multinet, Windows "superscope"): a client on the link of one member may
// get an address from every member.
type Superscope struct {
	ID          int64
	Name        string
	Description string
	// ScopeIDs are the member scopes (read only; membership is set on the
	// scope).
	ScopeIDs []int64
}

func (s *Superscope) validate() error {
	var v apperr.Validator
	s.Name = strings.TrimSpace(s.Name)
	v.Check(s.Name != "" && len(s.Name) <= maxNameLen, "name", "must be 1-%d characters", maxNameLen)
	v.Check(len(s.Description) <= 1024, "description", "must be at most 1024 characters")
	return v.Err()
}

func superscopeDetails(s Superscope) map[string]any {
	return map[string]any{"name": s.Name, "description": s.Description}
}

// setMembership puts a scope into a superscope (0 = none).
func setMembership(ctx context.Context, tx *sql.Tx, scopeID, superscopeID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM superscope_members WHERE scope_id = ?`, scopeID); err != nil {
		return err
	}
	if superscopeID == 0 {
		return nil
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM superscopes WHERE id = ?`, superscopeID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		var v apperr.Validator
		v.Add("superscope_id", "superscope %d does not exist", superscopeID)
		return v.Err()
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO superscope_members (scope_id, superscope_id) VALUES (?, ?)`, scopeID, superscopeID)
	return err
}

// ListSuperscopes returns all superscopes with their members, by name.
func (s *Store) ListSuperscopes(ctx context.Context) ([]Superscope, error) {
	rows, err := s.db.R.QueryContext(ctx, `
		SELECT ss.id, ss.name, ss.description, m.scope_id
		FROM superscopes ss LEFT JOIN superscope_members m ON m.superscope_id = ss.id
		LEFT JOIN scopes sc ON sc.id = m.scope_id
		ORDER BY ss.name COLLATE NOCASE, sc.network`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Superscope
	for rows.Next() {
		var (
			ss  Superscope
			sid sql.NullInt64
		)
		if err := rows.Scan(&ss.ID, &ss.Name, &ss.Description, &sid); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].ID != ss.ID {
			ss.ScopeIDs = []int64{}
			out = append(out, ss)
		}
		if sid.Valid {
			last := &out[len(out)-1]
			last.ScopeIDs = append(last.ScopeIDs, sid.Int64)
		}
	}
	return out, rows.Err()
}

// GetSuperscope returns one superscope.
func (s *Store) GetSuperscope(ctx context.Context, id int64) (Superscope, error) {
	all, err := s.ListSuperscopes(ctx)
	if err != nil {
		return Superscope{}, err
	}
	for _, ss := range all {
		if ss.ID == id {
			return ss, nil
		}
	}
	return Superscope{}, apperr.NotFoundf("superscope %d", id)
}

func uniqueSuperscope(err error, name string) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return apperr.Conflictf("superscope %q already exists", name)
	}
	return err
}

// CreateSuperscope adds a superscope.
func (s *Store) CreateSuperscope(ctx context.Context, ss Superscope) (Superscope, error) {
	ss.ID, ss.ScopeIDs = 0, []int64{}
	if err := ss.validate(); err != nil {
		return ss, err
	}
	err := s.mutate(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO superscopes (name, description) VALUES (?, ?)`, ss.Name, ss.Description)
		if err != nil {
			return uniqueSuperscope(err, ss.Name)
		}
		if ss.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		return s.rec(ctx, tx, "superscope.create", "superscope", ss.ID, change(nil, superscopeDetails(ss)))
	})
	return ss, err
}

// UpdateSuperscope changes name and description.
func (s *Store) UpdateSuperscope(ctx context.Context, ss Superscope) (Superscope, error) {
	if err := ss.validate(); err != nil {
		return ss, err
	}
	old, err := s.GetSuperscope(ctx, ss.ID)
	if err != nil {
		return ss, err
	}
	err = s.mutate(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE superscopes SET name = ?, description = ? WHERE id = ?`, ss.Name, ss.Description, ss.ID); err != nil {
			return uniqueSuperscope(err, ss.Name)
		}
		return s.rec(ctx, tx, "superscope.update", "superscope", ss.ID, change(superscopeDetails(old), superscopeDetails(ss)))
	})
	ss.ScopeIDs = old.ScopeIDs
	return ss, err
}

// DeleteSuperscope removes a superscope; its member scopes are kept.
func (s *Store) DeleteSuperscope(ctx context.Context, id int64) error {
	old, err := s.GetSuperscope(ctx, id)
	if err != nil {
		return err
	}
	return s.mutate(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM superscopes WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.NotFoundf("superscope %d", id)
		}
		return s.rec(ctx, tx, "superscope.delete", "superscope", id, change(superscopeDetails(old), nil))
	})
}
