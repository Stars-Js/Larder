package rerank

import (
	"context"
	"fmt"
	"sync"
	"time"

	"larder/internal/httpx"
)

// OpenAI 兼容的重排客户端，协议：POST /v1/rerank。
type OpenAI struct {
	client *httpx.Client
	model  string
	path   string

	mu    sync.RWMutex
	usage Usage
}

// NewOpenAI 创建兼容重排客户端。
func NewOpenAI(baseURL, apiKey, model, path string, timeout time.Duration, maxRetries int, qps float64) *OpenAI {
	if path == "" {
		path = "/v1/rerank"
	}
	return &OpenAI{
		client: httpx.New(httpx.Options{
			Name:       "rerank.openai",
			BaseURL:    baseURL,
			APIKey:     apiKey,
			Timeout:    timeout,
			MaxRetries: maxRetries,
			QPS:        qps,
		}),
		model: model,
		path:  path,
	}
}

// Name 返回实现名。
func (o *OpenAI) Name() string { return "openai" }

type rerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n,omitempty"`
}

type rerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
		Score          float64 `json:"score"`
	} `json:"results"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Rerank 返回与 documents 等长的得分数组。
func (o *OpenAI) Rerank(ctx context.Context, query string, documents []string) ([]float64, error) {
	if len(documents) == 0 {
		return nil, nil
	}

	o.mu.Lock()
	o.usage = Usage{}
	o.mu.Unlock()

	var resp rerankResponse
	req := rerankRequest{Model: o.model, Query: query, Documents: documents, TopN: len(documents)}
	if err := o.client.DoJSON(ctx, "POST", o.path, req, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil && resp.Error.Message != "" {
		return nil, fmt.Errorf("rerank.openai: %s", resp.Error.Message)
	}

	scores := make([]float64, len(documents))
	seen := make([]bool, len(documents))
	for _, r := range resp.Results {
		if r.Index < 0 || r.Index >= len(documents) {
			return nil, fmt.Errorf("rerank.openai: 非法 index %d", r.Index)
		}
		// 兼容 relevance_score 与 score 两种字段名。
		if r.RelevanceScore != 0 {
			scores[r.Index] = r.RelevanceScore
		} else {
			scores[r.Index] = r.Score
		}
		seen[r.Index] = true
	}
	// 未返回的候选给一个很低的分，仍保留顺序稳定。
	for i := range seen {
		if !seen[i] {
			scores[i] = -1e9
		}
	}

	prompt := resp.Usage.PromptTokens
	total := resp.Usage.TotalTokens
	if prompt == 0 {
		prompt = estimateTokens(query, documents)
	}
	if total == 0 {
		total = prompt
	}
	o.mu.Lock()
	o.usage.PromptTokens = prompt
	o.usage.TotalTokens = total
	o.mu.Unlock()

	return scores, nil
}

// LastUsage 返回最近一次 Rerank 调用的 token 用量。
func (o *OpenAI) LastUsage() Usage {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.usage
}

func estimateTokens(query string, documents []string) int {
	chars := len([]rune(query))
	for _, d := range documents {
		chars += len([]rune(d))
	}
	n := chars / 3
	if n < 1 {
		n = 1
	}
	return n
}
