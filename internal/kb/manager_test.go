package kb_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"larder/internal/config"
	"larder/internal/ingest"
	"larder/internal/kb"
	"larder/internal/search"
	"larder/internal/services"
	"larder/internal/store"

	"log/slog"
)

func ingestOpts() ingest.Options { return ingest.Options{} }
func searchOpts() search.Options { return search.Options{TopK: 5, CandidateK: 10} }

// fakeEmbedServer 返回固定维度的确定性向量（含 Windows/macOS 关键词区分）。
func fakeEmbedServer(t *testing.T, dim int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		data := make([]item, len(req.Input))
		for i, s := range req.Input {
			v := make([]float32, dim)
			if contains(s, "Windows") {
				v[0] = 1
			}
			if contains(s, "macOS") {
				v[1] = 1
			}
			data[i] = item{Index: i, Embedding: v}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func testConfig(dir, baseURL string, dim int) config.Config {
	c := config.Default()
	c.Storage.Dir = dir
	c.Embed.Endpoint.BaseURL = baseURL
	c.Embed.Endpoint.APIKey = "test"
	c.Embed.Endpoint.Model = "fake"
	c.Embed.Dim = dim
	c.Embed.Endpoint.Timeout = config.Duration(10 * time.Second)
	c.Chunk.MaxRunes = 400
	c.Chunk.Overlap = 40
	c.Chunk.MinRunes = 10
	return c
}

const docMD = "# 系统安装\n\n## Windows 安装\n\n在 Windows 上运行安装程序即可。\n\n## macOS 安装\n\n使用 Homebrew 安装。\n"

func TestMultiLibraryIsolation(t *testing.T) {
	dir := t.TempDir()
	dim := 8
	ts := fakeEmbedServer(t, dim)
	defer ts.Close()

	cfg := testConfig(dir, ts.URL, dim)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc, err := services.New(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := kb.NewManager(cfg, svc, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	ctx := context.Background()

	// 建两个库。
	a, err := mgr.Create(ctx, "产品文档")
	if err != nil {
		t.Fatalf("创建库 A 失败: %v", err)
	}
	b, err := mgr.Create(ctx, "运维手册")
	if err != nil {
		t.Fatalf("创建库 B 失败: %v", err)
	}
	if a.ID == b.ID {
		t.Fatalf("两个库 ID 不应相同: %s", a.ID)
	}

	// 往两个库各入库同一文档（应各自独立存在）。
	path := filepath.Join(dir, "install.md")
	if err := os.WriteFile(path, []byte(docMD), 0o644); err != nil {
		t.Fatal(err)
	}
	appA, err := mgr.Get(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	appB, err := mgr.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appA.Ingest.IngestFile(ctx, path, ingestOpts()); err != nil {
		t.Fatalf("库 A 入库失败: %v", err)
	}
	if _, err := appB.Ingest.IngestFile(ctx, path, ingestOpts()); err != nil {
		t.Fatalf("库 B 入库失败: %v", err)
	}

	// 列表应为 2 个库，各含 1 篇文档。
	infos, err := mgr.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("期望 2 个库，实际 %d", len(infos))
	}
	for _, in := range infos {
		if in.Documents != 1 {
			t.Errorf("库 %s 期望 1 篇文档，实际 %d", in.ID, in.Documents)
		}
		if in.Dim != dim {
			t.Errorf("库 %s 期望维度 %d，实际 %d", in.ID, dim, in.Dim)
		}
	}

	// 在库 A 检索 Windows 应命中。
	hits, err := appA.Search.Search(ctx, "Windows", searchOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(hits.Hits) == 0 || !contains(hits.Hits[0].Text, "Windows") {
		t.Fatalf("库 A 检索未命中 Windows: %+v", hits)
	}

	// 删除库 B，列表剩 1 个。
	if err := mgr.Delete(ctx, b.ID); err != nil {
		t.Fatalf("删除库 B 失败: %v", err)
	}
	infos, _ = mgr.List(ctx)
	if len(infos) != 1 || infos[0].ID != a.ID {
		t.Fatalf("删除后期望仅剩库 A，实际 %+v", infos)
	}
}

func TestLegacyLayoutMigration(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	legacyDB := filepath.Join(dir, "kb1.db")
	st, err := store.Open(legacyDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMeta(ctx, "kb_name", "旧版库"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".uploads"), 0o755); err != nil {
		t.Fatal(err)
	}
	upload := filepath.Join(dir, ".uploads", "hash.pdf")
	if err := os.WriteFile(upload, []byte("%PDF"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertDocument(ctx, store.Document{ID: "doc1", Source: "paper.pdf", Title: "A", OriginPath: upload, OriginType: "upload"}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if err := os.MkdirAll(filepath.Join(dir, ".assets", "doc1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".assets", "doc1", "a.png"), []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(dir, "http://127.0.0.1:1", 4)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc, err := services.New(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := kb.NewManager(cfg, svc, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	infos, err := mgr.List(ctx)
	if err != nil || len(infos) != 1 {
		t.Fatalf("list after migration: %+v %v", infos, err)
	}
	info := infos[0]
	wantDir := filepath.Join(dir, "旧版库")
	if info.ID != "kb1" || info.DataDir != wantDir {
		t.Fatalf("unexpected info: %+v want dir %s", info, wantDir)
	}
	for _, sub := range []string{"pdf", "files", "image", "exports"} {
		if fi, err := os.Stat(filepath.Join(wantDir, sub)); err != nil || !fi.IsDir() {
			t.Fatalf("missing %s: %v", sub, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(wantDir, "image", "doc1", "a.png")); err != nil || string(b) != "PNG" {
		t.Fatalf("legacy asset not migrated: %v %q", err, string(b))
	}
	if b, err := os.ReadFile(filepath.Join(wantDir, "pdf", "paper.pdf")); err != nil || string(b) != "%PDF" {
		t.Fatalf("legacy upload not copied: %v %q", err, string(b))
	}
	app, err := mgr.Get("kb1")
	if err != nil {
		t.Fatal(err)
	}
	doc, ok, err := app.Store.GetDocument(ctx, "doc1")
	if err != nil || !ok {
		t.Fatalf("doc: %v %v", ok, err)
	}
	if doc.OriginPath != filepath.Join(wantDir, "pdf", "paper.pdf") {
		t.Fatalf("origin path not updated: %s", doc.OriginPath)
	}
	if _, err := os.Stat(legacyDB); !os.IsNotExist(err) {
		t.Fatalf("legacy db still exists: %v", err)
	}
}
