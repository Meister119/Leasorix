package dhcp4

import (
	"log/slog"
	"net"
	"sort"

	"github.com/insomniacslk/dhcp/dhcpv4"

	"github.com/meister119/leasorix/internal/lease"
	"github.com/meister119/leasorix/internal/scope"
)

// minMaxMessageSize is the smallest DHCP message every client must accept
// (RFC 2132 9.10: 576 bytes IP datagram).
const minMaxMessageSize = 576

// ipUDPHeaders is the IP + UDP header overhead included in option 57.
const ipUDPHeaders = 28

// clientInfoOf extracts the attributes policies look at.
func clientInfoOf(m *dhcpv4.DHCPv4, c lease.Client) *scope.ClientInfo {
	ci := &scope.ClientInfo{
		MAC:         c.MAC,
		ClientID:    c.ClientID,
		Hostname:    c.Hostname,
		VendorClass: m.ClassIdentifier(),
		UserClasses: scope.ParseUserClass(m.Options.Get(dhcpv4.OptionUserClassInformation)),
	}
	if rai := m.RelayAgentInfo(); rai != nil {
		ci.CircuitID = rai.Get(dhcpv4.AgentCircuitIDSubOption)
		ci.RemoteID = rai.Get(dhcpv4.AgentRemoteIDSubOption)
		ci.SubscriberID = rai.Get(dhcpv4.SubscriberIDSubOption)
	}
	return ci
}

// candidates evaluates the policies of every pool for the client.
func candidates(snap *scope.Snapshot, pools []*scope.Pool, ci *scope.ClientInfo) ([]lease.Candidate, map[int64]*scope.Evaluation) {
	cands := make([]lease.Candidate, 0, len(pools))
	evals := make(map[int64]*scope.Evaluation, len(pools))
	for _, p := range pools {
		ev := snap.Evaluate(p, ci)
		evals[p.ID] = &ev
		cands = append(cands, lease.Candidate{Pool: p, Ranges: ev.Ranges, LeaseTime: ev.LeaseTime})
	}
	return cands, evals
}

// addConfiguredOptions adds the resolved option values to the reply.
//
// If the client sent a parameter request list (option 55) only requested
// options are included, in the order of the list; otherwise all configured
// options are sent. Options 66/67 (and 150) are mirrored into the BOOTP
// header fields sname/file/siaddr, which many PXE ROMs read instead of the
// options. If the reply exceeds the client's maximum message size, options
// are dropped from the end of the request list.
func addConfiguredOptions(log *slog.Logger, reply, req *dhcpv4.DHCPv4, opts map[uint8][]byte) {
	var codes []uint8
	if prl := req.ParameterRequestList(); len(prl) > 0 {
		seen := map[uint8]bool{}
		for _, oc := range prl {
			code := oc.Code()
			if _, ok := opts[code]; ok && !seen[code] && !reply.Options.Has(oc) {
				seen[code] = true
				codes = append(codes, code)
			}
		}
	} else {
		for code := range opts {
			if !reply.Options.Has(dhcpv4.GenericOptionCode(code)) {
				codes = append(codes, code)
			}
		}
		sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
	}
	for _, code := range codes {
		reply.UpdateOption(dhcpv4.OptGeneric(dhcpv4.GenericOptionCode(code), opts[code]))
	}

	if v := opts[dhcpv4.OptionBootfileName.Code()]; len(v) > 0 {
		reply.BootFileName = truncate(string(v), 127)
	}
	if v := opts[dhcpv4.OptionTFTPServerName.Code()]; len(v) > 0 {
		reply.ServerHostName = truncate(string(v), 63)
		if ip := net.ParseIP(string(v)).To4(); ip != nil {
			reply.ServerIPAddr = ip
		}
	}
	if v := opts[150]; len(v) >= 4 && isZero(reply.ServerIPAddr) {
		reply.ServerIPAddr = net.IP(v[:4])
	}

	limit := minMaxMessageSize - ipUDPHeaders
	if ms, err := req.MaxMessageSize(); err == nil && int(ms) > minMaxMessageSize {
		limit = int(ms) - ipUDPHeaders
	}
	var dropped []uint8
	for len(reply.ToBytes()) > limit && len(codes) > 0 {
		last := codes[len(codes)-1]
		codes = codes[:len(codes)-1]
		reply.Options.Del(dhcpv4.GenericOptionCode(last))
		dropped = append(dropped, last)
	}
	if len(dropped) > 0 {
		log.Warn("reply exceeds the client's maximum message size; options dropped", "dropped", dropped, "limit", limit)
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
