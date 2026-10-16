package scope

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/meister119/leasorix/internal/apperr"
	"github.com/meister119/leasorix/internal/dhcpopt"
	"github.com/meister119/leasorix/internal/ip4"
	"github.com/meister119/leasorix/internal/macaddr"
)

// OwnerKind is the level an option value is attached to.
type OwnerKind string

// Option owner levels, from least to most specific.
const (
	OwnerServer      OwnerKind = "server"
	OwnerScope       OwnerKind = "scope"
	OwnerPolicy      OwnerKind = "policy"
	OwnerReservation OwnerKind = "reservation"
)

// Owner identifies the object an option value belongs to. ID is 0 for the
// server level.
type Owner struct {
	Kind OwnerKind
	ID   int64
}

// ServerOwner is the server level owner.
var ServerOwner = Owner{Kind: OwnerServer}

// OptionValue is a configured option.
type OptionValue struct {
	ID    int64
	Owner Owner
	Code  uint8
	Type  dhcpopt.Type
	// Value is the canonical text form (see dhcpopt.Parse).
	Value string
}

// ClassType distinguishes vendor and user classes.
type ClassType string

// Class types.
const (
	ClassVendor ClassType = "vendor" // matches option 60
	ClassUser   ClassType = "user"   // matches option 77
)

// Class is a named vendor or user class definition that policies can refer
// to. Data is a match pattern (wildcards * and ?).
type Class struct {
	ID          int64
	Name        string
	Type        ClassType
	Data        string
	Description string
}

func (c *Class) validate() error {
	var v apperr.Validator
	c.Name = strings.TrimSpace(c.Name)
	v.Check(c.Name != "" && len(c.Name) <= maxNameLen, "name", "must be 1-%d characters", maxNameLen)
	v.Check(c.Type == ClassVendor || c.Type == ClassUser, "type", "must be vendor or user")
	v.Check(c.Data != "" && len(c.Data) <= 255, "data", "must be 1-255 characters")
	return v.Err()
}

// ConditionField is the client attribute a policy condition tests.
type ConditionField string

// Condition fields.
const (
	FieldMAC          ConditionField = "mac"                 // aa:bb:cc:dd:ee:ff, wildcards allowed
	FieldClientID     ConditionField = "client_id"           // option 61 as hex
	FieldHostname     ConditionField = "hostname"            // option 12
	FieldVendorClass  ConditionField = "vendor_class"        // option 60
	FieldUserClass    ConditionField = "user_class"          // option 77
	FieldClass        ConditionField = "class"               // name of a defined class
	FieldCircuitID    ConditionField = "relay_circuit_id"    // option 82.1
	FieldRemoteID     ConditionField = "relay_remote_id"     // option 82.2
	FieldSubscriberID ConditionField = "relay_subscriber_id" // option 82.6
)

// ConditionFields lists all supported fields.
var ConditionFields = []ConditionField{FieldMAC, FieldClientID, FieldHostname, FieldVendorClass, FieldUserClass, FieldClass, FieldCircuitID, FieldRemoteID, FieldSubscriberID}

// Condition operators.
const (
	OpEqual    = "eq"
	OpNotEqual = "ne"
)

// Condition is one test of a policy. Values may contain the wildcards *
// and ?. For relay fields and the client id a value prefixed with "0x" is
// compared against the hex representation.
type Condition struct {
	Field    ConditionField
	Operator string
	Value    string
}

// AddrRange is an inclusive address range.
type AddrRange struct {
	Start netip.Addr
	End   netip.Addr
}

func (r AddrRange) ints() ip4.Range {
	return ip4.Range{Start: ip4.MustFromAddr(r.Start), End: ip4.MustFromAddr(r.End)}
}

