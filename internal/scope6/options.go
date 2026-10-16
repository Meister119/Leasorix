package scope6

import (
	"context"
	"database/sql"

	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/dhcpopt6"
)

// OwnerKind is the level an option value is set on.
type OwnerKind string

// Option owners.
const (
	OwnerServer      OwnerKind = "server"
	OwnerScope       OwnerKind = "scope"
	OwnerReservation OwnerKind = "reservation"
)

// Owner identifies the server, a scope or a reservation.
type Owner struct {
	Kind OwnerKind
	ID   int64
}

// ServerOwner is the server level.
var ServerOwner = Owner{Kind: OwnerServer}

// OptionValue is a DHCPv6 option value.
type OptionValue struct {
	ID    int64
	Owner Owner
	Code  uint16
	Type  dhcpopt6.Type
	// Value is the canonical text form.
	Value string
}

func ownerCols(o Owner) (scopeID, resID any) {
	switch o.Kind {
	case OwnerScope:
		return o.ID, nil
	case OwnerReservation:
		return nil, o.ID
	}
	return nil, nil
}

const optionCols = `id, IFNULL(scope_id, 0), IFNULL(reservation_id, 0), code, type, value`

func scanOptions(rows *sql.Rows) ([]OptionValue, error) {
	defer rows.Close()
	out := []OptionValue{}
	for rows.Next() {
		var (
			ov       OptionValue
			sid, rid int64
			code     int
			typ      string
		)
		if err := rows.Scan(&ov.ID, &sid, &rid, &code, &typ, &ov.Value); err != nil {
			return nil, err
		}
		ov.Code, ov.Type = uint16(code), dhcpopt6.Type(typ)
		switch {
		case sid != 0:
			ov.Owner = Owner{OwnerScope, sid}
		case rid != 0:
			ov.Owner = Owner{OwnerReservation, rid}
		default:
			ov.Owner = ServerOwner
		}
		out = append(out, ov)
	}
	return out, rows.Err()
}

func (s *Store) allOptions(ctx context.Context) ([]OptionValue, error) {
	rows, err := s.db.R.QueryContext(ctx, `SELECT `+optionCols+` FROM options6 ORDER BY code`)
	if err != nil {
		return nil, err
	}
	return scanOptions(rows)
}

// ListOptions returns the option values of an owner by code.
func (s *Store) ListOptions(ctx context.Context, o Owner) ([]OptionValue, error) {
	sid, rid := ownerCols(o)
	rows, err := s.db.R.QueryContext(ctx, `SELECT `+optionCols+` FROM options6
		WHERE IFNULL(scope_id, 0) = IFNULL(?, 0) AND IFNULL(reservation_id, 0) = IFNULL(?, 0) ORDER BY code`, sid, rid)
	if err != nil {
		return nil, err
	}
	return scanOptions(rows)
}

func (s *Store) checkOwner(ctx context.Context, o Owner) error {
	var q string
	switch o.Kind {
	case OwnerServer:
		return nil
	case OwnerScope:
		q = `SELECT COUNT(*) FROM scopes6 WHERE id = ?`
	case OwnerReservation:
		q = `SELECT COUNT(*) FROM reservations6 WHERE id = ?`
	default:
		return apperr.NotFoundf("option owner %q", o.Kind)
	}
	var n int
	if err := s.db.R.QueryRowContext(ctx, q, o.ID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return apperr.NotFoundf("%s %d", o.Kind, o.ID)
	}
	return nil
}

func optionDetails(ov OptionValue) map[string]any {
	return map[string]any{"owner": string(ov.Owner.Kind), "owner_id": ov.Owner.ID, "code": ov.Code, "type": string(ov.Type), "value": ov.Value}
}

// SetOption creates or replaces an option value.
func (s *Store) SetOption(ctx context.Context, ov OptionValue) (OptionValue, error) {
	if err := s.checkOwner(ctx, ov.Owner); err != nil {
		return ov, err
	}
	var v apperr.Validator
	t, err := dhcpopt6.TypeFor(ov.Code, ov.Type)
	if err != nil {
		v.Add("code", "%s", err.Error())
		return ov, v.Err()
	}
	canon, _, err := dhcpopt6.Parse(t, ov.Value)
	if err != nil {
		v.Add("value", "%s", err.Error())
		return ov, v.Err()
	}
	ov.Type, ov.Value = t, canon
	sid, rid := ownerCols(ov.Owner)
	err = s.mutate(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM options6 WHERE IFNULL(scope_id, 0) = IFNULL(?, 0) AND IFNULL(reservation_id, 0) = IFNULL(?, 0) AND code = ?`,
			sid, rid, int(ov.Code)); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO options6 (scope_id, reservation_id, code, type, value) VALUES (?, ?, ?, ?, ?)`,
			sid, rid, int(ov.Code), string(ov.Type), ov.Value)
		if err != nil {
			return err
		}
		if ov.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		return rec(ctx, tx, "option6.set", "option6", ov.Code, nil, optionDetails(ov))
	})
	return ov, err
}

// DeleteOption removes an option value.
func (s *Store) DeleteOption(ctx context.Context, o Owner, code uint16) error {
	sid, rid := ownerCols(o)
	return s.mutate(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM options6 WHERE IFNULL(scope_id, 0) = IFNULL(?, 0) AND IFNULL(reservation_id, 0) = IFNULL(?, 0) AND code = ?`,
			sid, rid, int(code))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.NotFoundf("option %d", code)
		}
		return rec(ctx, tx, "option6.delete", "option6", code, map[string]any{"owner": string(o.Kind), "owner_id": o.ID, "code": code}, nil)
	})
}
