package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larder/internal/provider/parse"
)

func TestSaveImagesRewritesMarkdown(t *testing.T) {
	dir := t.TempDir()
	svc := &Service{dataDir: dir}
	md := "# 论文\n\n![架构图](images/arch.png)\n\n正文"
	got, err := svc.saveImages("doc1", md, []parse.Image{
		{Name: "arch.png", Data: []byte("PNGDATA"), MIME: "image/png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "![架构图](asset://arch.png)") {
		t.Fatalf("markdown not rewritten: %s", got)
	}
	asset := filepath.Join(dir, "image", "doc1", "arch.png")
	if b, err := os.ReadFile(asset); err != nil || string(b) != "PNGDATA" {
		t.Fatalf("asset missing: %v %q", err, string(b))
	}
}

func TestSafeAssetName(t *testing.T) {
	if got := safeAssetName(`images/../a:b.png`); got != "a_b.png" {
		t.Fatalf("safe name = %q", got)
	}
	if got := safeAssetName(".."); got != "" {
		t.Fatalf("dotdot should be empty, got %q", got)
	}
}