// Policy assigns options, a lease time and optionally an own address range
// to clients matching its conditions (Windows DHCP policies).
type Policy struct {
	ID int64
	// ScopeID is 0 for server level policies.
	ScopeID     int64
	Name        string
	Description string
	// Order is the processing order; lower values are evaluated first and
	// win on conflicting options.
	Order   int
	Enabled bool
	// Operator combines the conditions: "and" or "or".
	Operator string
	// LeaseTime overrides the scope lease time when set (0 = unlimited).
	LeaseTime  *time.Duration
	Conditions []Condition
	// Ranges are only allowed for scope level policies. Addresses in these
	// ranges are only leased to matching clients.
	Ranges    []AddrRange
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Operators for combining conditions.
const (
	CombineAnd = "and"
	CombineOr  = "or"
)

func validField(f ConditionField) bool {
	for _, k := range ConditionFields {
		if k == f {
			return true
		}
	}
	return false
}

// normalize validates the policy in isolation and canonicalises values.
func (p *Policy) normalize() error {
	var v apperr.Validator
	p.Name = strings.TrimSpace(p.Name)
	v.Check(p.Name != "" && len(p.Name) <= maxNameLen, "name", "must be 1-%d characters", maxNameLen)
	if p.Operator == "" {
		p.Operator = CombineAnd
	}
	v.Check(p.Operator == CombineAnd || p.Operator == CombineOr, "operator", "must be and or or")
	if p.LeaseTime != nil {
		lt := *p.LeaseTime
		v.Check(lt == 0 || lt >= MinLeaseTime, "lease_time", "must be 0 (unlimited) or at least %s", MinLeaseTime)
		v.Check(lt%time.Second == 0, "lease_time", "must be a whole number of seconds")
	}
	v.Check(len(p.Conditions) > 0, "conditions", "at least one condition is required")
	for i := range p.Conditions {
		c := &p.Conditions[i]
		field := "conditions[" + strconv.Itoa(i) + "]"
		if !validField(c.Field) {
			v.Add(field+".field", "unknown field %q", c.Field)
			continue
		}
		if c.Operator == "" {
			c.Operator = OpEqual
		}
		v.Check(c.Operator == OpEqual || c.Operator == OpNotEqual, field+".operator", "must be eq or ne")
		c.Value = strings.TrimSpace(c.Value)
		if c.Value == "" || len(c.Value) > 255 {
			v.Add(field+".value", "must be 1-255 characters")
			continue
		}
		switch c.Field {
		case FieldMAC:
			val, err := normalizeMACPattern(c.Value)
			if err != nil {
				v.Add(field+".value", "%v", err)
			}
			c.Value = val
		case FieldClientID:
			c.Value = strings.ToLower(strings.NewReplacer(":", "", "-", "").Replace(c.Value))
		}
	}
	if p.ScopeID == 0 && len(p.Ranges) > 0 {
		v.Add("ranges", "address ranges are only allowed for scope level policies")
	}
	for i, r := range p.Ranges {
		field := "ranges[" + strconv.Itoa(i) + "]"
		if !r.Start.Is4() || !r.End.Is4() {
			v.Add(field, "must be IPv4 addresses")
			continue
		}
		v.Check(ip4.MustFromAddr(r.Start) <= ip4.MustFromAddr(r.End), field, "end must not be lower than start")
	}
	for i := range p.Ranges {
		for j := i + 1; j < len(p.Ranges); j++ {
			if p.Ranges[i].Start.Is4() && p.Ranges[j].Start.Is4() && p.Ranges[i].ints().Overlaps(p.Ranges[j].ints()) {
				v.Add("ranges", "ranges %d and %d overlap", i, j)
			}
		}
	}
	return v.Err()
}

// normalizeMACPattern canonicalises a MAC address or wildcard pattern.
// NormalizeMACPattern validates a MAC filter pattern (MAC address or prefix
// with wildcards) and returns its canonical form.
func NormalizeMACPattern(s string) (string, error) { return normalizeMACPattern(s) }

func normalizeMACPattern(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !strings.ContainsAny(s, "*?") {
		return macaddr.Normalize(s)
	}
	s = strings.ReplaceAll(s, "-", ":")
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && r != ':' && r != '*' && r != '?' {
			return "", fmt.Errorf("invalid MAC pattern %q (use e.g. 00:15:5d:*)", s)
		}
	}
	return s, nil
}

// Filter lists.
const (
	FilterAllow = "allow"
	FilterDeny  = "deny"
)

// MACFilter is an entry of the allow or deny list.
type MACFilter struct {
	ID          int64
	List        string
	Pattern     string
	Description string
	CreatedAt   time.Time
}

// FilterSettings enables the filter lists.
type FilterSettings struct {
	AllowEnabled bool
	DenyEnabled  bool
}
