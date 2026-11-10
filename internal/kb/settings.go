package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"larder/internal/app"
	"larder/internal/chunk"
	"larder/internal/config"
	"larder/internal/store"
)

// metaSettingsKey 是每个知识库自己的处理参数（JSON）在 meta 表中的键。
const metaSettingsKey = "kb_settings"

// Settings 是知识库级设置。未填写的字段跟随全局设置。
type Settings struct {
	Name          string `json:"name"`
	ParseProvider string `json:"parse_provider"` // "" 或 "global" 表示跟随全局；plain/mineru/docling/tika
	RerankEnabled *bool  `json:"rerank_enabled"` // nil 表示跟随全局

	ChunkMaxRunes int `json:"chunk_max_runes"`
	ChunkOverlap  int `json:"chunk_overlap"`
	ChunkMinRunes int `json:"chunk_min_runes"`

	TopK       int `json:"top_k"`
	CandidateK int `json:"candidate_k"`
}

// effective 用全局配置补全未设置的字段，供前端展示。
func (s Settings) effective(cfg config.Config) Settings {
	out := s
	if out.ParseProvider == "" || out.ParseProvider == "global" {
		out.ParseProvider = cfg.Parse.Provider
	}
	// 三项切块参数都未设置时才整体跟随全局；否则 0 是合法值（例如不重叠）。
	if out.ChunkMaxRunes <= 0 && out.ChunkOverlap <= 0 && out.ChunkMinRunes <= 0 {
		out.ChunkMaxRunes = cfg.Chunk.MaxRunes
		out.ChunkOverlap = cfg.Chunk.Overlap
		out.ChunkMinRunes = cfg.Chunk.MinRunes
	} else if out.ChunkMaxRunes <= 0 {
		out.ChunkMaxRunes = cfg.Chunk.MaxRunes
	}
	if out.TopK <= 0 {
		out.TopK = cfg.Search.TopK
	}
	if out.CandidateK <= 0 {
		out.CandidateK = cfg.Search.CandidateK
	}
	if out.RerankEnabled == nil {
		v := cfg.Rerank.Enabled
		out.RerankEnabled = &v
	}
	return out
}

// overrides 把知识库设置转换成 app 层可用的覆盖项。
func (s Settings) overrides(cfg config.Config) app.Overrides {
	ov := app.Overrides{}
	if p := strings.TrimSpace(s.ParseProvider); p != "" && p != "global" {
		ov.ParseProvider = p
	}
	ov.RerankEnabled = s.RerankEnabled
	maxRunes := s.ChunkMaxRunes
	if maxRunes <= 0 || maxRunes == cfg.Chunk.MaxRunes {
		maxRunes = cfg.Chunk.MaxRunes
	}
	overlap := s.ChunkOverlap
	if overlap < 0 || overlap >= maxRunes {
		overlap = cfg.Chunk.Overlap
	}
	minRunes := s.ChunkMinRunes
	if minRunes < 0 {
		minRunes = cfg.Chunk.MinRunes
	}
	if s.ChunkMaxRunes > 0 || s.ChunkOverlap > 0 || s.ChunkMinRunes > 0 {
		ov.Chunk = &chunk.Options{MaxRunes: maxRunes, Overlap: overlap, MinRunes: minRunes}
	}
	ov.TopK = s.TopK
	ov.CandidateK = s.CandidateK
	return ov
}

// validate 检查取值范围。
func (s Settings) validate() error {
	switch s.ParseProvider {
	case "", "global", "plain", "mineru", "docling", "tika":
	default:
		return fmt.Errorf("不支持的解析方式 %q", s.ParseProvider)
	}
	if s.ChunkMaxRunes != 0 && (s.ChunkMaxRunes < 100 || s.ChunkMaxRunes > 8000) {
		return fmt.Errorf("切块大小应在 100–8000 字符之间")
	}
	if s.ChunkOverlap < 0 || (s.ChunkMaxRunes > 0 && s.ChunkOverlap >= s.ChunkMaxRunes) {
		return fmt.Errorf("切块重叠必须小于切块大小")
	}
	if s.ChunkMinRunes < 0 || s.ChunkMinRunes > 500 {
		return fmt.Errorf("最小切块应在 0–500 字符之间")
	}
	if s.TopK < 0 || s.TopK > 50 {
		return fmt.Errorf("检索条数应在 0–50 之间")
	}
	if s.CandidateK < 0 || s.CandidateK > 200 {
		return fmt.Errorf("候选条数应在 0–200 之间")
	}
	if s.CandidateK > 0 && s.TopK > 0 && s.CandidateK < s.TopK {
		return fmt.Errorf("候选条数不能小于检索条数")
	}
	return nil
}

