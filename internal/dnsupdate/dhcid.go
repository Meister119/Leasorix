package dnsupdate

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net"
	"strings"

	"github.com/miekg/dns"
)

// DHCID identifier types (RFC 4701 section 3.3).
const (
	idHWAddr   = 0x0000 // htype + chaddr
	idClientID = 0x0001 // DHCPv4 client identifier option
	idDUID     = 0x0002 // DUID (also from an RFC 4361 client identifier)
)

// DHCID returns the base64 encoded RDATA of the DHCID record (RFC 4701)
// that ties fqdn to a client. The client is identified by its client
// identifier option (hex, as stored in the lease) or, without one, by its
// Ethernet hardware address.
func DHCID(mac, clientIDHex, fqdn string) string {
	var typ uint16
	var ident []byte
	if cid, err := hex.DecodeString(clientIDHex); err == nil && len(cid) > 0 {
		// RFC 4361: type 255, 4 byte IAID, DUID.
		if cid[0] == 255 && len(cid) > 5 {
			typ, ident = idDUID, cid[5:]
		} else {
			typ, ident = idClientID, cid
		}
	} else {
		hw, _ := net.ParseMAC(mac)
		typ, ident = idHWAddr, append([]byte{1}, hw...)
	}
	h := sha256.New()
	h.Write(ident)
	h.Write(wireName(fqdn))
	rdata := append([]byte{byte(typ >> 8), byte(typ), 1}, h.Sum(nil)...)
	return base64.StdEncoding.EncodeToString(rdata)
}

// wireName is the canonical (lower case) wire format of a domain name.
func wireName(name string) []byte {
	buf := make([]byte, 256)
	n, err := dns.PackDomainName(dns.CanonicalName(strings.TrimSuffix(name, ".")+"."), buf, 0, nil, false)
	if err != nil {
		return nil
	}
	return buf[:n]
}
