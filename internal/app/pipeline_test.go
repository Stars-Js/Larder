package app_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"larder/internal/chunk"
	"larder/internal/ingest"
	"larder/internal/provider/parse"
	"larder/internal/search"
	"larder/internal/store"
)

// fakeEmbedder 用两个关键词做二维向量，便于确定性验证检索正确性。
type fakeEmbedder struct{ dim int }

func (f *fakeEmbedder) Name() string { return "fake" }
func (f *fakeEmbedder) Dim() int     { return f.dim }
func (f *fakeEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	out := make([][]float32, len(inputs))
	for i, s := range inputs {
		v := make([]float32, f.dim)
		if contains(s, "Windows") {
			v[0] = 1
		}
		if contains(s, "macOS") {
			v[1] = 1
		}
		out[i] = v
	}
	return out, nil
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func setup(t *testing.T) (store.Store, *ingest.Service, *search.Service) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	emb := &fakeEmbedder{dim: 2}
	ing := ingest.New(st, parse.NewPlain(), emb, chunk.Options{MaxRunes: 400, Overlap: 40, MinRunes: 10}, "fake-embed", false, t.TempDir())
	srch := search.New(st, emb, nil, "fake-embed", "")
	return st, ing, srch
}

const docMD = `# 系统安装

## Windows 安装

在 Windows 上运行安装程序即可。

## macOS 安装

使用 Homebrew 安装。
`

func TestIngestAndSearch(t *testing.T) {
	_, ing, srch := setup(t)
	ctx := context.Background()

	// 写入临时 Markdown 文件。
	dir := t.TempDir()
	path := filepath.Join(dir, "install.md")
	if err := os.WriteFile(path, []byte(docMD), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ing.IngestFile(ctx, path, ingest.Options{})
	if err != nil {
		t.Fatalf("入库失败: %v", err)
	}
	if res.Chunks < 2 {
		t.Fatalf("切块数过少: %d", res.Chunks)
	}

	// 再入库一次应判重跳过。
	res2, err := ing.IngestFile(ctx, path, ingest.Options{})
	if err != nil {
		t.Fatalf("二次入库失败: %v", err)
	}
	if !res2.Skipped {
		t.Error("相同内容应被跳过")
	}

	// 检索 Windows 相关内容。
	hits, err := srch.Search(ctx, "Windows", search.Options{TopK: 1})
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	if len(hits.Hits) == 0 {
		t.Fatal("应至少命中一条")
	}
	if !contains(hits.Hits[0].Text, "Windows") {
		t.Errorf("首选命中应包含 Windows，实为: %q", hits.Hits[0].Text)
	}
}

func TestDimConflict(t *testing.T) {
	st, ing, _ := setup(t)
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.md")
	os.WriteFile(path, []byte(docMD), 0o644)
	if _, err := ing.IngestFile(ctx, path, ingest.Options{}); err != nil {
		t.Fatalf("首次入库失败: %v", err)
	}
	// 已初始化维度后，用一个不同的维度调用 EnsureDim 应报冲突。
	if err := st.EnsureDim(999); err == nil {
		t.Error("维度冲突时 EnsureDim 应返回错误")
	}
}

func TestDelete(t *testing.T) {
	st, ing, _ := setup(t)
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.md")
	os.WriteFile(path, []byte(docMD), 0o644)
	res, err := ing.IngestFile(ctx, path, ingest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteDocument(ctx, res.DocID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, ok, _ := st.GetDocument(ctx, res.DocID); ok {
		t.Error("删除后不应再查到文档")
	}
	n, _ := st.CountChunks(ctx, res.DocID)
	if n != 0 {
		t.Errorf("删除后切块数应为 0，实为 %d", n)
	}
}
