package embed

import (
	"context"
	"fmt"
	"sync"
	"time"

	"larder/internal/httpx"
)

// OpenAI 是 OpenAI 兼容的 /v1/embeddings 客户端。
type OpenAI struct {
	client *httpx.Client
	model  string
	path   string

	mu    sync.RWMutex
	dim   int
	usage Usage
	// batch 控制单次请求的文本数量，避免超出服务端限制。
	batch int
}

// NewOpenAI 创建 OpenAI 兼容 embedder。dim 为 0 时首次调用后自动探测。
func NewOpenAI(baseURL, apiKey, model, path string, dim int, timeout time.Duration, maxRetries int, qps float64) *OpenAI {
	if path == "" {
		path = "/v1/embeddings"
	}
	return &OpenAI{
		client: httpx.New(httpx.Options{
			Name:       "embed.openai",
			BaseURL:    baseURL,
			APIKey:     apiKey,
			Timeout:    timeout,
			MaxRetries: maxRetries,
			QPS:        qps,
		}),
		model: model,
		path:  path,
		dim:   dim,
		batch: 64,
	}
}

// Name 返回实现名。
func (o *OpenAI) Name() string { return "openai" }

// Dim 返回已知维度；未探测到则为 0。
func (o *OpenAI) Dim() int {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.dim
}

type embeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingsResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Embed 分批调用接口，返回与 inputs 顺序一致的向量。
func (o *OpenAI) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	o.mu.Lock()
	o.usage = Usage{}
	o.mu.Unlock()

	out := make([][]float32, 0, len(inputs))
	for start := 0; start < len(inputs); start += o.batch {
		end := start + o.batch
		if end > len(inputs) {
			end = len(inputs)
		}
		vecs, err := o.embedBatch(ctx, inputs[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (o *OpenAI) embedBatch(ctx context.Context, inputs []string) ([][]float32, error) {
	var resp embeddingsResponse
	req := embeddingsRequest{Model: o.model, Input: inputs}
	if err := o.client.DoJSON(ctx, "POST", o.path, req, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil && resp.Error.Message != "" {
		return nil, fmt.Errorf("embed.openai: %s", resp.Error.Message)
	}
	if len(resp.Data) != len(inputs) {
		return nil, fmt.Errorf("embed.openai: 期望 %d 个向量，实际 %d", len(inputs), len(resp.Data))
	}

	// 按 index 归位，防止服务端乱序。
	out := make([][]float32, len(inputs))
	for _, d := range resp.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, fmt.Errorf("embed.openai: 非法 index %d", d.Index)
		}
		out[d.Index] = d.Embedding
	}
	for i, v := range out {
		if len(v) == 0 {
			return nil, fmt.Errorf("embed.openai: 第 %d 个向量为空", i)
		}
	}

	// 记录服务实际返回的维度。配置里的 dim 只是提示值，
	// 换模型后必须以真实响应为准，否则界面和检索维度会显示错误。
	prompt := resp.Usage.PromptTokens
	total := resp.Usage.TotalTokens
	if prompt == 0 {
		prompt = estimateTokens(inputs)
	}
	if total == 0 {
		total = prompt
	}
	o.mu.Lock()
	o.dim = len(out[0])
	o.usage.PromptTokens += prompt
	o.usage.TotalTokens += total
	o.usage.Requests++
	o.mu.Unlock()

	return out, nil
}

// LastUsage 返回最近一次 Embed 调用累计的 token 用量。
func (o *OpenAI) LastUsage() Usage {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.usage
}

func estimateTokens(inputs []string) int {
	total := 0
	for _, s := range inputs {
		n := len([]rune(s)) / 3
		if n < 1 {
			n = 1
		}
		total += n
	}
	return total
}
