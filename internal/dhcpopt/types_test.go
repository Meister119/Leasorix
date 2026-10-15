package dhcpopt

import (
	"bytes"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		typ   Type
		in    string
		canon string
		wire  []byte
	}{
		{TypeIP, " 10.0.0.1 ", "10.0.0.1", []byte{10, 0, 0, 1}},
		{TypeIPList, "10.0.0.1, 10.0.0.2;10.0.0.3", "10.0.0.1,10.0.0.2,10.0.0.3", []byte{10, 0, 0, 1, 10, 0, 0, 2, 10, 0, 0, 3}},
		{TypeString, "example.com", "example.com", []byte("example.com")},
		{TypeUint8, "8", "8", []byte{8}},
		{TypeUint16, "1500", "1500", []byte{0x05, 0xdc}},
		{TypeUint32, "86400", "86400", []byte{0, 1, 0x51, 0x80}},
		{TypeInt32, "-3600", "-3600", []byte{0xff, 0xff, 0xf1, 0xf0}},
		{TypeBool, "1", "true", []byte{1}},
		{TypeBool, "false", "false", []byte{0}},
		{TypeHex, "01:0A-ff", "010aff", []byte{1, 10, 255}},
		{TypeHex, "0x0102", "0102", []byte{1, 2}},
		{TypeDomainList, "Example.com., corp.example.com", "example.com,corp.example.com",
			append([]byte("\x07example\x03com\x00"), []byte("\x04corp\x07example\x03com\x00")...)},
		// RFC 3442 examples: 0/0 -> 0, 10/8 -> 8.10, 10.17.0.0/16 -> 16.10.17, 10.27.129/24 -> 24.10.27.129
		{TypeRoutes, "0.0.0.0/0 10.0.0.1, 10.0.0.0/8 10.0.0.2,10.17.0.0/16 10.0.0.3, 10.27.129.0/24 10.0.0.4",
			"0.0.0.0/0 10.0.0.1, 10.0.0.0/8 10.0.0.2, 10.17.0.0/16 10.0.0.3, 10.27.129.0/24 10.0.0.4",
			[]byte{0, 10, 0, 0, 1, 8, 10, 10, 0, 0, 2, 16, 10, 17, 10, 0, 0, 3, 24, 10, 27, 129, 10, 0, 0, 4}},
	}
	for _, c := range cases {
		canon, wire, err := Parse(c.typ, c.in)
		if err != nil {
			t.Errorf("%s %q: %v", c.typ, c.in, err)
			continue
		}
		if canon != c.canon || !bytes.Equal(wire, c.wire) {
			t.Errorf("%s %q: got %q % x, want %q % x", c.typ, c.in, canon, wire, c.canon, c.wire)
		}
		// Canonical form must round-trip.
		canon2, wire2, err := Parse(c.typ, canon)
		if err != nil || canon2 != canon || !bytes.Equal(wire2, wire) {
			t.Errorf("%s %q: not idempotent", c.typ, canon)
		}
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[Type][]string{
		TypeIP:         {"", "300.1.1.1", "::1", "10.0.0.1,10.0.0.2"},
		TypeIPList:     {"", "a.b.c.d"},
		TypeString:     {"   "},
		TypeUint8:      {"256", "-1", "x"},
		TypeUint16:     {"65536"},
		TypeUint32:     {"4294967296"},
		TypeInt32:      {"2147483648"},
		TypeBool:       {"yes please"},
		TypeHex:        {"zz", "", "0"},
		TypeDomainList: {"", "bad..name", "under score!"},
		TypeRoutes:     {"", "10.0.0.0/8", "10.0.0.0/8 x", "::/0 10.0.0.1"},
		Type("nope"):   {"1"},
	}
	for typ, ins := range cases {
		for _, in := range ins {
			if _, _, err := Parse(typ, in); err == nil {
				t.Errorf("%s %q accepted", typ, in)
			}
		}
	}
	long := bytes.Repeat([]byte("a"), maxValueLen+1)
	if _, _, err := Parse(TypeString, string(long)); err == nil {
		t.Error("over-long string accepted")
	}
}

func TestCatalog(t *testing.T) {
	for _, code := range []uint8{3, 6, 12, 15, 42, 43, 60, 66, 67, 119, 121, 150} {
		if _, ok := Builtin(code); !ok {
			t.Errorf("option %d missing from catalog", code)
		}
	}
	for code := range Managed {
		if _, ok := Builtin(code); ok {
			t.Errorf("managed option %d must not be configurable", code)
		}
	}
	defs := BuiltinDefinitions()
	for i := 1; i < len(defs); i++ {
		if defs[i-1].Code >= defs[i].Code {
			t.Fatal("definitions not sorted/unique")
		}
	}
	for _, d := range defs {
		if !d.Type.Valid() {
			t.Errorf("option %d has invalid type %s", d.Code, d.Type)
		}
	}
}
