package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestZoteroDataSignature(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "zotero.sqlite")
	if err := os.WriteFile(db, []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "storage"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := zoteroDataSignature(dir)
	if first == "" {
		t.Fatal("empty signature")
	}
	if again := zoteroDataSignature(dir); again != first {
		t.Fatalf("signature should be stable: %q vs %q", first, again)
	}
	future := time.Now().Add(3 * time.Second)
	if err := os.Chtimes(db, future, future); err != nil {
		t.Fatal(err)
	}
	if got := zoteroDataSignature(dir); got == first {
		t.Fatalf("signature should change after sqlite mtime: %q", got)
	}
}
