package search_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"larder/internal/model"
	"larder/internal/search"
	"larder/internal/store"
)

type fixedEmbedder struct{}

func (fixedEmbedder) Name() string { return "fixed" }
func (fixedEmbedder) Dim() int     { return 2 }
func (fixedEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	out := make([][]float32, len(inputs))
	for i := range inputs {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

type failingReranker struct{}

func (failingReranker) Name() string { return "failing" }
func (failingReranker) Rerank(context.Context, string, []string) ([]float64, error) {
	return nil, errors.New("重排服务 503")
}

func TestRerankFailureFallsBackToVectorHits(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "kb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	if err := st.EnsureDim(2); err != nil {
		t.Fatal(err)
	}
	docID := "doc-1"
	if err := st.UpsertDocument(ctx, store.Document{
		ID: docID, Source: "a.md", Title: "A", MIME: "text/markdown", Hash: "h", Chunks: 1,
		ParseStatus: "ok", EmbedStatus: "ok", EmbedDim: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceChunks(ctx, docID, []model.Chunk{{DocID: docID, Ordinal: 0, Text: "命中内容"}}, [][]float32{{1, 0}}); err != nil {
		t.Fatal(err)
	}
	docID2 := "doc-2"
	if err := st.UpsertDocument(ctx, store.Document{
		ID: docID2, Source: "b.md", Title: "B", MIME: "text/markdown", Hash: "h2", Chunks: 1,
		ParseStatus: "ok", EmbedStatus: "ok", EmbedDim: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceChunks(ctx, docID2, []model.Chunk{{DocID: docID2, Ordinal: 0, Text: "第二段"}}, [][]float32{{0.9, 0.1}}); err != nil {
		t.Fatal(err)
	}

	svc := search.New(st, fixedEmbedder{}, failingReranker{}, "fake-embed", "fake-rerank")
	res, err := svc.Search(ctx, "查询", search.Options{TopK: 3, CandidateK: 5})
	if err != nil {
		t.Fatalf("重排失败不应让检索整体失败: %v", err)
	}
	if res.Reranked {
		t.Fatal("重排失败时 Reranked 应为 false")
	}
	if res.RerankError == "" {
		t.Fatal("应返回重排失败原因")
	}
	if len(res.Hits) != 2 || res.Hits[0].Text != "命中内容" {
		t.Fatalf("应退回向量召回结果: %+v", res.Hits)
	}
}
