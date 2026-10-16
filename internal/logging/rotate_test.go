package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.log")
	r, err := OpenRotating(path, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	line := append(bytes.Repeat([]byte("a"), 300<<10), '\n') // ~300 KiB
	for range 10 {
		if _, err := r.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"x.log", "x.log.1", "x.log.2"} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
		if st.Size() > 1<<20 {
			t.Fatalf("%s is %d bytes", name, st.Size())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "x.log.3")); !os.IsNotExist(err) {
		t.Fatal("more backups than configured")
	}
	// Re-open appends.
	r, err = OpenRotating(path, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if r.size == 0 {
		t.Fatal("size of existing file not picked up")
	}
	_ = r.Close()
}
