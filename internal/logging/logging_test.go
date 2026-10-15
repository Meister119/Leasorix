package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONOutput(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(&buf, "info", "json")
	if err != nil {
		t.Fatal(err)
	}
	l.Debug("hidden")
	l.Info("hello", "k", "v")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %q", len(lines), buf.String())
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatal(err)
	}
	if m["msg"] != "hello" || m["k"] != "v" {
		t.Fatalf("unexpected record %v", m)
	}
}

func TestInvalid(t *testing.T) {
	if _, err := New(&bytes.Buffer{}, "nope", "json"); err == nil {
		t.Error("expected level error")
	}
	if _, err := New(&bytes.Buffer{}, "info", "xml"); err == nil {
		t.Error("expected format error")
	}
}
