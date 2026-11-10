package kb

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"larder/internal/store"
)

// UsageKB 是单个知识库的用量。
type UsageKB struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ParseCount   int64  `json:"parse_count"`
	EmbedTokens  int64  `json:"embed_tokens"`
	RerankTokens int64  `json:"rerank_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
}

// UsageSummary 是跨知识库的用量汇总。
type UsageSummary struct {
	store.UsageSummary
	Since string    `json:"since"`
	ByKB  []UsageKB `json:"by_kb"`
}

// Usage 汇总 since 之后所有知识库的解析 / 嵌入 / 重排用量。
func (m *Manager) Usage(ctx context.Context, since time.Time) (UsageSummary, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return UsageSummary{}, err
	}
	out := UsageSummary{
		Since: since.Format(time.RFC3339),
		ByKB:  make([]UsageKB, 0),
	}
	days := map[string]*store.UsageDay{}
	models := map[string]*store.UsageModel{}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".db")
		p := filepath.Join(m.dir, e.Name())
		st, err := store.Open(p)
		if err != nil {
			continue
		}
		sum, err := st.UsageSummary(ctx, since)
		name, ok, _ := st.GetMeta(ctx, metaNameKey)
		_ = st.Close()
		if err != nil {
			continue
		}
		if !ok || name == "" {
			name = id
		}

		out.ParseCount += sum.ParseCount
		out.ParseFailed += sum.ParseFailed
		out.ParseChars += sum.ParseChars
		out.EmbedCount += sum.EmbedCount
		out.EmbedTokens += sum.EmbedTokens
		out.RerankCount += sum.RerankCount
		out.RerankTokens += sum.RerankTokens

		kbRow := UsageKB{
			ID: id, Name: name,
			ParseCount: sum.ParseCount, EmbedTokens: sum.EmbedTokens,
			RerankTokens: sum.RerankTokens, TotalTokens: sum.EmbedTokens + sum.RerankTokens,
		}
		if kbRow.TotalTokens > 0 || kbRow.ParseCount > 0 {
			out.ByKB = append(out.ByKB, kbRow)
		}

		for _, d := range sum.ByDay {
			tgt := days[d.Day]
			if tgt == nil {
				tgt = &store.UsageDay{Day: d.Day}
				days[d.Day] = tgt
			}
			tgt.Parse += d.Parse
			tgt.EmbedTokens += d.EmbedTokens
			tgt.RerankTokens += d.RerankTokens
			tgt.TotalTokens = tgt.EmbedTokens + tgt.RerankTokens
		}
		for _, mo := range sum.ByModel {
			key := mo.Kind + "::" + mo.Model
			tgt := models[key]
			if tgt == nil {
				tgt = &store.UsageModel{Kind: mo.Kind, Model: mo.Model}
				models[key] = tgt
			}
			tgt.Count += mo.Count
			tgt.Tokens += mo.Tokens
		}
	}

	out.TotalTokens = out.EmbedTokens + out.RerankTokens
	out.ByDay = make([]store.UsageDay, 0, len(days))
	for _, d := range days {
		out.ByDay = append(out.ByDay, *d)
	}
	sort.Slice(out.ByDay, func(i, j int) bool { return out.ByDay[i].Day < out.ByDay[j].Day })
	out.ByModel = make([]store.UsageModel, 0, len(models))
	for _, mo := range models {
		out.ByModel = append(out.ByModel, *mo)
	}
	sort.Slice(out.ByModel, func(i, j int) bool {
		if out.ByModel[i].Tokens != out.ByModel[j].Tokens {
			return out.ByModel[i].Tokens > out.ByModel[j].Tokens
		}
		return out.ByModel[i].Count > out.ByModel[j].Count
	})
	sort.Slice(out.ByKB, func(i, j int) bool {
		if out.ByKB[i].TotalTokens != out.ByKB[j].TotalTokens {
			return out.ByKB[i].TotalTokens > out.ByKB[j].TotalTokens
		}
		return out.ByKB[i].ParseCount > out.ByKB[j].ParseCount
	})
	return out, nil
}
