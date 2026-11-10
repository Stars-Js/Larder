// Package rerank 定义"重排"这一环节的抽象。
//
// 采用业界通行的 {model, query, documents, top_n} 兼容协议，
// 可对接 SiliconFlow / Xinference / Jina / Cohere 兼容代理等。
package rerank

import "context"

// Usage 是一次重排调用消耗的 token。
type Usage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// UsageReporter 由支持返回 token 用量的实现可选实现。
type UsageReporter interface {
	LastUsage() Usage
}

// Reranker 对候选文档按与 query 的相关性重新打分排序。
type Reranker interface {
	// Name 返回实现名。
	Name() string
	// Rerank 返回每个候选的相关性得分，顺序与 documents 对应。
	Rerank(ctx context.Context, query string, documents []string) ([]float64, error)
}
