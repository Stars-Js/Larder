package mcp_test

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
	"sync/atomic"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"larder/internal/activity"
	"larder/internal/config"
	"larder/internal/ingest"
	"larder/internal/kb"
	"larder/internal/library"
	"larder/internal/mcp"
	"larder/internal/model"
	"larder/internal/services"
)

func fakeEmbeddingServer(t *testing.T) (*httptest.Server, *int64) {
	t.Helper()
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		data := make([]map[string]any, len(req.Input))
		for i := range req.Input {
			data[i] = map[string]any{"index": i, "embedding": []float32{1, 1, 1}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": data, "usage": map[string]any{"prompt_tokens": 10, "total_tokens": 10},
		})
	}))
	return srv, &calls
}

type mcpEnv struct {
	mgr        *kb.Manager
	lib        *library.Service
	act        *activity.Logger
	kb1        string
	embedCalls *int64
}

func setupMCPEnv(t *testing.T) *mcpEnv {
	t.Helper()
	emb, embedCalls := fakeEmbeddingServer(t)
	t.Cleanup(emb.Close)
	cfg := config.Default()
	cfg.Storage.Dir = t.TempDir()
	cfg.Parse.Provider = "plain"
	cfg.Embed.Endpoint.BaseURL = emb.URL
	cfg.Embed.Endpoint.APIKey = "test"
	cfg.Embed.Endpoint.Model = "fake"
	cfg.Embed.Dim = 3
	cfg.Rerank.Enabled = false
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := services.New(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := kb.NewManager(cfg, svc, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	lib := library.New(mgr, logger)
	act := activity.New(filepath.Join(cfg.Storage.Dir, "activity.log"))

	first, err := mgr.Create(context.Background(), "Paper Library")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create(context.Background(), "Other Library"); err != nil {
		t.Fatal(err)
	}
	a, err := mgr.Get(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	md := "# Attention Is All You Need\n\n## Abstract\n\n" + strings.Repeat("Transformer attention mechanism. ", 20)
	res, err := a.Ingest.IngestMarkdown(context.Background(), "zotero://0/ITEM1", "paper.md", "text/markdown", md, ingest.Options{
		Title: "Attention Is All You Need", OriginType: "zotero",
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := a.Store.UpsertLibraryItem(context.Background(), model.LibraryItem{
		ItemKey: "ITEM1", Title: "Attention Is All You Need", Year: 2017,
		Authors: []string{"Vaswani, Ashish"}, Journal: "NeurIPS", DOI: "10.5555/3295222",
		ItemType: "journalArticle", CiteKey: "vaswani2017attention", DocID: res.DocID,
		SyncStatus: "ok",
	}); err != nil {
		t.Fatalf("upsert library item: %v", err)
	}
	_ = a.Store.SetMeta(context.Background(), "zotero_cite_style", "gb-t-7714-numeric")
	return &mcpEnv{mgr: mgr, lib: lib, act: act, kb1: first.ID, embedCalls: embedCalls}
}

func connectClient(t *testing.T, srv *mcp.Server) *sdkmcp.ClientSession {
	t.Helper()
	return connectClientAt(t, srv, "/mcp")
}

func connectClientAt(t *testing.T, srv *mcp.Server, path string) *sdkmcp.ClientSession {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "larder-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &sdkmcp.StreamableClientTransport{Endpoint: ts.URL + path},
		&sdkmcp.ClientSessionOptions{ProtocolVersion: "2025-06-18"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callTool(t *testing.T, session *sdkmcp.ClientSession, name string, args map[string]any) *sdkmcp.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func resultText(t *testing.T, res *sdkmcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		return ""
	}
	tc, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		return ""
	}
	return tc.Text
}

func structured(t *testing.T, res *sdkmcp.CallToolResult) map[string]any {
	t.Helper()
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured: %v", err)
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func TestMCPSelectionAndReferences(t *testing.T) {
	env := setupMCPEnv(t)
	srv := mcp.New(env.mgr, env.lib, "http://127.0.0.1:8765", env.act)
	session := connectClient(t, srv)

	// 1. 多文献库时首次检索返回 selection_required。
	res := callTool(t, session, "larder_search", map[string]any{"query": "transformer"})
	if res.IsError {
		t.Fatalf("search should not error: %s", resultText(t, res))
	}
	out := structured(t, res)
	if out["selection_required"] != true {
		t.Fatalf("expected selection_required, got %+v (%s)", out, resultText(t, res))
	}
	if !strings.Contains(resultText(t, res), "请选择文献库") {
		t.Fatalf("selection text missing: %s", resultText(t, res))
	}

	// 2. 显式留空调用 select_library 也能触发选择。
	res = callTool(t, session, "larder_select_library", map[string]any{})
	if res.IsError {
		t.Fatalf("empty select: %s", resultText(t, res))
	}
	sel := structured(t, res)
	if sel["selection_required"] != true {
		t.Fatalf("empty select should return selection_required: %+v", sel)
	}
	if libs, _ := sel["libraries"].([]any); len(libs) != 2 {
		t.Fatalf("expected 2 libraries, got %+v", sel["libraries"])
	}

	// 3. 选择文献库后，检索返回论文级引用。
	res = callTool(t, session, "larder_select_library", map[string]any{"library": env.kb1})
	if res.IsError {
		t.Fatalf("select: %s", resultText(t, res))
	}
	res = callTool(t, session, "larder_search", map[string]any{"query": "transformer", "kb": env.kb1})
	if res.IsError {
		t.Fatalf("search after select: %s", resultText(t, res))
	}
	out = structured(t, res)
	refs, _ := out["references"].([]any)
	if len(refs) != 1 {
		t.Fatalf("expected 1 reference, got %+v (%s)", out["references"], resultText(t, res))
	}
	ref := refs[0].(map[string]any)
	mark, _ := ref["cite_link"].(string)
	if !strings.HasPrefix(mark, "[<sup>[1]</sup>](") || ref["ref_no"] != float64(1) {
		t.Fatalf("unexpected ref: %+v", ref)
	}
	if !strings.Contains(resultText(t, res), "## 参考文献") || !strings.Contains(resultText(t, res), "[1]") {
		t.Fatalf("reference block missing: %s", resultText(t, res))
	}
	results, _ := out["results"].([]any)
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	refMark, _ := results[0].(map[string]any)["cite_link"].(string)
	if !strings.HasPrefix(refMark, "[<sup>[1]</sup>](") {
		t.Fatalf("result cite_link missing sup hover link: %+v", results[0])
	}

	// 3. 悬浮模式：链接文字是文内标记，title 包含文献词条和原文。
	res = callTool(t, session, "larder_search", map[string]any{"query": "transformer", "kb": env.kb1})
	if res.IsError {
		t.Fatalf("hover search: %s", resultText(t, res))
	}
	hoverOut := structured(t, res)
	hoverRefs, _ := hoverOut["references"].([]any)
	if len(hoverRefs) == 0 {
		t.Fatal("expected hover references")
	}
	hoverLink := hoverRefs[0].(map[string]any)["cite_link"].(string)
	if !strings.HasPrefix(hoverLink, "[<sup>[1]</sup>](") || !strings.Contains(hoverLink, "【文献词条】") || !strings.Contains(hoverLink, "【原文】") {
		t.Fatalf("unexpected hover link: %s", hoverLink)
	}

	// 4. 跨库检索只调用一次 embedding。
	atomic.StoreInt64(env.embedCalls, 0)
	res = callTool(t, session, "larder_search", map[string]any{"query": "transformer", "kb": "all"})
	if res.IsError {
		t.Fatalf("all search: %s", resultText(t, res))
	}
	if n := atomic.LoadInt64(env.embedCalls); n != 1 {
		t.Fatalf("multi-kb search should embed query once, got %d calls", n)
	}

	// 5. 显式取参考文献与 BibTeX。
	docID := ref["doc_id"].(string)
	res = callTool(t, session, "larder_get_references", map[string]any{"kb": env.kb1, "doc_ids": docID, "include_bibtex": true})
	if res.IsError {
		t.Fatalf("get refs: %s", resultText(t, res))
	}
	refsOut := structured(t, res)
	md, _ := refsOut["markdown"].(string)
	bib, _ := refsOut["bibtex"].(string)
	if !strings.Contains(md, "## 参考文献") || !strings.Contains(bib, "@article{vaswani2017attention") {
		t.Fatalf("unexpected refs output: md=%q bib=%q", md, bib)
	}

	// 4. 导出 .bib 文件。
	res = callTool(t, session, "larder_export_bibliography", map[string]any{"kb": env.kb1, "doc_ids": docID})
	if res.IsError {
		t.Fatalf("export: %s", resultText(t, res))
	}
	exp := structured(t, res)
	path, _ := exp["path"].(string)
	if path == "" {
		t.Fatalf("missing export path: %+v", exp)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("export file missing: %v", err)
	}

	// 5. 列表与状态。
	res = callTool(t, session, "larder_list_libraries", map[string]any{})
	if res.IsError {
		t.Fatalf("list: %s", resultText(t, res))
	}
	listOut := structured(t, res)
	if listOut["count"] != float64(2) {
		t.Fatalf("expected 2 libraries, got %+v", listOut)
	}
	res = callTool(t, session, "larder_library_status", map[string]any{"kb": env.kb1})
	if res.IsError {
		t.Fatalf("status: %s", resultText(t, res))
	}
	if !strings.Contains(resultText(t, res), "共 1 条") {
		t.Fatalf("unexpected status: %s", resultText(t, res))
	}
}

func TestMCPPlainCitationZCode(t *testing.T) {
	env := setupMCPEnv(t)
	cfg := env.mgr.Config()
	cfg.Citation.Profiles["zcode"] = config.CitationStyle{
		Mode: "plain", Fields: []string{"link", "reference", "file", "text"},
	}
	env.mgr.ApplyConfig(cfg)

	srv := mcp.New(env.mgr, env.lib, "http://127.0.0.1:8765", env.act)
	session := connectClientAt(t, srv, "/mcp?client=zcode")

	res := callTool(t, session, "larder_search", map[string]any{"query": "transformer", "kb": env.kb1})
	if res.IsError {
		t.Fatalf("plain search: %s", resultText(t, res))
	}
	out := structured(t, res)
	refs, _ := out["references"].([]any)
	if len(refs) == 0 {
		t.Fatal("expected plain references")
	}
	ref := refs[0].(map[string]any)
	if ref["cite_mark"] != "[1]" {
		t.Fatalf("plain cite_mark = %v, want [1]", ref["cite_mark"])
	}
	link, _ := ref["cite_link"].(string)
	if link != "[1]" {
		t.Fatalf("plain cite_link = %q, want [1]", link)
	}
	results, _ := out["results"].([]any)
	if len(results) == 0 || results[0].(map[string]any)["ref_mark"] != "[1]" {
		t.Fatalf("plain result ref_mark missing: %+v", out["results"])
	}
	if strings.Contains(resultText(t, res), "[[1]](") || strings.Contains(resultText(t, res), "http://127.0.0.1:8765/r/") {
		t.Fatalf("plain body must only contain the citation mark: %s", resultText(t, res))
	}
}

func TestMCPPlainCitationCherryStudio(t *testing.T) {
	env := setupMCPEnv(t)
	cfg := env.mgr.Config()
	cfg.Citation.Profiles["cherrystudio"] = config.CitationStyle{
		Mode: "plain", Fields: []string{"reference", "link", "file", "text"},
	}
	env.mgr.ApplyConfig(cfg)

	srv := mcp.New(env.mgr, env.lib, "http://127.0.0.1:8765", env.act)
	session := connectClientAt(t, srv, "/mcp?client=cherrystudio")

	res := callTool(t, session, "larder_search", map[string]any{"query": "transformer", "kb": env.kb1})
	if res.IsError {
		t.Fatalf("cherry plain search: %s", resultText(t, res))
	}
	out := structured(t, res)
	refs, _ := out["references"].([]any)
	if len(refs) == 0 {
		t.Fatal("expected cherry plain references")
	}
	ref := refs[0].(map[string]any)
	if ref["cite_mark"] != "[1]" {
		t.Fatalf("cherry plain cite_mark = %v, want [1]", ref["cite_mark"])
	}
	link, _ := ref["cite_link"].(string)
	if link != "[1]" {
		t.Fatalf("cherry plain cite_link = %q, want [1]", link)
	}
	if strings.Contains(resultText(t, res), "【文献词条】") || strings.Contains(resultText(t, res), "http://127.0.0.1:8765/r/") {
		t.Fatalf("cherry plain body must only contain [1]: %s", resultText(t, res))
	}
}

func connectElicitingClient(t *testing.T, srv *mcp.Server, path string, choose func(context.Context, *sdkmcp.ElicitRequest) (*sdkmcp.ElicitResult, error)) *sdkmcp.ClientSession {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "larder-elicit-test", Version: "1"}, &sdkmcp.ClientOptions{
		ElicitationHandler: choose,
	})
	session, err := client.Connect(context.Background(), &sdkmcp.StreamableClientTransport{Endpoint: ts.URL + path}, nil)
	if err != nil {
		t.Fatalf("connect eliciting client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestMCPElicitsLibrarySelection(t *testing.T) {
	env := setupMCPEnv(t)
	srv := mcp.New(env.mgr, env.lib, "http://127.0.0.1:8765", env.act)

	var asked int
	session := connectElicitingClient(t, srv, "/mcp", func(_ context.Context, req *sdkmcp.ElicitRequest) (*sdkmcp.ElicitResult, error) {
		asked++
		if !strings.Contains(req.Params.Message, "文献库") {
			t.Fatalf("unexpected elicitation message: %s", req.Params.Message)
		}
		schema, ok := req.Params.RequestedSchema.(map[string]any)
		if !ok {
			t.Fatalf("requested schema type = %T", req.Params.RequestedSchema)
		}
		props, _ := schema["properties"].(map[string]any)
		lib, _ := props["library"].(map[string]any)
		ids, _ := lib["enum"].([]any)
		found := false
		for _, id := range ids {
			if id == env.kb1 {
				found = true
			}
		}
		if !found {
			t.Fatalf("library enum missing %q: %+v", env.kb1, ids)
		}
		return &sdkmcp.ElicitResult{Action: "accept", Content: map[string]any{"library": env.kb1}}, nil
	})

	res := callTool(t, session, "larder_search", map[string]any{"query": "transformer"})
	if res.IsError {
		t.Fatalf("elicited search: %s", resultText(t, res))
	}
	if asked != 1 {
		t.Fatalf("expected one elicitation, got %d", asked)
	}
	out := structured(t, res)
	if out["selection_required"] == true {
		t.Fatalf("search should continue after elicitation: %+v", out)
	}
	refs, _ := out["references"].([]any)
	if len(refs) == 0 {
		t.Fatalf("expected search results after elicitation: %+v", out)
	}
}

func TestMCPElicitationDeclineFallsBack(t *testing.T) {
	env := setupMCPEnv(t)
	srv := mcp.New(env.mgr, env.lib, "http://127.0.0.1:8765", env.act)
	session := connectElicitingClient(t, srv, "/mcp", func(context.Context, *sdkmcp.ElicitRequest) (*sdkmcp.ElicitResult, error) {
		return &sdkmcp.ElicitResult{Action: "decline"}, nil
	})
	res := callTool(t, session, "larder_search", map[string]any{"query": "transformer"})
	if res.IsError {
		t.Fatalf("declined search: %s", resultText(t, res))
	}
	out := structured(t, res)
	if out["selection_required"] != true {
		t.Fatalf("decline should fall back to selection_required: %+v", out)
	}
	if !strings.Contains(resultText(t, res), "已取消选择") {
		t.Fatalf("decline fallback text missing: %s", resultText(t, res))
	}
}

func TestMCPElicitsSelectLibraryTool(t *testing.T) {
	env := setupMCPEnv(t)
	srv := mcp.New(env.mgr, env.lib, "http://127.0.0.1:8765", env.act)
	session := connectElicitingClient(t, srv, "/mcp", func(_ context.Context, req *sdkmcp.ElicitRequest) (*sdkmcp.ElicitResult, error) {
		return &sdkmcp.ElicitResult{Action: "accept", Content: map[string]any{"library": env.kb1}}, nil
	})
	res := callTool(t, session, "larder_select_library", map[string]any{})
	if res.IsError {
		t.Fatalf("elicit select: %s", resultText(t, res))
	}
	if !strings.Contains(resultText(t, res), "已选择文献库") {
		t.Fatalf("unexpected select result: %s", resultText(t, res))
	}
	out := structured(t, res)
	if out["kb_id"] != env.kb1 {
		t.Fatalf("selected kb = %v, want %s", out["kb_id"], env.kb1)
	}
}

func connectDirectElicitingClient(t *testing.T, srv *mcp.Server, path string, choose func(context.Context, *sdkmcp.ElicitRequest) (*sdkmcp.ElicitResult, error)) *sdkmcp.ClientSession {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "larder-direct-elicit-test", Version: "1"}, &sdkmcp.ClientOptions{
		ElicitationHandler: choose,
	})
	session, err := client.Connect(context.Background(), &sdkmcp.StreamableClientTransport{Endpoint: ts.URL + path},
		&sdkmcp.ClientSessionOptions{ProtocolVersion: "2025-06-18"})
	if err != nil {
		t.Fatalf("connect direct eliciting client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestMCPDirectElicitsLibrarySelection(t *testing.T) {
	env := setupMCPEnv(t)
	srv := mcp.New(env.mgr, env.lib, "http://127.0.0.1:8765", env.act)
	var asked int
	session := connectDirectElicitingClient(t, srv, "/mcp", func(_ context.Context, req *sdkmcp.ElicitRequest) (*sdkmcp.ElicitResult, error) {
		asked++
		if req.Params.Mode != "form" {
			t.Fatalf("direct elicitation mode = %q", req.Params.Mode)
		}
		return &sdkmcp.ElicitResult{Action: "accept", Content: map[string]any{"library": env.kb1}}, nil
	})
	res := callTool(t, session, "larder_search", map[string]any{"query": "transformer"})
	if res.IsError {
		t.Fatalf("direct elicited search: %s", resultText(t, res))
	}
	if asked != 1 {
		t.Fatalf("expected one direct elicitation, got %d", asked)
	}
	out := structured(t, res)
	if out["selection_required"] == true {
		t.Fatalf("search should continue after direct elicitation: %+v", out)
	}
	refs, _ := out["references"].([]any)
	if len(refs) == 0 {
		t.Fatalf("expected results after direct elicitation: %+v", out)
	}
}

func TestMCPUsesCitationReferenceStyle(t *testing.T) {
	env := setupMCPEnv(t)
	cfg := env.mgr.Config()
	profile := cfg.Citation.Profiles["default"]
	profile.ReferenceStyle = "apa"
	cfg.Citation.Profiles["default"] = profile
	env.mgr.ApplyConfig(cfg)
	srv := mcp.New(env.mgr, env.lib, "http://127.0.0.1:8765", env.act)
	session := connectClientAt(t, srv, "/mcp")
	res := callTool(t, session, "larder_search", map[string]any{"query": "transformer", "kb": env.kb1})
	if res.IsError {
		t.Fatalf("apa search: %s", resultText(t, res))
	}
	out := structured(t, res)
	refs, _ := out["references"].([]any)
	if len(refs) == 0 {
		t.Fatal("expected references")
	}
	ref := refs[0].(map[string]any)
	text, _ := ref["reference"].(string)
	if !strings.Contains(text, "(2017).") || strings.Contains(text, "[J]") {
		t.Fatalf("citation style should come from settings: %s", text)
	}
}

func TestMCPPerAgentCitationStyles(t *testing.T) {
	env := setupMCPEnv(t)
	cfg := env.mgr.Config()
	def := cfg.Citation.Profiles["default"]
	def.ReferenceStyle = "apa"
	cfg.Citation.Profiles["default"] = def
	cherry := cfg.Citation.Profiles["cherrystudio"]
	cherry.ReferenceStyle = "ieee"
	cfg.Citation.Profiles["cherrystudio"] = cherry
	env.mgr.ApplyConfig(cfg)
	srv := mcp.New(env.mgr, env.lib, "http://127.0.0.1:8765", env.act)

	search := func(path string) string {
		session := connectClientAt(t, srv, path)
		res := callTool(t, session, "larder_search", map[string]any{"query": "transformer", "kb": env.kb1})
		if res.IsError {
			t.Fatalf("search %s: %s", path, resultText(t, res))
		}
		out := structured(t, res)
		refs, _ := out["references"].([]any)
		if len(refs) == 0 {
			t.Fatalf("no references for %s", path)
		}
		text, _ := refs[0].(map[string]any)["reference"].(string)
		return text
	}

	if got := search("/mcp?client=default"); !strings.Contains(got, "(2017).") || strings.Contains(got, "[J]") {
		t.Fatalf("default profile should use APA: %s", got)
	}
	if got := search("/mcp?client=cherrystudio"); !strings.Contains(got, "\"Attention Is All You Need,\"") {
		t.Fatalf("cherry profile should use IEEE: %s", got)
	}
}

func TestMCPGetReferencesHoverLink(t *testing.T) {
	env := setupMCPEnv(t)
	srv := mcp.New(env.mgr, env.lib, "http://127.0.0.1:8765", env.act)
	session := connectClientAt(t, srv, "/mcp?client=cherrystudio")

	list := callTool(t, session, "larder_list_documents", map[string]any{"kb": env.kb1})
	if list.IsError {
		t.Fatalf("list docs: %s", resultText(t, list))
	}
	docs, _ := structured(t, list)["documents"].([]any)
	if len(docs) == 0 {
		t.Fatal("expected documents")
	}
	docID, _ := docs[0].(map[string]any)["doc_id"].(string)
	res := callTool(t, session, "larder_get_references", map[string]any{"kb": env.kb1, "doc_ids": docID})
	if res.IsError {
		t.Fatalf("get refs: %s", resultText(t, res))
	}
	refs, _ := structured(t, res)["references"].([]any)
	if len(refs) == 0 {
		t.Fatal("expected references")
	}
	ref := refs[0].(map[string]any)
	link, _ := ref["cite_link"].(string)
	if !strings.HasPrefix(link, "[<sup>[1]</sup>](") || !strings.Contains(link, `"【文献词条】`) || !strings.Contains(link, "【原文】") {
		t.Fatalf("get_references should expose hover link: %s", link)
	}
	if !strings.Contains(resultText(t, res), "上方「正文引用」表") {
		t.Fatalf("missing hover citation hint: %s", resultText(t, res))
	}
}

func TestMCPSettingsApplyToExistingSession(t *testing.T) {
	env := setupMCPEnv(t)
	cfg := env.mgr.Config()
	p := cfg.Citation.Profiles["default"]
	p.ReferenceStyle = "apa"
	cfg.Citation.Profiles["default"] = p
	env.mgr.ApplyConfig(cfg)
	srv := mcp.New(env.mgr, env.lib, "http://127.0.0.1:8765", env.act)
	session := connectClientAt(t, srv, "/mcp?client=default")

	searchRef := func() string {
		res := callTool(t, session, "larder_search", map[string]any{"query": "transformer", "kb": env.kb1})
		if res.IsError {
			t.Fatalf("search: %s", resultText(t, res))
		}
		refs, _ := structured(t, res)["references"].([]any)
		if len(refs) == 0 {
			t.Fatal("expected references")
		}
		text, _ := refs[0].(map[string]any)["reference"].(string)
		return text
	}
	if got := searchRef(); !strings.Contains(got, "(2017).") {
		t.Fatalf("initial style should be APA: %s", got)
	}

	// 不新建 MCP 会话，直接改配置后再次检索。
	cfg = env.mgr.Config()
	p = cfg.Citation.Profiles["default"]
	p.ReferenceStyle = "ieee"
	cfg.Citation.Profiles["default"] = p
	env.mgr.ApplyConfig(cfg)
	if got := searchRef(); !strings.Contains(got, `"Attention Is All You Need,"`) {
		t.Fatalf("existing session should pick up new style: %s", got)
	}
}
