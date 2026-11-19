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
