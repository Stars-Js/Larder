package parse

import (
	"context"
	"fmt"
	"strings"
	"time"

	"larder/internal/httpx"
)

// Docling 调用 docling-serve 的转换接口，输出 Markdown。
// 默认路径 /v1/convert/file，可用 endpoint.path 覆盖。
type Docling struct {
	client *httpx.Client
	path   string
}

// NewDocling 创建 Docling 解析器。
func NewDocling(baseURL, apiKey, path string, timeout time.Duration, maxRetries int, qps float64) *Docling {
	if path == "" {
		path = "/v1/convert/file"
	}
	return &Docling{
		client: httpx.New(httpx.Options{
			Name:       "parse.docling",
			BaseURL:    baseURL,
			APIKey:     apiKey,
			Timeout:    timeout,
			MaxRetries: maxRetries,
			QPS:        qps,
		}),
		path: path,
	}
}

// Name 返回实现名。
func (d *Docling) Name() string { return "docling" }

type doclingResponse struct {
	Document struct {
		MDContent string `json:"md_content"`
		Markdown  string `json:"markdown"`
	} `json:"document"`
	MDContent string `json:"md_content"`
	Markdown  string `json:"markdown"`
	Status    string `json:"status"`
	Errors    []any  `json:"errors"`
}

// Parse 上传文件并取回 Markdown。
func (d *Docling) Parse(ctx context.Context, in Input) (Result, error) {
	fields := map[string]string{
		"to_formats": "md",
		"do_ocr":     "true",
	}

	var resp doclingResponse
	if err := d.client.DoMultipart(ctx, d.path, "files", in.FileName, in.Data, fields, &resp); err != nil {
		return Result{}, err
	}

	if resp.Status == "failure" {
		return Result{}, fmt.Errorf("docling: 转换失败 (%d 个错误)", len(resp.Errors))
	}

	md := firstNonEmpty(resp.Document.MDContent, resp.Document.Markdown, resp.MDContent, resp.Markdown)
	if strings.TrimSpace(md) == "" {
		return Result{}, fmt.Errorf("docling: 响应中未找到 markdown 内容")
	}

	return Result{
		Markdown: md,
		Title:    inferTitle(md, in.FileName),
		MIME:     "text/markdown",
		Method:   d.Name(),
		Note:     "Docling 转换服务",
	}, nil
}
