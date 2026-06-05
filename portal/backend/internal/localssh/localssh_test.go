package localssh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpsertCreatesAndReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".ssh", "config")

	if err := Upsert(path, "ws-a", "Host ws-a\n    User dev\n"); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	got := read(t, path)
	if !strings.Contains(got, "# >>> portal:ws-a >>>") || !strings.Contains(got, "Host ws-a") {
		t.Fatalf("missing managed block:\n%s", got)
	}

	// Re-upsert with new content must replace, not duplicate.
	if err := Upsert(path, "ws-a", "Host ws-a\n    User dev\n    Port 2222\n"); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got = read(t, path)
	if n := strings.Count(got, "# >>> portal:ws-a >>>"); n != 1 {
		t.Fatalf("block duplicated (%d begin markers):\n%s", n, got)
	}
	if !strings.Contains(got, "Port 2222") {
		t.Fatalf("content not replaced:\n%s", got)
	}
}

func TestUpsertPreservesUnmanagedAndOtherBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("Host hand-written\n    User me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Upsert(path, "ws-a", "Host ws-a\n    User dev\n"); err != nil {
		t.Fatal(err)
	}
	if err := Upsert(path, "ws-b", "Host ws-b\n    User dev\n"); err != nil {
		t.Fatal(err)
	}

	if err := Remove(path, "ws-a"); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.Contains(got, "Host hand-written") {
		t.Fatalf("clobbered unmanaged entry:\n%s", got)
	}
	if strings.Contains(got, "portal:ws-a") {
		t.Fatalf("ws-a block not removed:\n%s", got)
	}
	if !strings.Contains(got, "portal:ws-b") {
		t.Fatalf("ws-b block wrongly removed:\n%s", got)
	}
}

func TestRemoveMissingIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := Remove(path, "ws-x"); err != nil {
		t.Fatalf("remove on missing file: %v", err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
