// Package app 负责把配置、存储与各环节服务装配成可运行的应用。
package app

import (
	"log/slog"
	"path/filepath"
	"time"

	"larder/internal/chunk"
	"larder/internal/config"
	"larder/internal/ingest"
	"larder/internal/provider/embed"
	"larder/internal/provider/parse"
	"larder/internal/provider/rerank"
	"larder/internal/search"
	"larder/internal/services"
	"larder/internal/store"
)

// Version 是应用版本号。
const Version = "1.0.0"

// Overrides 是单个知识库可覆盖的全局设置（来自知识库设置）。
type Overrides struct {
	ParseProvider string         // "" 表示跟随全局
	RerankEnabled *bool          // nil 表示跟随全局
	Chunk         *chunk.Options // nil 表示跟随全局
	TopK          int            // 0 表示跟随全局
	CandidateK    int            // 0 表示跟随全局
}

// App 是单个知识库的运行实例：持有该库的存储与编排服务。
// 解析/向量/重排三个外部服务由共享的 services.Container 提供，可热更新。
type App struct {
	ID        string // 所属知识库 ID（单库模式下为空）
	DBPath    string // 数据库文件路径
	Cfg       config.Config
	Store     store.Store
	Ingest    *ingest.Service
	Search    *search.Service
	Embedder  embed.Embedder
	Reranker  rerank.Reranker
	Parser    parse.Parser
	Logger    *slog.Logger
	StartedAt time.Time
}

// New 依据配置构建 App（使用配置里的单库路径与一个私有服务容器）。
func New(cfg config.Config, logger *slog.Logger) (*App, func(), error) {
	sc, err := services.New(cfg, logger)
	if err != nil {
		return nil, nil, err
	}
	return NewAt(cfg, "", cfg.Storage.Path, sc, logger, Overrides{})
}

// NewAt 针对指定的库文件构建 App，外部服务取自共享容器 sc。
// 返回的清理函数仅关闭本库的存储，不关闭共享服务。
func NewAt(cfg config.Config, id, dbPath string, sc *services.Container, logger *slog.Logger, ov Overrides) (*App, func(), error) {
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, nil, err
	}

	parser, emb, rr := sc.Snapshot()

	// 知识库可覆盖解析方式（使用全局解析服务的连接参数）。
	if ov.ParseProvider != "" && ov.ParseProvider != cfg.Parse.Provider {
		cfg.Parse.Provider = ov.ParseProvider
		var perr error
		parser, perr = services.BuildParser(cfg)
		if perr != nil {
			_ = st.Close()
			return nil, nil, perr
		}
	}

	// 知识库可单独开关重排；开启但全局关闭时，若配置了端点则临时构建。
	if ov.RerankEnabled != nil {
		if *ov.RerankEnabled {
			if rr == nil && cfg.Rerank.Endpoint.BaseURL != "" {
				rr = rerank.NewOpenAI(
					cfg.Rerank.Endpoint.BaseURL,
					cfg.Rerank.Endpoint.APIKey,
					cfg.Rerank.Endpoint.Model,
					cfg.Rerank.Endpoint.Path,
					cfg.Rerank.Endpoint.Timeout.Std(),
					cfg.Rerank.Endpoint.MaxRetries,
					cfg.Rerank.Endpoint.QPS,
				)
			}
		} else {
			rr = nil
		}
	}

	chunkOpt := chunk.Options{
		MaxRunes: cfg.Chunk.MaxRunes,
		Overlap:  cfg.Chunk.Overlap,
		MinRunes: cfg.Chunk.MinRunes,
	}
	if ov.Chunk != nil {
		chunkOpt = *ov.Chunk
	}
	if ov.TopK > 0 {
		cfg.Search.TopK = ov.TopK
	}
	if ov.CandidateK > 0 {
		cfg.Search.CandidateK = ov.CandidateK
	}

	uploadDir := filepath.Join(cfg.Storage.Dir, ".uploads")
	ing := ingest.New(st, parser, emb, chunkOpt, cfg.Embed.Endpoint.Model, rr != nil, uploadDir)
	srch := search.New(st, emb, rr, cfg.Embed.Endpoint.Model, cfg.Rerank.Endpoint.Model)

	a := &App{
		ID:        id,
		DBPath:    dbPath,
		Cfg:       cfg,
		Store:     st,
		Ingest:    ing,
		Search:    srch,
		Embedder:  emb,
		Reranker:  rr,
		Parser:    parser,
		Logger:    logger,
		StartedAt: time.Now(),
	}
	return a, func() { _ = st.Close() }, nil
}

// RerankEnabled 报告是否启用了重排。
func (a *App) RerankEnabled() bool { return a.Reranker != nil }

// EmbedDim 返回当前知识库已建立的向量维度；空库返回 0。
func (a *App) EmbedDim() int {
	return a.Store.Dim()
}
