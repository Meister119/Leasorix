//go:build unix

package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// LockFileName is the lock file inside the data directory.
const LockFileName = "leasorixd.lock"

// Lock is an exclusive advisory lock on the data directory. It prevents two
// processes (two daemons, or the daemon and an offline CLI command) from
// working on the same database.
type Lock struct{ f *os.File }

// LockDataDir acquires the lock or fails immediately.
func LockDataDir(dir string) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, LockFileName), os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // data dir from the configuration
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("data directory %s is in use by another leasorixd process", dir)
		}
		return nil, fmt.Errorf("lock data directory: %w", err)
	}
	return &Lock{f: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}
