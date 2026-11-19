package zotero

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestLocalAPI(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "storage", "ATT1"), 0o755); err != nil {
		t.Fatal(err)
	}
	pdfPath := filepath.Join(dataDir, "storage", "ATT1", "paper.pdf")
	if err := os.WriteFile(pdfPath, []byte("PDFDATA"), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/users/0/collections", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, []map[string]any{{
			"key": "COL1", "version": 1,
			"data": map[string]any{"key": "COL1", "name": "Papers", "parentCollection": false},
			"meta": map[string]any{"numItems": 1},
		}})
	})
	mux.HandleFunc("/api/users/0/collections/COL1/items/top", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, []map[string]any{{
			"key": "ITEM1", "version": 2,
			"data": map[string]any{
				"itemType": "journalArticle", "title": "Paper One", "date": "2021-05-01",
				"DOI": "10.1/x", "url": "https://example.com", "publicationTitle": "Journal",
				"volume": "1", "issue": "2", "pages": "3-4", "abstractNote": "abstract",
				"language": "en", "dateModified": "2021-05-02T00:00:00Z",
				"creators": []map[string]any{{"creatorType": "author", "firstName": "John", "lastName": "Smith"}},
			},
		}})
	})
	mux.HandleFunc("/api/users/0/items/ITEM1/children", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, []map[string]any{{
			"key": "ATT1",
			"data": map[string]any{
				"itemType": "attachment", "contentType": "application/pdf", "filename": "paper.pdf",
				"path": "storage:paper.pdf", "linkMode": "imported_file",
			},
		}})
	})
	mux.HandleFunc("/api/users/0/items/ATT1/fulltext", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"content": "hello fulltext", "indexedPages": 2, "totalPages": 2})
	})
	mux.HandleFunc("/api/users/0/items/ATT1/file", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("PDFDATA"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New(Config{DataDir: dataDir, APIBase: ts.URL + "/api", LibraryID: "0"})
	defer c.Close()
	ctx := context.Background()
	if err := c.ProbeAPI(ctx); err != nil {
		t.Fatalf("ProbeAPI: %v", err)
	}
	cols, err := c.Collections(ctx)
	if err != nil {
		t.Fatalf("Collections: %v", err)
	}
	if len(cols) != 1 || cols[0].Name != "Papers" || cols[0].Key != "COL1" {
		t.Fatalf("unexpected collections: %+v", cols)
	}
	items, err := c.CollectionItems(ctx, "COL1", false)
	if err != nil {
		t.Fatalf("CollectionItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	it := items[0]
	if it.Title != "Paper One" || it.Year() != 2021 || it.DOI != "10.1/x" {
		t.Fatalf("unexpected item: %+v", it)
	}
	if len(it.Attachments) != 1 || it.Attachments[0].Key != "ATT1" {
		t.Fatalf("unexpected attachments: %+v", it.Attachments)
	}
	if got := c.AttachmentPath(it.Attachments[0]); got != pdfPath {
		t.Fatalf("attachment path = %q, want %q", got, pdfPath)
	}
	b, err := c.ReadAttachment(ctx, it.Attachments[0])
	if err != nil || string(b) != "PDFDATA" {
		t.Fatalf("ReadAttachment: %q %v", string(b), err)
	}
	ft, ok, err := c.Fulltext(ctx, "ATT1")
	if err != nil || !ok || ft.Content != "hello fulltext" {
		t.Fatalf("Fulltext: %+v %v %v", ft, ok, err)
	}
}

func TestSQLiteFallback(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "zotero.sqlite")
	if err := os.MkdirAll(filepath.Join(dataDir, "storage", "ATT1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "storage", "ATT1", "paper.pdf"), []byte("PDFDATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "storage", "ATT1", ".zotero-ft-cache"), []byte("cached fulltext"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		`create table collections(collectionID integer primary key, collectionName text, parentCollectionID integer, key text, version integer, synced integer, libraryID integer)`,
		`create table collectionItems(collectionID integer, itemID integer, orderIndex integer)`,
		`create table items(itemID integer primary key, itemTypeID integer, key text, dateAdded text, dateModified text, version integer, synced integer)`,
		`create table itemTypes(itemTypeID integer primary key, typeName text)`,
		`create table itemData(itemID integer, fieldID integer, valueID integer)`,
		`create table itemDataValues(valueID integer primary key, value text)`,
		`create table fields(fieldID integer primary key, fieldName text)`,
		`create table itemCreators(itemID integer, creatorID integer, creatorTypeID integer, orderIndex integer)`,
		`create table creators(creatorID integer primary key, firstName text, lastName text, fieldMode integer)`,
		`create table creatorTypes(creatorTypeID integer primary key, creatorType text)`,
		`create table itemAttachments(itemID integer, parentItemID integer, linkMode integer, contentType text, path text)`,
		`create table deletedItems(itemID integer primary key)`,
		`create table fulltextItems(itemID integer primary key, indexedPages integer, totalPages integer, indexedChars integer, totalChars integer, version integer, synced integer)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("schema: %v\n%s", err, q)
		}
	}
	exec := func(q string, args ...any) {
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	exec(`insert into itemTypes values(1,'journalArticle')`)
	exec(`insert into itemTypes values(2,'attachment')`)
	exec(`insert into collections values(10,'Papers',null,'COL1',1,0,1)`)
	exec(`insert into items values(1,1,'ITEM1','2021-01-01','2021-05-02',2,0)`)
	exec(`insert into items values(2,2,'ATT1','2021-01-01','2021-05-02',2,0)`)
	exec(`insert into collectionItems values(10,1,0)`)
	fields := map[string]string{"title": "Paper One", "date": "2021-05-01", "DOI": "10.1/x", "publicationTitle": "Journal", "volume": "1", "issue": "2", "pages": "3-4", "abstractNote": "abstract", "language": "en"}
	i := 1
	for name, value := range fields {
		exec(`insert into fields values(?,?)`, i, name)
		exec(`insert into itemDataValues values(?,?)`, i, value)
		exec(`insert into itemData values(1,?,?)`, i, i)
		i++
	}
	exec(`insert into creators values(1,'John','Smith',0)`)
	exec(`insert into creatorTypes values(1,'author')`)
	exec(`insert into itemCreators values(1,1,1,0)`)
	exec(`insert into itemAttachments values(2,1,0,'application/pdf','storage:paper.pdf')`)
	exec(`insert into fulltextItems values(2,2,2,100,100,1,0)`)

	c := New(Config{DataDir: dataDir, APIBase: "http://127.0.0.1:1/api", LibraryID: "0", ForceDB: true})
	defer c.Close()
	ctx := context.Background()
	cols, err := c.Collections(ctx)
	if err != nil {
		t.Fatalf("Collections: %v", err)
	}
	if len(cols) != 1 || cols[0].Name != "Papers" {
		t.Fatalf("unexpected collections: %+v", cols)
	}
	items, err := c.CollectionItems(ctx, "COL1", false)
	if err != nil {
		t.Fatalf("CollectionItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	it := items[0]
	if it.Title != "Paper One" || it.DOI != "10.1/x" || it.Year() != 2021 {
		t.Fatalf("unexpected item: %+v", it)
	}
	if it.Authors()[0] != "Smith, John" {
		t.Fatalf("unexpected authors: %+v", it.Authors())
	}
	if len(it.Attachments) != 1 || it.Attachments[0].Key != "ATT1" {
		t.Fatalf("unexpected attachments: %+v", it.Attachments)
	}
	ft, ok, err := c.Fulltext(ctx, "ATT1")
	if err != nil || !ok || ft.Content != "cached fulltext" {
		t.Fatalf("Fulltext: %+v %v %v", ft, ok, err)
	}
}

func TestCollectionTree(t *testing.T) {
	tree := CollectionTree([]Collection{
		{Key: "b", Name: "B", ParentKey: "a"},
		{Key: "a", Name: "A"},
		{Key: "c", Name: "C", ParentKey: "a"},
	})
	if len(tree) != 1 || tree[0].Key != "a" || len(tree[0].Children) != 2 {
		t.Fatalf("unexpected tree: %+v", tree)
	}
	if tree[0].Children[0].Key != "b" || tree[0].Children[1].Key != "c" {
		t.Fatalf("children not sorted: %+v", tree[0].Children)
	}
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestCollectionItemsFetchesChildrenConcurrently(t *testing.T) {
	const n = 20
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/api/users/0/collections/COL1/items/top", func(w http.ResponseWriter, r *http.Request) {
		items := make([]map[string]any, n)
		for i := 0; i < n; i++ {
			items[i] = map[string]any{
				"key": "ITEM" + strconv.Itoa(i), "version": 1,
				"data": map[string]any{"itemType": "journalArticle", "title": "Paper " + strconv.Itoa(i)},
			}
		}
		writeTestJSON(w, items)
	})
	for i := 0; i < n; i++ {
		key := "ITEM" + strconv.Itoa(i)
		mux.HandleFunc("/api/users/0/items/"+key+"/children", func(w http.ResponseWriter, r *http.Request) {
			writeTestJSON(w, []map[string]any{{
				"key": "ATT" + key,
				"data": map[string]any{
					"itemType": "attachment", "contentType": "application/pdf",
					"filename": key + ".pdf", "path": "storage:" + key + ".pdf",
				},
			}})
		})
	}
	ts := httptest.NewServer(mux)
	defer ts.Close()
	c := New(Config{DataDir: t.TempDir(), APIBase: ts.URL + "/api", LibraryID: "0"})
	defer c.Close()
	items, err := c.CollectionItems(context.Background(), "COL1", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != n {
		t.Fatalf("expected %d items, got %d", n, len(items))
	}
	for _, it := range items {
		if len(it.Attachments) != 1 {
			t.Fatalf("item %s missing attachment: %+v", it.Key, it.Attachments)
		}
	}
}
