package mcp

import (
	"strings"
	"testing"

	"larder/internal/config"
	"larder/internal/model"
)

func TestFormatCitationsUsesRightAlignedBadge(t *testing.T) {
	out := formatCitations([]model.SearchHit{{
		DocTitle: "英语六级核心词",
		Source:   "1.pdf",
		Heading:  "Unit 1",
		Text:     "surface 表面",
	}}, "英语 <六级>", badgeMarkdown("英语 <六级>"), "http://127.0.0.1:8765", "log1", config.CitationStyle{Mode: "markdown", Fields: []string{"doc", "file", "text"}}, 220)

	if !strings.HasPrefix(out, "|  | **📚 Larder · 英语 <六级>** |") {
		t.Fatalf("citations should start with a right-aligned Markdown table badge: %q", out)
	}
	if !strings.Contains(out, "|:--|--:|") {
		t.Fatalf("badge table right-alignment missing: %q", out)
	}
	if strings.Contains(out, "【Larder") || strings.Contains(out, "![Larder") {
		t.Fatalf("plain/image badge should not duplicate the table badge: %q", out)
	}
	if !strings.Contains(out, "文档：英语六级核心词") || !strings.Contains(out, "文件：1.pdf") || !strings.Contains(out, "章节：Unit 1") {
		t.Fatalf("citation metadata missing: %q", out)
	}
}

func TestFormatCitationsEmptyStillHasBadge(t *testing.T) {
	out := formatCitations(nil, "KB", badgeMarkdown("KB"), "http://127.0.0.1:8765", "log1", config.CitationStyle{Mode: "markdown", Fields: []string{"doc", "file", "text"}}, 220)
	if !strings.HasPrefix(out, "|  | **📚 Larder · KB** |") || !strings.Contains(out, "未检索到相关内容") {
		t.Fatalf("unexpected empty result: %q", out)
	}
}

func TestZCodeCitationIDIsComplete(t *testing.T) {
	hits := []model.SearchHit{{
		DocID: "3cb6792f-1730-0000-0000-000000000000", ChunkID: 173,
		DocTitle: "组内研究进展", Source: "组内研究进展.pdf", Heading: "技术路径",
		Text: "非均匀 B 样条时基对齐",
	}}
	style := config.CitationStyle{Mode: "plain", Fields: []string{"cite", "log", "doc", "file", "heading", "text"}}
	out := toCitations(hits, "http://127.0.0.1:8765", "evt1", style, true, 120)
	if len(out) != 1 {
		t.Fatalf("got %d citations", len(out))
	}
	c := out[0]
	if c.CiteID != c.CiteLink {
		t.Fatalf("zcode cite_id should be the complete citation: id=%q link=%q", c.CiteID, c.CiteLink)
	}
	if !strings.HasPrefix(c.CiteID, "[cite:3cb6792f-173]（【日志】http://127.0.0.1:8765/c/evt1；【文档】") {
		t.Fatalf("zcode citation should keep token, log-first parentheses and source: %q", c.CiteID)
	}
}

func TestCitationWithoutLogHasNoLogLink(t *testing.T) {
	hits := []model.SearchHit{{
		DocID: "3cb6792f-1730-0000-0000-000000000000", ChunkID: 173,
		DocTitle: "组内研究进展", Source: "组内研究进展.pdf",
		Text: "非均匀 B 样条时基对齐",
	}}
	style := config.CitationStyle{Mode: "plain", Fields: []string{"cite", "doc", "file"}}
	out := formatCitations(hits, "KB", badgeMarkdown("KB"), "http://127.0.0.1:8765", "evt1", style, 220)
	if strings.Contains(out, "【日志】") || strings.Contains(out, "/c/evt1") {
		t.Fatalf("unchecked log should not emit a log link: %q", out)
	}
	if !strings.Contains(out, "引用：[cite:3cb6792f-173]（【文档】组内研究进展【文件】组内研究进展.pdf）") {
		t.Fatalf("citation without log should still keep the source: %q", out)
	}
}
