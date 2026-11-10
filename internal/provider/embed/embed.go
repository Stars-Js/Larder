// Package embed 定义"向量化"这一环节的抽象。
//
// 只依赖 OpenAI 兼容的 /v1/embeddings 接口，因此 Infinity、Xinference、
// SiliconFlow、OpenAI、vLLM 等都能直接对接。
package embed

import "context"

// Usage 是一次 Embed 调用消耗的 token。
type Usage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
	Requests     int `json:"requests"`
}

// UsageReporter 由支持返回 token 用量的实现可选实现。
type UsageReporter interface {
	LastUsage() Usage
}

// Embedder 把一批文本转成向量。
type Embedder interface {
	// Name 返回实现名。
	Name() string
	// Embed 返回与 inputs 等长的向量列表。
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
	// Dim 返回向量维度；未知时返回 0（可在首次调用后探测）。
	Dim() int
}
