package scope

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/meister119/leasorix/internal/audit"
)

// rec writes an audit entry inside the mutation's transaction, so the
// change and its audit record are committed atomically.
func (s *Store) rec(ctx context.Context, tx *sql.Tx, action, objType string, id any, details map[string]any) error {
	return audit.Insert(ctx, tx, audit.New(ctx, action, objType, fmt.Sprint(id), details))
}

func change(before, after map[string]any) map[string]any {
	m := map[string]any{}
	if before != nil {
		m["before"] = before
	}
	if after != nil {
		m["after"] = after
	}
	return m
}

func leaseSeconds(sc Scope) int64 { return int64(sc.LeaseTime.Seconds()) }

func scopeDetails(sc Scope) map[string]any {
	return map[string]any{
		"name": sc.Name, "description": sc.Description, "subnet": sc.Subnet.String(),
		"start": sc.Start.String(), "end": sc.End.String(), "lease_time": leaseSeconds(sc), "enabled": sc.Enabled,
		"bootp": sc.BOOTP, "superscope_id": sc.SuperscopeID,
		"dns": map[string]any{"mode": string(sc.DNS.Mode), "domain": sc.DNS.Domain, "ttl": int64(sc.DNS.TTL / time.Second),
			"cleanup": sc.DNS.Cleanup, "protect": sc.DNS.Protect},
	}
}

func exclusionDetails(e Exclusion) map[string]any {
	return map[string]any{"scope_id": e.ScopeID, "start": e.Start.String(), "end": e.End.String()}
}

func reservationDetails(r Reservation) map[string]any {
	return map[string]any{
		"scope_id": r.ScopeID, "ip": r.IP.String(), "mac": r.MAC, "client_id": r.ClientID,
		"hostname": r.Hostname, "description": r.Description,
	}
}

func classDetails(c Class) map[string]any {
	return map[string]any{"name": c.Name, "type": string(c.Type), "data": c.Data, "description": c.Description}
}

func policyDetails(p Policy) map[string]any {
	conds := make([]map[string]string, len(p.Conditions))
	for i, c := range p.Conditions {
		conds[i] = map[string]string{"field": string(c.Field), "operator": c.Operator, "value": c.Value}
	}
	ranges := make([]string, len(p.Ranges))
	for i, r := range p.Ranges {
		ranges[i] = r.Start.String() + "-" + r.End.String()
	}
	m := map[string]any{
		"name": p.Name, "scope_id": p.ScopeID, "order": p.Order, "enabled": p.Enabled,
		"operator": p.Operator, "conditions": conds, "ranges": ranges, "description": p.Description,
	}
	if p.LeaseTime != nil {
		m["lease_time"] = int64(p.LeaseTime.Seconds())
	}
	return m
}

func optionDetails(ov OptionValue) map[string]any {
	return map[string]any{"owner": string(ov.Owner.Kind), "owner_id": ov.Owner.ID, "code": ov.Code, "type": string(ov.Type), "value": ov.Value}
}

func optionObjectID(o Owner, code uint8) string {
	if o.Kind == OwnerServer {
		return fmt.Sprintf("server/%d", code)
	}
	return fmt.Sprintf("%s/%d/%d", o.Kind, o.ID, code)
}
