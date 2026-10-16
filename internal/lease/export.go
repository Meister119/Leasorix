package lease

import (
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"time"

	"github.com/meister119/leasorix/internal/ip4"
)

// ExportFormat identifies the lease export JSON format.
const ExportFormat = "leasorix-leases/v1"

// ExportFile is the emergency export of the lease table. It can be imported
// on the same or another server (records are merged, see Wins).
type ExportFile struct {
	Format     string         `json:"format"`
	ExportedAt time.Time      `json:"exported_at"`
	Node       string         `json:"node,omitempty"`
	Leases     []ExportRecord `json:"leases"`
}

// ExportRecord is one lease record.
type ExportRecord struct {
	IP       string    `json:"ip"`
	MAC      string    `json:"mac"`
	ClientID string    `json:"client_id,omitempty"`
	Hostname string    `json:"hostname,omitempty"`
	State    State     `json:"state"`
	Start    time.Time `json:"start"`
	Expires  time.Time `json:"expires"`
	Updated  time.Time `json:"updated"`
	Origin   string    `json:"origin,omitempty"`
}

// NewExport builds an export of the given records.
func NewExport(node string, leases []Lease, now time.Time) ExportFile {
	f := ExportFile{Format: ExportFormat, ExportedAt: now.UTC(), Node: node, Leases: make([]ExportRecord, 0, len(leases))}
	for _, l := range leases {
		f.Leases = append(f.Leases, ExportRecord{
			IP: ip4.String(l.IP), MAC: l.MAC, ClientID: l.ClientID, Hostname: l.Hostname, State: l.State,
			Start: l.Start.UTC(), Expires: l.Expires.UTC(), Updated: l.Updated.UTC(), Origin: l.Origin,
		})
	}
	return f
}

// ReadExport parses and validates an export.
func ReadExport(r io.Reader) (ExportFile, error) {
	var f ExportFile
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("invalid lease export: %w", err)
	}
	if f.Format != ExportFormat {
		return f, fmt.Errorf("unsupported lease export format %q (expected %s)", f.Format, ExportFormat)
	}
	return f, nil
}

// Records converts the export into lease records (scope ids are resolved
// on import).
func (f ExportFile) Records() ([]Lease, error) {
	out := make([]Lease, 0, len(f.Leases))
	for i, r := range f.Leases {
		a, err := netip.ParseAddr(r.IP)
		if err != nil || !a.Is4() {
			return nil, fmt.Errorf("record %d: invalid address %q", i+1, r.IP)
		}
		switch r.State {
		case StateActive, StateExpired, StateReleased, StateDeclined:
		default:
			return nil, fmt.Errorf("record %d: invalid state %q", i+1, r.State)
		}
		if r.State != StateDeclined && r.MAC == "" && r.ClientID == "" {
			return nil, fmt.Errorf("record %d: mac or client_id required", i+1)
		}
		out = append(out, Lease{
			IP: ip4.MustFromAddr(a), MAC: r.MAC, ClientID: r.ClientID, Hostname: r.Hostname, State: r.State,
			Start: r.Start.UTC().Truncate(time.Second), Expires: r.Expires.UTC().Truncate(time.Second),
			Updated: r.Updated.UTC().Truncate(time.Millisecond), Origin: r.Origin,
		})
	}
	return out, nil
}
