package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larder/internal/model"
	"larder/internal/zotero"
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
	if !strings.Contains(got, "![fig](image/a.png)") {
		t.Fatalf("asset not rewritten: %s", got)
	}
	if b, err := os.ReadFile(filepath.Join(mdDir, "image", "a.png")); err != nil || string(b) != "PNG" {
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

func TestSidecarTargetItemAndLarder(t *testing.T) {
	dataDir := t.TempDir()
	att := zotero.Attachment{Key: "ATT1", Path: "storage:paper.pdf"}
	item := zotero.Item{Key: "ITEM1", Title: "论文"}
	dir, name, ok := sidecarTarget(dataDir, "item", "集合", att, item)
	if !ok || name != "论文.md" || dir != filepath.Join(dataDir, "storage", "ATT1") {
		t.Fatalf("item sidecar = %q %q %v", dir, name, ok)
	}
	dir, name, ok = sidecarTarget(dataDir, "larder", "集合", att, item)
	want := filepath.Join(dataDir, "larder", "集合", "论文 [ITEM1]")
	if !ok || name != "论文.md" || dir != want {
		t.Fatalf("larder sidecar = %q %q %v, want %q", dir, name, ok, want)
	}
	if _, _, ok := sidecarTarget(dataDir, "off", "集合", att, item); ok {
		t.Fatal("off should not write sidecar")
	}
}

func TestSidecarMarkdownForIngest(t *testing.T) {
	md := "![a](image/a.png)\n![b](image/b.png)"
	got, names := sidecarMarkdownForIngest(md)
	if !strings.Contains(got, "asset://a.png") || !strings.Contains(got, "asset://b.png") {
		t.Fatalf("image refs not rewritten: %s", got)
	}
	if len(names) != 2 || names[0] != "a.png" || names[1] != "b.png" {
		t.Fatalf("names = %v", names)
	}
}

func TestWriteMarkdownSidecarCopiesImages(t *testing.T) {
	dataDir := t.TempDir()
	assets := filepath.Join(dataDir, "image", "doc1")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "a.png"), []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	att := zotero.Attachment{Key: "ATT1", Path: "storage:paper.pdf"}
	item := zotero.Item{Key: "ITEM1", Title: "论文"}
	p, err := writeMarkdownSidecar(dataDir, "item", "集合", att, item, "![a](asset://a.png)", assets)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(p); err != nil || !strings.Contains(string(b), "image/a.png") {
		t.Fatalf("sidecar markdown = %v %q", err, string(b))
	}
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(p), "image", "a.png")); err != nil || string(b) != "PNG" {
		t.Fatalf("sidecar image = %v %q", err, string(b))
	}
}

func TestNormalizeLegacySidecarImages(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "images", "a.png"), []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	mdPath := filepath.Join(dir, "paper.md")
	got := normalizeSidecarLayout(mdPath, "![a](images/a.png)")
	if !strings.Contains(got, "image/a.png") || strings.Contains(got, "images/") {
		t.Fatalf("legacy ref not rewritten: %s", got)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "image", "a.png")); err != nil || string(b) != "PNG" {
		t.Fatalf("legacy image not moved: %v %q", err, string(b))
	}
	if _, err := os.Stat(filepath.Join(dir, "images")); !os.IsNotExist(err) {
		t.Fatalf("images dir should be gone: %v", err)
	}
}

func TestExistingSidecar(t *testing.T) {
	dataDir := t.TempDir()
	att := zotero.Attachment{Key: "ATT1", Path: "storage:paper.pdf"}
	item := zotero.Item{Key: "ITEM1", Title: "论文"}
	dir, name, ok := sidecarTarget(dataDir, "item", "集合", att, item)
	if !ok {
		t.Fatal("target not found")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mdPath := filepath.Join(dir, name)
	if err := os.WriteFile(mdPath, []byte("# 已有解析"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, ok := existingSidecar(dataDir, "item", "集合", att, item)
	if !ok || got != mdPath {
		t.Fatalf("existing sidecar = %q %v, want %q", got, ok, mdPath)
	}
}

func TestExistingSidecarForLibraryItem(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "storage", "ATT1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "论文.md"), []byte("# 解析"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := model.LibraryItem{ItemKey: "ITEM1", Title: "论文", AttachmentKey: "ATT1", AttachmentPath: filepath.Join(dir, "paper.pdf")}
	got, ok := ExistingSidecarForItem(dataDir, "item", "集合", item)
	if !ok || got != filepath.Join(dir, "论文.md") {
		t.Fatalf("sidecar=%q ok=%v", got, ok)
	}
}
