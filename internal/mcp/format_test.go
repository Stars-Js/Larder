package mcp

import (
	"strings"
	"testing"

	"larder/internal/model"
)

func TestPaperHoverTitleSkipsLinkField(t *testing.T) {
	item := model.LibraryItem{Title: "论文", AttachmentPath: `C:\papers\paper.pdf`}
	h := model.SearchHit{Text: "原文", Source: "zotero://0/ITEM1"}
	got := paperHoverTitle(item, h, "文献词条", []string{"link", "reference", "file", "text"}, "http://log")
	if strings.Contains(got, "链接") || strings.Contains(got, "http://log") {
		t.Fatalf("hover title should not include link field: %s", got)
	}
	if !strings.Contains(got, "【文献词条】文献词条") || !strings.Contains(got, "【文献文件名】paper.pdf") || !strings.Contains(got, "【原文】原文") {
		t.Fatalf("unexpected hover title: %s", got)
	}
}

func TestHoverCitationLinkEscapesMarkAndUsesSup(t *testing.T) {
	got := hoverCitationLink("[1]", "http://127.0.0.1:8765/r/kb/doc", `文献 "标题"`, true)
	wantPrefix := `[<sup>[1]</sup>](http://127.0.0.1:8765/r/kb/doc "文献 '标题'")`
	if got != wantPrefix {
		t.Fatalf("hover link = %q, want %q", got, wantPrefix)
	}
}

func TestFormatPaperCitationsPutsLinksBeforeSnippets(t *testing.T) {
	hits := []model.SearchHit{{DocID: "doc1", DocTitle: "论文", Text: "正文片段"}}
	link := `[<sup>[1]</sup>](http://127.0.0.1:8765/r/kb/doc1 "【文献词条】论文")`
	refs := map[string]reference{
		"doc1": {RefNo: 1, DocID: "doc1", Title: "论文", Mark: "[1]", CiteLink: link},
	}
	out := formatPaperCitations(hits, refs, "KB", badgeMarkdown("KB"))
	if !strings.Contains(out, "【正文引用") || !strings.Contains(out, link) {
		t.Fatalf("citation link table missing: %s", out)
	}
	if strings.Index(out, "【正文引用") > strings.Index(out, "找到 1 条") {
		t.Fatalf("citation links should come before snippets: %s", out)
	}
	if strings.Contains(out, "引用：[<sup>") {
		t.Fatalf("per-hit duplicate citation link should be removed: %s", out)
	}
}

func TestCitationLinkListCompact(t *testing.T) {
	refs := []reference{{
		RefNo: 1, Title: "论文",
		CiteLink: `[<sup>[1]</sup>](http://127.0.0.1:8765/r/kb/doc1 "t")`,
	}}
	full := citationLinkList(refs, false)
	if !strings.Contains(full, "[1] 论文") || !strings.Contains(full, refs[0].CiteLink) {
		t.Fatalf("full link list = %s", full)
	}
	compact := citationLinkList(refs, true)
	if strings.Contains(compact, "论文") || !strings.Contains(compact, refs[0].CiteLink) {
		t.Fatalf("compact link list = %s", compact)
	}
}
