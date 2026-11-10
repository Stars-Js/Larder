package parse

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestPlainPDFExtract 用真实 PDF 样例验证内置文本层抽取。
// 依赖环境变量 LARDER_TEST_PDF 指向一个文字版 PDF。
func TestPlainPDFExtract(t *testing.T) {
	path := os.Getenv("LARDER_TEST_PDF")
	if path == "" {
		t.Skip("未设置 LARDER_TEST_PDF，跳过 PDF 抽取测试")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 PDF 失败: %v", err)
	}
	res, err := NewPlain().Parse(context.Background(), Input{
		FileName: "sample.pdf",
		MIME:     "application/pdf",
		Data:     data,
	})
	if err != nil {
		t.Fatalf("解析 PDF 失败: %v", err)
	}
	if strings.TrimSpace(res.Markdown) == "" {
		t.Fatal("PDF 抽取文本为空")
	}
	t.Logf("PDF 抽取成功，共 %d 字符；开头：%q", len(res.Markdown), firstRunes(res.Markdown, 60))
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
