package ingest_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"larder/internal/chunk"
	"larder/internal/ingest"
	"larder/internal/provider/parse"
	"larder/internal/store"
)

type switchParser struct {
	fail bool
}

func (p *switchParser) Name() string { return "switch" }
func (p *switchParser) Parse(context.Context, parse.Input) (parse.Result, error) {
	if p.fail {
		return parse.Result{}, errors.New("模拟解析失败：该 PDF 未包含可提取的文本层")
	}
	return parse.Result{Markdown: "# 标题\n\n这是重新解析后的正文。", Title: "标题", Method: "switch"}, nil
}

type switchEmbedder struct {
	fail bool
	dim  int
}

func (e *switchEmbedder) Name() string { return "fake" }
func (e *switchEmbedder) Dim() int     { return e.dim }
func (e *switchEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	if e.fail {
		return nil, errors.New("模拟向量化失败")
	}
	out := make([][]float32, len(inputs))
	for i := range inputs {
		out[i] = []float32{1, 0, 0, 0}
	}
	return out, nil
}

func setup(t *testing.T) (store.Store, *switchParser, *switchEmbedder, *ingest.Service) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "kb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p := &switchParser{fail: true}
	e := &switchEmbedder{fail: true, dim: 4}
	svc := ingest.New(st, p, e, chunk.Options{MaxRunes: 200, Overlap: 20, MinRunes: 10}, "fake-embed", false, filepath.Join(dir, "uploads"))
	return st, p, e, svc
}

func TestFailedParseIsRecordedAndCanBeRetried(t *testing.T) {
	st, p, e, svc := setup(t)
	ctx := context.Background()

	res, err := svc.IngestData(ctx, "1.pdf", "1.pdf", "application/pdf", []byte("%PDF-1.6 fake"), ingest.Options{})
	if err == nil {
		t.Fatal("应返回解析失败")
	}
	if res.DocID == "" {
		t.Fatal("失败时也应返回 doc_id，便于前端定位")
	}
	doc, ok, err := st.GetDocument(ctx, res.DocID)
	if err != nil || !ok {
		t.Fatalf("失败记录应落库: ok=%v err=%v", ok, err)
	}
	if doc.ParseStatus != "error" || doc.EmbedStatus != "pending" {
		t.Fatalf("解析失败状态不正确: %+v", doc)
	}
	if doc.ParseNote == "" {
		t.Fatal("应记录失败原因")
	}

	// 配置好解析服务后重新处理；仍需模拟向量化成功。
	p.fail = false
	e.fail = false
	res2, err := svc.Reprocess(ctx, doc.ID)
	if err != nil {
		t.Fatalf("重新处理失败: %v", err)
	}
	if res2.ParseStatus != "ok" || res2.EmbedStatus != "ok" || res2.Chunks == 0 {
		t.Fatalf("重新处理结果不正确: %+v", res2)
	}
	doc2, _, _ := st.GetDocument(ctx, doc.ID)
	if doc2.ParseStatus != "ok" || doc2.EmbedStatus != "ok" || doc2.Chunks == 0 {
		t.Fatalf("重新处理后状态不正确: %+v", doc2)
	}
	if doc2.OriginType != "upload" || doc2.OriginPath == "" {
		t.Fatalf("应保存上传缓存以支持重试: %+v", doc2)
	}
}

func TestEmbedFailureIsRecorded(t *testing.T) {
	st, p, e, svc := setup(t)
	ctx := context.Background()
	p.fail = false // 解析成功，向量化失败

	res, err := svc.IngestData(ctx, "a.md", "a.md", "text/markdown", []byte("# 标题\n\n正文"), ingest.Options{})
	if err == nil {
		t.Fatal("应返回向量化失败")
	}
	doc, ok, _ := st.GetDocument(ctx, res.DocID)
	if !ok {
		t.Fatal("失败记录应落库")
	}
	if doc.ParseStatus != "ok" || doc.EmbedStatus != "error" || doc.EmbedNote == "" {
		t.Fatalf("向量化失败状态不正确: %+v", doc)
	}
	if doc.ParseNote == "" {
		t.Fatal("解析成功时也应记录解析说明")
	}

	e.fail = false
	res2, err := svc.Reprocess(ctx, doc.ID)
	if err != nil {
		t.Fatalf("重新处理失败: %v", err)
	}
	if res2.EmbedStatus != "ok" {
		t.Fatalf("重新处理后应嵌入成功: %+v", res2)
	}
}
