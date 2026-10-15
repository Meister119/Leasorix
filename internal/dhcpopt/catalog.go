package dhcpopt

import "sort"

// Definition describes an option code.
type Definition struct {
	Code        uint8  `json:"code"`
	Name        string `json:"name"`
	Type        Type   `json:"type"`
	Description string `json:"description"`
	// Builtin definitions ship with leasorix; others are user defined.
	Builtin bool `json:"builtin"`
}

// builtin is the catalog of well-known options (RFC 2132 and successors).
var builtin = []Definition{
	{2, "time-offset", TypeInt32, "UTC offset in seconds", true},
	{3, "routers", TypeIPList, "Default gateways", true},
	{4, "time-servers", TypeIPList, "RFC 868 time servers", true},
	{5, "name-servers", TypeIPList, "IEN 116 name servers", true},
	{6, "domain-name-servers", TypeIPList, "DNS servers", true},
	{7, "log-servers", TypeIPList, "MIT-LCS UDP log servers", true},
	{9, "lpr-servers", TypeIPList, "Line printer servers", true},
	{12, "host-name", TypeString, "Client host name", true},
	{13, "boot-file-size", TypeUint16, "Boot file size in 512-byte blocks", true},
	{15, "domain-name", TypeString, "DNS domain name", true},
	{17, "root-path", TypeString, "Root disk path", true},
	{19, "ip-forwarding", TypeBool, "Enable IP forwarding", true},
	{23, "default-ip-ttl", TypeUint8, "Default IP time-to-live", true},
	{26, "interface-mtu", TypeUint16, "Interface MTU", true},
	{28, "broadcast-address", TypeIP, "Broadcast address", true},
	{31, "router-discovery", TypeBool, "Perform router discovery", true},
	{33, "static-routes", TypeIPList, "Classful static routes (destination/router pairs)", true},
	{35, "arp-cache-timeout", TypeUint32, "ARP cache timeout in seconds", true},
	{37, "default-tcp-ttl", TypeUint8, "Default TCP time-to-live", true},
	{40, "nis-domain", TypeString, "NIS domain name", true},
	{41, "nis-servers", TypeIPList, "NIS servers", true},
	{42, "ntp-servers", TypeIPList, "NTP servers", true},
	{43, "vendor-encapsulated-options", TypeHex, "Vendor specific information", true},
	{44, "netbios-name-servers", TypeIPList, "WINS / NBNS servers", true},
	{45, "netbios-dd-server", TypeIPList, "NetBIOS datagram distribution servers", true},
	{46, "netbios-node-type", TypeUint8, "NetBIOS node type (1=B, 2=P, 4=M, 8=H)", true},
	{47, "netbios-scope", TypeString, "NetBIOS scope", true},
	{60, "vendor-class-identifier", TypeString, "Vendor class identifier (e.g. PXEClient)", true},
	{64, "nisplus-domain", TypeString, "NIS+ domain name", true},
	{65, "nisplus-servers", TypeIPList, "NIS+ servers", true},
	{66, "tftp-server-name", TypeString, "TFTP server name (PXE next server)", true},
	{67, "bootfile-name", TypeString, "Boot file name (PXE)", true},
	{69, "smtp-servers", TypeIPList, "SMTP servers", true},
	{70, "pop3-servers", TypeIPList, "POP3 servers", true},
	{72, "www-servers", TypeIPList, "WWW servers", true},
	{100, "pcode", TypeString, "IEEE 1003.1 TZ string (RFC 4833)", true},
	{101, "tcode", TypeString, "TZ database name (RFC 4833)", true},
	{108, "ipv6-only-preferred", TypeUint32, "IPv6-only preferred wait time (RFC 8925)", true},
	{114, "captive-portal", TypeString, "Captive portal API URI (RFC 8910)", true},
	{119, "domain-search", TypeDomainList, "DNS domain search list (RFC 3397)", true},
	{121, "classless-static-routes", TypeRoutes, "Classless static routes (RFC 3442)", true},
	{150, "tftp-server-address", TypeIPList, "TFTP server addresses (Cisco)", true},
	{249, "ms-classless-static-routes", TypeRoutes, "Classless static routes (Microsoft)", true},
	{252, "wpad", TypeString, "Web proxy auto-discovery URL", true},
}

// Managed lists the codes leasorix fills itself; they cannot be configured
// as option values.
var Managed = map[uint8]string{
	0:   "pad",
	1:   "subnet mask (derived from the scope subnet)",
	50:  "requested IP address (client only)",
	51:  "lease time (set via scope or policy lease time)",
	52:  "option overload",
	53:  "DHCP message type",
	54:  "server identifier",
	55:  "parameter request list (client only)",
	57:  "maximum message size (client only)",
	58:  "renewal time T1 (derived from the lease time)",
	59:  "rebinding time T2 (derived from the lease time)",
	61:  "client identifier (client only)",
	82:  "relay agent information (relay only)",
	118: "subnet selection (client only)",
	255: "end",
}

var builtinByCode = func() map[uint8]Definition {
	m := make(map[uint8]Definition, len(builtin))
	for _, d := range builtin {
		m[d.Code] = d
	}
	return m
}()

// Builtin returns the built-in definition for code.
func Builtin(code uint8) (Definition, bool) {
	d, ok := builtinByCode[code]
	return d, ok
}

// BuiltinDefinitions returns all built-in definitions ordered by code.
func BuiltinDefinitions() []Definition {
	out := append([]Definition(nil), builtin...)
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}