// loadSettings 从库的 meta 表读取知识库设置；不存在时返回零值。
func loadSettings(ctx context.Context, st store.Store) (Settings, error) {
	raw, ok, err := st.GetMeta(ctx, metaSettingsKey)
	if err != nil {
		return Settings{}, err
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return Settings{}, nil
	}
	var s Settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return Settings{}, fmt.Errorf("知识库设置格式错误: %w", err)
	}
	return s, nil
}

func saveSettings(ctx context.Context, st store.Store, s Settings) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return st.SetMeta(ctx, metaSettingsKey, string(b))
}

// GetSettings 返回知识库设置的生效值（未设置的字段用全局值补全）。
func (m *Manager) GetSettings(ctx context.Context, id string) (Settings, error) {
	a, err := m.Open(id)
	if err != nil {
		return Settings{}, err
	}
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()
	s, err := loadSettings(ctx, a.Store)
	if err != nil {
		return Settings{}, err
	}
	name, ok, err := a.Store.GetMeta(ctx, metaNameKey)
	if err != nil {
		return Settings{}, err
	}
	if !ok || name == "" {
		name = id
	}
	s.Name = name
	return s.effective(cfg), nil
}

// GetSettingsRaw 返回知识库中实际保存的设置（未补全全局值），
// 供前端区分“跟随全局”和“显式覆盖”。
func (m *Manager) GetSettingsRaw(ctx context.Context, id string) (Settings, error) {
	a, err := m.Open(id)
	if err != nil {
		return Settings{}, err
	}
	s, err := loadSettings(ctx, a.Store)
	if err != nil {
		return Settings{}, err
	}
	name, ok, err := a.Store.GetMeta(ctx, metaNameKey)
	if err != nil {
		return Settings{}, err
	}
	if !ok || name == "" {
		name = id
	}
	s.Name = name
	return s, nil
}

// UpdateSettings 保存知识库设置，并重建该库的 App 让新参数生效。
func (m *Manager) UpdateSettings(ctx context.Context, id string, s Settings) (Settings, error) {
	if err := s.validate(); err != nil {
		return Settings{}, err
	}
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()
	if s.RerankEnabled != nil && *s.RerankEnabled && strings.TrimSpace(cfg.Rerank.Endpoint.BaseURL) == "" {
		return Settings{}, fmt.Errorf("本知识库启用了重排，但全局尚未配置重排服务的 Base URL；请先到「设置 → 重排服务」填写")
	}
	if p := strings.TrimSpace(s.ParseProvider); p != "" && p != "global" && p != "plain" &&
		strings.TrimSpace(cfg.Parse.Endpoint.BaseURL) == "" {
		return Settings{}, fmt.Errorf("本知识库选择了 %s 解析，但全局尚未配置解析服务的 Base URL；请先到「设置 → 解析服务」填写", p)
	}
	a, err := m.Open(id)
	if err != nil {
		return Settings{}, err
	}
	if err := saveSettings(ctx, a.Store, s); err != nil {
		return Settings{}, err
	}
	if name := strings.TrimSpace(s.Name); name != "" {
		if err := a.Store.SetMeta(ctx, metaNameKey, name); err != nil {
			return Settings{}, err
		}
	}
	m.Evict(id)
	return m.GetSettings(ctx, id)
}

// Evict 关闭并移除已缓存的库实例，下次访问时按最新设置重建。
func (m *Manager) Evict(id string) {
	m.mu.Lock()
	if a, ok := m.cache[id]; ok {
		_ = a.Store.Close()
		delete(m.cache, id)
	}
	m.mu.Unlock()
}
