// Package model 定义跨层共享的领域数据结构。
package model

import "time"

// Document 是一份已完成解析与切块的来源文档。
type Document struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"` // 文件绝对路径或 URL
	Title     string    `json:"title"`
	MIME      string    `json:"mime,omitempty"`
	Hash      string    `json:"hash,omitempty"` // 正文 sha256，用于判重
	Chunks    int       `json:"chunks"`
	CreatedAt time.Time `json:"created_at"`

	// 处理状态：展示这份文档在入库时各环节的结果。
	ParseStatus  string `json:"parse_status,omitempty"`  // pending / running / ok / error
	ParseMethod  string `json:"parse_method,omitempty"`  // plain / mineru / docling / tika
	ParseNote    string `json:"parse_note,omitempty"`    // 解析补充说明（页数、失败原因等）
	EmbedStatus  string `json:"embed_status,omitempty"`  // pending / running / ok / error
	EmbedModel   string `json:"embed_model,omitempty"`   // 向量化所用模型
	EmbedNote    string `json:"embed_note,omitempty"`    // 向量化失败原因
	EmbedDim     int    `json:"embed_dim,omitempty"`     // 向量维度
	RerankStatus string `json:"rerank_status,omitempty"` // off / on（检索时是否应用重排）

	// 来源信息，供“重新处理”使用。
	OriginPath string    `json:"origin_path,omitempty"` // 原始文件绝对路径 / URL / 上传缓存路径
	OriginType string    `json:"origin_type,omitempty"` // file / url / upload
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

// Chunk 是文档切块后的最小检索单元。
type Chunk struct {
	ID      int64  `json:"id"`
	DocID   string `json:"doc_id"`
	Ordinal int    `json:"ordinal"`
	Heading string `json:"heading,omitempty"` // 所属标题路径，如 "安装 > Windows"
	Text    string `json:"text"`
}

// SearchHit 是检索结果，带引用信息与两段得分。
type SearchHit struct {
	ChunkID     int64   `json:"chunk_id"`
	DocID       string  `json:"doc_id"`
	DocTitle    string  `json:"doc_title"`
	Source      string  `json:"source"`
	KBID        string  `json:"kb_id,omitempty"`
	KBName      string  `json:"kb_name,omitempty"`
	Heading     string  `json:"heading,omitempty"`
	Text        string  `json:"text"`
	Score       float64 `json:"score"`           // 最终得分（有重排时为重排分）
	Distance    float64 `json:"vector_distance"` // 向量距离，越小越近
	RerankScore float64 `json:"rerank_score,omitempty"`
}
