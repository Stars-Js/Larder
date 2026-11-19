package ingest_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larder/internal/chunk"
	"larder/internal/ingest"
	"larder/internal/provider/parse"
	"larder/internal/store"
)

type layoutParser struct{}

func (p *layoutParser) Name() string { return "layout-fake" }
func (p *layoutParser) Parse(context.Context, parse.Input) (parse.Result, error) {
	return parse.Result{
		Markdown: "# 版面解析\n\n![架构图](images/arch.png)\n\n正文",
		Title:    "版面解析", Method: "layout-fake",
		Images: []parse.Image{{Name: "arch.png", Data: []byte("PNGDATA"), MIME: "image/png"}},
	}, nil
}

func TestPreferExternalParserAndAssets(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "kb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	embedder := &switchEmbedder{dim: 4}
	svc := ingest.New(st, &switchParser{fail: true}, &layoutParser{}, embedder,
		chunk.Options{MaxRunes: 300, Overlap: 20, MinRunes: 10}, "fake-embed", false, filepath.Join(dir, "uploads"))
	ctx := context.Background()
	res, err := svc.IngestData(ctx, "paper.pdf", "paper.pdf", "application/pdf", []byte("%PDF-1.6 fake"), ingest.Options{PreferExternal: true})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	doc, ok, _ := st.GetDocument(ctx, res.DocID)
	if !ok || doc.ParseMethod != "layout-fake" {
		t.Fatalf("layout parser not used: %+v", doc)
	}
	md, _ := st.GetDocumentMarkdown(ctx, res.DocID)
	if !strings.Contains(md, "asset://arch.png") {
		t.Fatalf("markdown missing asset ref: %s", md)
	}
	asset := filepath.Join(dir, ".assets", res.DocID, "arch.png")
	if b, err := os.ReadFile(asset); err != nil || string(b) != "PNGDATA" {
		t.Fatalf("asset not saved: %v %q", err, string(b))
	}
}
