//go:build linux

// Command setcap gives a file the capabilities leasorixd needs
// (CAP_NET_BIND_SERVICE and CAP_NET_RAW, permitted + effective) by writing
// the security.capability extended attribute (VFS_CAP_REVISION_2). It
// replaces libcap's setcap in the image build, which would need a package
// manager and network access.
package main

import (
	"encoding/binary"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const (
	capNetBindService = 10
	capNetRaw         = 13
	vfsCapRevision2   = 0x02000000
	vfsCapFlagsEff    = 0x000001
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: setcap FILE")
		os.Exit(2)
	}
	var b [20]byte
	binary.LittleEndian.PutUint32(b[0:], vfsCapRevision2|vfsCapFlagsEff)
	binary.LittleEndian.PutUint32(b[4:], 1<<capNetBindService|1<<capNetRaw) // permitted, low 32 bits
	// inheritable (low), permitted (high), inheritable (high) stay 0
	if err := unix.Setxattr(os.Args[1], "security.capability", b[:], 0); err != nil {
		fmt.Fprintf(os.Stderr, "setcap %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}
