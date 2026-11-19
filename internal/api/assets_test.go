package api

import (
	"strings"
	"testing"
)

func TestRewriteAssetURLs(t *testing.T) {
	in := "![fig](asset://a.png)\n\n![中文](asset://b%20c.jpg)"
	out := rewriteAssetURLs("kb1", "doc1", in)
	if !strings.Contains(out, "/api/kbs/kb1/documents/doc1/assets/a.png") {
		t.Fatalf("asset url missing: %s", out)
	}
	if !strings.Contains(out, "/api/kbs/kb1/documents/doc1/assets/b%2520c.jpg") {
		t.Fatalf("escaped asset url unexpected: %s", out)
	}
	if strings.Contains(out, "asset://") {
		t.Fatalf("asset scheme should be rewritten: %s", out)
	}
}
