// Package store 提供本地单文件存储：文档/切块元数据用普通表，
// 向量用 sqlite-vec 的 vec0 虚拟表，全部落在同一个 .db 文件里。
package store

import (
	"context"
	"time"

	"larder/internal/model"
)

// Document 是待写入的文档记录。
type Document struct {
	ID     string
	Source string
	Title  string
	MIME   string
	Hash   string
	Chunks int

	ParseStatus  string
	ParseMethod  string
	ParseNote    string
	EmbedStatus  string
	EmbedModel   string
	EmbedNote    string
	EmbedDim     int
	RerankStatus string
	OriginPath   string
	OriginType   string
	Markdown     string // 解析后的完整 Markdown，仅用于预览；为空时保留旧值
}

// DocStatus 是处理流水线各环节的最新状态，用于在失败/进行中及时落库。
type DocStatus struct {
	ParseStatus  string
	ParseMethod  string
	ParseNote    string
	EmbedStatus  string
	EmbedModel   string
	EmbedNote    string
	EmbedDim     int
	RerankStatus string
	Chunks       int
}

// Hit 是向量召回的一条原始结果（未重排）。
type Hit struct {
	ChunkID  int64
	DocID    string
	DocTitle string
	Source   string
	Ordinal  int
	Heading  string
	Text     string
	Distance float64
}

// UsageEvent 是一条用量记录（解析 / 嵌入 / 重排）。
type UsageEvent struct {
	TS     time.Time
	Kind   string // parse / embed / rerank
	Model  string
	Tokens int64
	Chars  int64
	Status string // ok / error
}

// UsageDay 是按天聚合的用量。
type UsageDay struct {
	Day          string `json:"day"`
	Parse        int64  `json:"parse"`
	EmbedTokens  int64  `json:"embed_tokens"`
	RerankTokens int64  `json:"rerank_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
}

// UsageModel 是按模型聚合的用量。
type UsageModel struct {
	Kind   string `json:"kind"`
	Model  string `json:"model"`
	Count  int64  `json:"count"`
	Tokens int64  `json:"tokens"`
}

// UsageSummary 是一段时间内的用量汇总。
type UsageSummary struct {
	ParseCount   int64        `json:"parse_count"`
	ParseFailed  int64        `json:"parse_failed"`
	ParseChars   int64        `json:"parse_chars"`
	EmbedCount   int64        `json:"embed_count"`
	EmbedTokens  int64        `json:"embed_tokens"`
	RerankCount  int64        `json:"rerank_count"`
	RerankTokens int64        `json:"rerank_tokens"`
	TotalTokens  int64        `json:"total_tokens"`
	ByDay        []UsageDay   `json:"by_day"`
	ByModel      []UsageModel `json:"by_model"`
}

// Store 是存储层接口，便于在 service 层替换/测试。
type Store interface {
	// EnsureDim 确保向量表以指定维度存在；维度冲突返回错误。
	EnsureDim(dim int) error
	// Dim 返回当前向量维度，未初始化返回 0。
	Dim() int

	// FindDocByHash 按 source+hash 查已存在文档，用于判重。
	FindDocByHash(ctx context.Context, source, hash string) (model.Document, bool, error)
	// FindDocBySource 按来源查最近的文档，用于同一来源重新入库/重新处理。
	FindDocBySource(ctx context.Context, source string) (model.Document, bool, error)
	// UpsertDocument 写入或更新文档记录。
	UpsertDocument(ctx context.Context, doc Document) error
	// SetDocumentStatus 更新处理流水线状态（解析/向量化/重排）。
	SetDocumentStatus(ctx context.Context, docID string, st DocStatus) error
	// DeleteDocumentsByOriginExcept 删除同一原始文件/URL 的其它旧版本文档。
	DeleteDocumentsByOriginExcept(ctx context.Context, originPath, keepID string) error
	// ReplaceChunks 用新切块替换某文档的全部切块（含向量）。
	ReplaceChunks(ctx context.Context, docID string, chunks []model.Chunk, vectors [][]float32) error
	// DeleteDocument 删除文档及其切块与向量。
	DeleteDocument(ctx context.Context, docID string) error

	// Search 向量 KNN 召回 k 条。
	Search(ctx context.Context, vector []float32, k int) ([]Hit, error)
	// GetChunks 按 chunkID 批量取回文本，用于重排。
	GetChunks(ctx context.Context, ids []int64) (map[int64]model.Chunk, error)

	// ListDocuments 列出全部文档。
	ListDocuments(ctx context.Context) ([]model.Document, error)
	// GetDocument 按 ID 取文档。
	GetDocument(ctx context.Context, id string) (model.Document, bool, error)
	// GetDocumentMarkdown 返回解析后的完整 Markdown（可能为空）。
	GetDocumentMarkdown(ctx context.Context, docID string) (string, error)
	// ListChunks 按序号返回某文档的全部切块。
	ListChunks(ctx context.Context, docID string) ([]model.Chunk, error)
	// CountChunks 统计某文档（docID 为空则全库）的切块数。
	CountChunks(ctx context.Context, docID string) (int, error)

	// AddUsage 追加一条用量记录。
	AddUsage(ctx context.Context, ev UsageEvent) error
	// UsageSummary 返回 since 之后的用量汇总（since 为零表示全部）。
	UsageSummary(ctx context.Context, since time.Time) (UsageSummary, error)

	// GetMeta 读取 meta 表中的键值；不存在时 ok=false。
	GetMeta(ctx context.Context, key string) (value string, ok bool, err error)
	// SetMeta 写入 meta 表键值（存在则覆盖）。
	SetMeta(ctx context.Context, key, value string) error

	Close() error
}
