package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWeakCiteKey(t *testing.T) {
	for _, k := range []string{"", "2017", "ref2017", "abc"} {
		if !weakCiteKey(k) {
			t.Fatalf("%q should be weak", k)
		}
	}
	for _, k := range []string{"vaswani2017attention", "vaswani2017", "zhang2020"} {
		if weakCiteKey(k) {
			t.Fatalf("%q should not be weak", k)
		}
	}
}

func TestCopyAssetsToSidecar(t *testing.T) {
	dir := t.TempDir()
	assets := filepath.Join(dir, "assets")
	mdDir := filepath.Join(dir, "md")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "a.png"), []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := copyAssetsToSidecar(assets, mdDir, "![fig](asset://a.png)\n")
	if !strings.Contains(got, "![fig](images/a.png)") {
		t.Fatalf("asset not rewritten: %s", got)
	}
	if b, err := os.ReadFile(filepath.Join(mdDir, "images", "a.png")); err != nil || string(b) != "PNG" {
		t.Fatalf("asset not copied: %v %q", err, string(b))
	}
}

func TestSanitizeFileName(t *testing.T) {
	if got := sanitizeFileName(`a/b:c*?"<>|`); got != "a_b_c______" {
		t.Fatalf("unexpected sanitize: %q", got)
	}
	if got := sanitizeFileName("  "); got != "" {
		t.Fatalf("blank should be empty: %q", got)
	}
}
