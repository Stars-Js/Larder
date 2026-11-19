package library_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"larder/internal/cite"
	"larder/internal/config"
	"larder/internal/kb"
	"larder/internal/library"
	"larder/internal/search"
	"larder/internal/services"
)

func fakeVector(s string) []float32 {
	low := strings.ToLower(s)
	v := []float32{0, 0, 1}
	if strings.Contains(low, "transformer") {
		v[0] = 1
	}
	if strings.Contains(low, "attention") {
		v[1] = 1
	}
	return v
}

func embeddingServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data := make([]map[string]any, len(req.Input))
		for i, s := range req.Input {
			data[i] = map[string]any{"index": i, "embedding": fakeVector(s)}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  data,
			"model": "fake-embed",
			"usage": map[string]any{"prompt_tokens": 10, "total_tokens": 10},
		})
	}))
}

func zoteroServer(t *testing.T) *httptest.Server {
	t.Helper()
	fulltext := "# Attention Is All You Need\n\n## Abstract\n\n" +
		strings.Repeat("We propose the Transformer, a model based entirely on attention mechanisms. ", 8) +
		"\n\n## Method\n\n" + strings.Repeat("The encoder and decoder use self-attention to model sequence dependencies. ", 8)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/api/users/0/collections", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{{
			"key": "COL1", "version": 1,
			"data": map[string]any{"key": "COL1", "name": "Transformer Papers", "parentCollection": false},
			"meta": map[string]any{"numItems": 1},
		}})
	})
	mux.HandleFunc("/api/users/0/collections/COL1/items/top", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{{
			"key": "ITEM1", "version": 7,
			"data": map[string]any{
				"itemType": "journalArticle", "title": "Attention Is All You Need",
				"date": "2017-06-12", "DOI": "10.5555/3295222", "publicationTitle": "NeurIPS",
				"volume": "30", "pages": "5998-6008", "language": "en",
				"abstractNote": "The dominant sequence transduction models are based on complex recurrent or convolutional neural networks.",
				"dateModified": "2024-01-01T00:00:00Z",
				"creators": []map[string]any{
					{"creatorType": "author", "firstName": "Ashish", "lastName": "Vaswani"},
					{"creatorType": "author", "firstName": "Noam", "lastName": "Shazeer"},
				},
			},
		}})
	})
	mux.HandleFunc("/api/users/0/items/ITEM1/children", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{{
			"key": "ATT1",
			"data": map[string]any{
				"itemType": "attachment", "contentType": "application/pdf",
				"filename": "paper.pdf", "path": "storage:paper.pdf", "linkMode": "imported_file",
			},
		}})
	})
	mux.HandleFunc("/api/users/0/items/ATT1/fulltext", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"content": fulltext, "indexedPages": 10, "totalPages": 10})
	})
	return httptest.NewServer(mux)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestSyncCollectionEndToEnd(t *testing.T) {
	zdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(zdir, "storage", "ATT1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zdir, "storage", "ATT1", "paper.pdf"), []byte("PDF"), 0o644); err != nil {
		t.Fatal(err)
	}
	zapi := zoteroServer(t)
	defer zapi.Close()
	emb := embeddingServer(t)
	defer emb.Close()

	cfg := config.Default()
	cfg.Storage.Dir = t.TempDir()
	cfg.Parse.Provider = "plain"
	cfg.Embed.Endpoint.BaseURL = emb.URL
	cfg.Embed.Endpoint.APIKey = "test"
	cfg.Embed.Endpoint.Model = "fake-embed"
	cfg.Embed.Dim = 3
	cfg.Rerank.Enabled = false
	cfg.Zotero.Enabled = true
	cfg.Zotero.DataDir = zdir
	cfg.Zotero.APIBase = zapi.URL + "/api"
	cfg.Zotero.LibraryID = "0"
	cfg.Zotero.MDMode = "storage"
	cfg.Zotero.CiteStyle = "gb-t-7714-numeric"

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := services.New(cfg, logger)
	if err != nil {
		t.Fatalf("services.New: %v", err)
	}
	mgr, err := kb.NewManager(cfg, svc, logger)
	if err != nil {
		t.Fatalf("kb.NewManager: %v", err)
	}
	defer mgr.Close()
	lib := library.New(mgr, logger)
	ctx := context.Background()

	info, err := lib.Create(ctx, library.CreateRequest{
		Name: "Transformer Papers", DataDir: zdir, APIBase: zapi.URL + "/api",
		LibraryID: "0", CollectionKey: "COL1", CollectionName: "Transformer Papers",
		MDMode: "storage", CiteStyle: "gb-t-7714-numeric",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Kind != "zotero" || info.ZoteroCollection != "Transformer Papers" {
		t.Fatalf("unexpected info: %+v", info)
	}
	if _, err := lib.StartSync(ctx, info.ID, library.SyncOptions{}); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		job := lib.Job(info.ID)
		if job.State != "running" {
			if job.State != "ok" {
				t.Fatalf("sync state=%s msg=%s errors=%v", job.State, job.Message, job.Errors)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sync timeout: %+v", job)
		}
		time.Sleep(50 * time.Millisecond)
	}

	a, err := mgr.Get(info.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	job := lib.Job(info.ID)
	items, err := a.Store.ListLibraryItems(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("library items: %+v err=%v job=%+v", items, err, job)
	}
	it := items[0]
	if it.Title != "Attention Is All You Need" || it.Year != 2017 || it.DOI != "10.5555/3295222" {
		t.Fatalf("unexpected item: %+v", it)
	}
	if it.SyncStatus != "ok" || it.DocID == "" || it.BibTeX == "" {
		t.Fatalf("item not fully synced: %+v", it)
	}
	if it.FulltextSource != "zotero" {
		t.Fatalf("expected zotero fulltext source, got %q", it.FulltextSource)
	}

	docs, err := a.Store.ListDocuments(ctx)
	if err != nil || len(docs) != 1 {
		t.Fatalf("documents: %+v err=%v", docs, err)
	}
	if docs[0].Title != "Attention Is All You Need" || docs[0].Chunks == 0 {
		t.Fatalf("unexpected doc: %+v", docs[0])
	}
	md, _ := a.Store.GetDocumentMarkdown(ctx, docs[0].ID)
	if !strings.Contains(md, "Transformer") {
		t.Fatalf("markdown missing content: %q", md)
	}

	// 写入 Zotero storage/<attachment>/<标题>.md
	sidecar := filepath.Join(zdir, "storage", "ATT1", "Attention Is All You Need.md")
	if b, err := os.ReadFile(sidecar); err != nil || !strings.Contains(string(b), "Transformer") {
		t.Fatalf("sidecar missing: %v", err)
	}

	// 检索
	res, err := a.Search.Search(ctx, "transformer attention", search.Options{TopK: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected search hits")
	}

	// 引用
	ci := library.CiteItem(it)
	if ci.Key == "" {
		t.Fatalf("missing cite key: %+v", ci)
	}
	refs := cite.BuildReferences([]cite.Item{ci}, "gb-t-7714-numeric")
	if len(refs) != 1 || refs[0].Mark != "[1]" {
		t.Fatalf("unexpected refs: %+v", refs)
	}
	if !strings.Contains(refs[0].Text, "Attention Is All You Need[J]") {
		t.Fatalf("unexpected reference text: %s", refs[0].Text)
	}
	if !strings.Contains(refs[0].BibTeX, "@article{vaswani2017attention") {
		t.Fatalf("unexpected bibtex: %s", refs[0].BibTeX)
	}

	// 计数
	total, synced, failed, err := lib.Counts(ctx, info.ID)
	if err != nil || total != 1 || synced != 1 || failed != 0 {
		t.Fatalf("counts: %d %d %d %v", total, synced, failed, err)
	}
}
