package parse

import (
	"context"
	"fmt"
	"strings"
	"time"

	"larder/internal/httpx"
)

// Tika 调用 Apache Tika Server 的 PUT /tika 接口。
// Tika 只能给出纯文本，没有标题层级，切块质量弱于 MinerU/Docling。
type Tika struct {
	client *httpx.Client
	path   string
}

// NewTika 创建 Tika 解析器。
func NewTika(baseURL, apiKey, path string, timeout time.Duration, maxRetries int, qps float64) *Tika {
	if path == "" {
		path = "/tika"
	}
	return &Tika{
		client: httpx.New(httpx.Options{
			Name:       "parse.tika",
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
func (t *Tika) Name() string { return "tika" }

// Tika 返回 text/plain，因此用 DoText 语义：这里复用 multipart 不太合适，
// 直接用 PUT 上传原始字节。
func (t *Tika) Parse(ctx context.Context, in Input) (Result, error) {
	body, err := t.client.DoRaw(ctx, "PUT", t.path, "application/octet-stream", in.Data, "text/plain")
	if err != nil {
		return Result{}, err
	}

	text := strings.TrimSpace(string(body))
	if text == "" {
		return Result{}, fmt.Errorf("tika: 返回内容为空")
	}

	return Result{
		Markdown: text,
		Title:    inferTitle(text, in.FileName),
		MIME:     "text/plain",
		Method:   t.Name(),
		Note:     "Apache Tika 文本抽取",
	}, nil
}
