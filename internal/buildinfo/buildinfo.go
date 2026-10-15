// Package buildinfo exposes version information injected at build time via
// -ldflags "-X github.com/meister119/leasorix/internal/buildinfo.Version=...".
package buildinfo

import (
	"fmt"
	"runtime"
)

// Set via -ldflags at build time.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// GoVersion returns the Go runtime version the binary was built with.
func GoVersion() string { return runtime.Version() }

// String returns a single-line human readable version string.
func String() string {
	return fmt.Sprintf("leasorix %s (commit %s, built %s, %s)", Version, Commit, Date, runtime.Version())
}
