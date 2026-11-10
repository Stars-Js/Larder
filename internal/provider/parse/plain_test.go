package parse_test

import (
	"context"
	"strings"
	"testing"

	"larder/internal/provider/parse"
)

func TestPlainMarkdown(t *testing.T) {
	p := parse.NewPlain()
	in := parse.Input{
		FileName: "doc.md",
		MIME:     "text/markdown",
		Data:     []byte("# 我的标题\n\n正文内容。\n"),
	}
	res, err := p.Parse(context.Background(), in)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if res.Title != "我的标题" {
		t.Errorf("标题推断错误: %q", res.Title)
	}
	if !strings.Contains(res.Markdown, "正文内容") {
		t.Errorf("Markdown 内容缺失")
	}
}

func TestPlainCodeFence(t *testing.T) {
	p := parse.NewPlain()
	in := parse.Input{FileName: "main.go", Data: []byte("package main\nfunc main(){}\n")}
	res, err := p.Parse(context.Background(), in)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !strings.HasPrefix(res.Markdown, "```go") {
		t.Errorf("代码文件应被围栏包裹，实际: %q", res.Markdown[:10])
	}
}

func TestPlainRejectsBinary(t *testing.T) {
	p := parse.NewPlain()
	in := parse.Input{FileName: "a.bin", MIME: "application/octet-stream", Data: []byte{0x00, 0x01, 0xff}}
	if _, err := p.Parse(context.Background(), in); err == nil {
		t.Error("二进制文件应被拒绝")
	}
}
