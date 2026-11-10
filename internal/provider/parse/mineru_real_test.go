package parse

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestRealMinerU 用真实 MinerU 云 API 解析一个大 PDF（默认 ./1.pdf）。
// 仅在设置了 LARDER_MINERU_TOKEN 时运行，用于端到端验证扫描件 OCR。
//
// 运行方式（token 只放在环境变量里，不会被打印）：
//
//	$env:LARDER_MINERU_TOKEN="你的token"
//	$env:LARDER_TEST_PDF="e:\Academic\Code\Larder\1.pdf"   # 可选，默认 1.pdf
//	go test ./internal/provider/parse/ -run TestRealMinerU -v -timeout 30m
func TestRealMinerU(t *testing.T) {
	token := os.Getenv("LARDER_MINERU_TOKEN")
	if token == "" {
		t.Skip("未设置 LARDER_MINERU_TOKEN，跳过真实 MinerU 测试")
	}
	path := os.Getenv("LARDER_TEST_PDF")
	if path == "" {
		path = "1.pdf"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	t.Logf("文件: %s, 大小: %.1f MB", path, float64(len(data))/(1<<20))

	p := NewMinerU("https://mineru.net", token, "", 120*time.Second, 2, 0)
	if !p.cloud {
		t.Fatalf("期望云 API 模式")
	}
	// 支持用 LARDER_MINERU_PAGES 限制页码范围（如 "1-3"），便于快速验证。
	if pages := os.Getenv("LARDER_MINERU_PAGES"); pages != "" {
		p.WithPageRanges(pages)
		t.Logf("页码范围: %s", pages)
	}
	if mv := os.Getenv("LARDER_MINERU_MODEL"); mv != "" {
		p.WithModelVersion(mv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 28*time.Minute)
	defer cancel()

	start := time.Now()
	res, err := p.Parse(ctx, Input{FileName: "1.pdf", MIME: "application/pdf", Data: data})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	elapsed := time.Since(start)

	md := res.Markdown
	t.Logf("解析成功，耗时 %s，Markdown 字符数: %d，标题: %q", elapsed.Round(time.Second), len([]rune(md)), res.Title)

	// 打印开头片段便于人工核对（避免刷屏）。
	sample := []rune(md)
	if len(sample) > 800 {
		sample = sample[:800]
	}
	t.Logf("Markdown 开头片段:\n%s", string(sample))

	if len([]rune(md)) < 100 {
		t.Fatalf("解析出的 Markdown 过短（%d 字符），可能异常", len([]rune(md)))
	}

	// 可选：把完整 Markdown 落盘，便于人工核对。
	if out := os.Getenv("LARDER_MINERU_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
			t.Fatalf("写出 Markdown 失败: %v", err)
		}
		t.Logf("Markdown 已写入: %s", out)
	}
}
