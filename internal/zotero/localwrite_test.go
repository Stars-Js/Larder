package zotero

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEnsureMarkdownLinkCreatesAndReusesChild(t *testing.T) {
	var mu sync.Mutex
	var child map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Zotero-Server-ID", "SERVER1")
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/api/users/0/items/ITEM1/children", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		items := []map[string]any{}
		if child != nil {
			items = append(items, map[string]any{"key": "ATT2", "data": child})
		}
		_ = json.NewEncoder(w).Encode(items)
	})
	mux.HandleFunc("/api/users/0/items", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Zotero-API-Key") != "key1" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var payload []map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload) != 1 {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		mu.Lock()
		child = payload[0]
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"successful": []map[string]any{{"key": "ATT2"}},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	dir := t.TempDir()
	md := filepath.Join(dir, "paper.md")
	if err := os.WriteFile(md, []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New(Config{DataDir: dir, APIBase: ts.URL + "/api", LibraryID: "0", APIKey: "key1"})
	ctx := context.Background()
	key, created, err := c.EnsureMarkdownLink(ctx, "ITEM1", "Larder Markdown", md)
	if err != nil || !created || key != "ATT2" {
		t.Fatalf("create: key=%q created=%v err=%v", key, created, err)
	}
	if child["linkMode"] != "linked_file" || child["path"] != md || child["contentType"] != "text/markdown" {
		t.Fatalf("unexpected payload: %+v", child)
	}
	key, created, err = c.EnsureMarkdownLink(ctx, "ITEM1", "Larder Markdown", md)
	if err != nil || created || key != "ATT2" {
		t.Fatalf("reuse: key=%q created=%v err=%v", key, created, err)
	}
	if strings.TrimSpace(child["title"].(string)) != "Larder Markdown" {
		t.Fatalf("title = %v", child["title"])
	}
}

func TestAuthorizeLocalWrite(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Zotero-Server-ID", "SERVER1")
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/api/local/authorize", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Zotero-Server-ID") != "SERVER1" {
			http.Error(w, "missing server id", http.StatusPreconditionRequired)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"key": "KEY1", "remember": true})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	c := New(Config{DataDir: t.TempDir(), APIBase: ts.URL + "/api", LibraryID: "0"})
	auth, err := c.AuthorizeLocalWrite(context.Background(), "Larder")
	if err != nil || auth.Key != "KEY1" || !auth.Remember {
		t.Fatalf("auth=%+v err=%v", auth, err)
	}
	if c.cfg.APIKey != "KEY1" {
		t.Fatalf("api key not cached: %q", c.cfg.APIKey)
	}
}

func TestFirstAttachmentKeyShapes(t *testing.T) {
	cases := []string{
		`{"0":{"key":"K1"}}`,
		`[{"key":"K2"}]`,
		`{"key":"K3"}`,
	}
	want := []string{"K1", "K2", "K3"}
	for i, raw := range cases {
		if got := firstAttachmentKey([]byte(raw)); got != want[i] {
			t.Fatalf("%s => %q, want %q", raw, got, want[i])
		}
	}
	if got := firstAttachmentKey([]byte(`{}`)); got != "" {
		t.Fatalf("empty object => %q", got)
	}
}
