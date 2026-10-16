package configio

import "github.com/meister119/leasorix/internal/dhcpopt"

func dhcpoptDef(code uint8, name string) dhcpopt.Definition {
	return dhcpopt.Definition{Code: code, Name: name, Type: dhcpopt.TypeString}
}
