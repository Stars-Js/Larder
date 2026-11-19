package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStatsAggregatesDocumentStatuses(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "kb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	docs := []Document{
		{ID: "ok", Source: "a", Title: "A", ParseStatus: "ok", EmbedStatus: "ok"},
		{ID: "embed-error", Source: "b", Title: "B", ParseStatus: "ok", EmbedStatus: "error"},
		{ID: "parse-error", Source: "c", Title: "C", ParseStatus: "error", EmbedStatus: "pending"},
		{ID: "pending", Source: "d", Title: "D", ParseStatus: "running", EmbedStatus: "pending"},
	}
	for _, d := range docs {
		if err := st.UpsertDocument(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 4 || stats.Parsed != 2 || stats.Embedded != 1 || stats.Failed != 2 || stats.Pending != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}
