//go:build unix

package store

import "testing"

func TestDataDirLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	l, err := LockDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockDataDir(dir); err == nil {
		t.Fatal("second lock on the same data directory succeeded")
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	l2, err := LockDataDir(dir)
	if err != nil {
		t.Fatal("lock not released:", err)
	}
	_ = l2.Release()
}
