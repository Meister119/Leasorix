//go:build !unix

package store

// Lock is a no-op on platforms without flock.
type Lock struct{}

// LockDataDir is a no-op on platforms without flock.
func LockDataDir(string) (*Lock, error) { return &Lock{}, nil }

// Release is a no-op.
func (l *Lock) Release() error { return nil }
