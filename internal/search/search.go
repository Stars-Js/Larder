// Package search 负责“查询 → 向量召回 → 重排 → 返回带引用结果”。
package search

import (
	"context"
	"fmt"
	"sort"

	"larder/internal/model"
	"larder/internal/provider/embed"
	"larder/internal/provider/rerank"
	"larder/internal/store"
)

// Service 编排检索流程。
type Service struct {
	store       store.Store
	embedder    embed.Embedder
	reranker    rerank.Reranker // 可为 nil，表示不做重排
	embedModel  string
	rerankModel string
}

// New 创建检索服务。reranker 为 nil 时退化为纯向量检索。
func New(st store.Store, embedder embed.Embedder, reranker rerank.Reranker, embedModel, rerankModel string) *Service {
	return &Service{store: st, embedder: embedder, reranker: reranker, embedModel: embedModel, rerankModel: rerankModel}
}

// Options 控制检索行为。
type Options struct {
	TopK       int
	CandidateK int
}

// Result 是一次检索的完整结果。重排服务失败时不会让整个检索失败，
// 而是退回向量结果并把原因放在 RerankError 里。
type Result struct {
	Hits        []model.SearchHit
	Reranked    bool
	RerankError string
}

// Search 执行检索。返回的命中带文档标题、来源、标题路径与得分。
func (s *Service) Search(ctx context.Context, query string, opt Options) (Result, error) {
	if query == "" {
		return Result{}, fmt.Errorf("查询内容不能为空")
	}
	if opt.TopK <= 0 {
		opt.TopK = 8
	}
	if opt.CandidateK < opt.TopK {
		opt.CandidateK = opt.TopK * 4
	}

	// 1) 查询向量。
	vecs, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		s.recordUsage(ctx, store.UsageEvent{Kind: "embed", Model: s.embedModel, Status: "error"})
		return Result{}, fmt.Errorf("查询向量化失败: %w", err)
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		s.recordUsage(ctx, store.UsageEvent{Kind: "embed", Model: s.embedModel, Status: "error"})
		return Result{}, fmt.Errorf("查询向量为空")
	}
	s.recordUsage(ctx, store.UsageEvent{
		Kind: "embed", Model: s.embedModel, Chars: int64(len([]rune(query))),
		Tokens: embedTokens(s.embedder, []string{query}), Status: "ok",
	})

	// 2) 向量召回候选。
	hits, err := s.store.Search(ctx, vecs[0], opt.CandidateK)
	if err != nil {
		return Result{}, err
	}
	if len(hits) == 0 {
		return Result{Hits: []model.SearchHit{}}, nil
	}

	vectorHits := toVectorHits(hits)

	// 3) 重排（可选）。失败时退回向量结果，不让检索整体不可用。
	if s.reranker != nil && len(hits) > 1 {
		docs := make([]string, len(hits))
		for i, h := range hits {
			docs[i] = rerankText(h.Heading, h.Text)
		}
		scores, rerr := s.reranker.Rerank(ctx, query, docs)
		if rerr != nil {
			s.recordUsage(ctx, store.UsageEvent{Kind: "rerank", Model: s.rerankModel, Status: "error"})
			return Result{Hits: clamp(vectorHits, opt.TopK), Reranked: false, RerankError: rerr.Error()}, nil
		}
		if len(scores) != len(hits) {
			s.recordUsage(ctx, store.UsageEvent{Kind: "rerank", Model: s.rerankModel, Status: "error"})
			return Result{Hits: clamp(vectorHits, opt.TopK), Reranked: false, RerankError: "重排得分数量与候选数不匹配"}, nil
		}
		s.recordUsage(ctx, store.UsageEvent{
			Kind: "rerank", Model: s.rerankModel, Tokens: rerankTokens(s.reranker, query, docs), Status: "ok",
		})
		result := make([]model.SearchHit, len(hits))
		for i, h := range hits {
			result[i] = toHit(h)
			result[i].RerankScore = scores[i]
			result[i].Score = scores[i]
		}
		sort.SliceStable(result, func(i, j int) bool {
			return result[i].Score > result[j].Score
		})
		return Result{Hits: clamp(result, opt.TopK), Reranked: true}, nil
	}

	// 4) 无重排：以负距离作为得分（距离越小分越高）。
	return Result{Hits: clamp(vectorHits, opt.TopK)}, nil
}

func toVectorHits(hits []store.Hit) []model.SearchHit {
	out := make([]model.SearchHit, len(hits))
	for i, h := range hits {
		out[i] = toHit(h)
		// 向量距离越小越相近；转成 0–1 的正向得分，便于界面展示。
		out[i].Score = 1 / (1 + h.Distance)
	}
	// 已按距离升序，天然就是得分降序，无需再排。
	return out
}

func toHit(h store.Hit) model.SearchHit {
	return model.SearchHit{
		ChunkID:  h.ChunkID,
		DocID:    h.DocID,
		DocTitle: h.DocTitle,
		Source:   h.Source,
		Heading:  h.Heading,
		Text:     h.Text,
		Distance: h.Distance,
	}
}

func rerankText(heading, text string) string {
	if heading == "" {
		return text
	}
	return heading + "\n" + text
}

func (s *Service) recordUsage(ctx context.Context, ev store.UsageEvent) {
	_ = s.store.AddUsage(ctx, ev)
}

func embedTokens(e embed.Embedder, inputs []string) int64 {
	if r, ok := e.(embed.UsageReporter); ok {
		if u := r.LastUsage(); u.TotalTokens > 0 {
			return int64(u.TotalTokens)
		}
	}
	chars := 0
	for _, s := range inputs {
		chars += len([]rune(s))
	}
	n := chars / 3
	if n < 1 {
		n = 1
	}
	return int64(n)
}

func rerankTokens(r rerank.Reranker, query string, docs []string) int64 {
	if rr, ok := r.(rerank.UsageReporter); ok {
		if u := rr.LastUsage(); u.TotalTokens > 0 {
			return int64(u.TotalTokens)
		}
	}
	chars := len([]rune(query))
	for _, d := range docs {
		chars += len([]rune(d))
	}
	n := chars / 3
	if n < 1 {
		n = 1
	}
	return int64(n)
}

func clamp(hits []model.SearchHit, topK int) []model.SearchHit {
	if topK > 0 && len(hits) > topK {
		return hits[:topK]
	}
	return hits
}
