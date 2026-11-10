// Package kb 管理多个知识库：每个知识库是 data/ 目录下的一个独立 .db 文件，
// 共享同一套外部服务（解析/向量/重排）配置，但各自持有独立的文档与向量索引。
package kb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"larder/internal/app"
	"larder/internal/config"
	"larder/internal/model"
	"larder/internal/services"
	"larder/internal/store"
)

// metaNameKey 是写入每个库 meta 表的名称键。
const metaNameKey = "kb_name"

// Info 是知识库的元信息，用于列表展示。
type Info struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Documents int       `json:"documents"`
	Chunks    int       `json:"chunks"`
	Dim       int       `json:"dim"`
	CreatedAt time.Time `json:"created_at"`

	// 处理状态汇总：知识库展示时反映解析/嵌入/重排状态。
	ParseMethod  string `json:"parse_method,omitempty"`  // 解析服务（plain/mineru/docling/tika）
	EmbedModel   string `json:"embed_model,omitempty"`   // 向量模型
	EmbedDim     int    `json:"embed_dim,omitempty"`     // 向量维度
	RerankStatus string `json:"rerank_status,omitempty"` // on/off
	RerankModel  string `json:"rerank_model,omitempty"`  // 重排模型
	ParsedDocs   int    `json:"parsed_docs"`             // 解析成功的文档数
	EmbeddedDocs int    `json:"embedded_docs"`           // 嵌入成功的文档数
	PendingDocs  int    `json:"pending_docs"`            // 待处理（进行中）
	FailedDocs   int    `json:"failed_docs"`             // 解析或嵌入失败
}

// Manager 负责知识库的创建、列举、打开与删除。
type Manager struct {
	cfg    config.Config
	svc    *services.Container
	logger *slog.Logger

	mu    sync.RWMutex
	dir   string
	cache map[string]*app.App // id -> 已打开的库
}

// NewManager 创建管理器，确保数据目录存在。
func NewManager(cfg config.Config, svc *services.Container, logger *slog.Logger) (*Manager, error) {
	dir := cfg.Storage.Dir
	if dir == "" {
		dir = "data"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录 %s 失败: %w", dir, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".uploads"), 0o755); err != nil {
		return nil, fmt.Errorf("创建上传缓存目录失败: %w", err)
	}
	return &Manager{cfg: cfg, svc: svc, logger: logger, dir: dir, cache: map[string]*app.App{}}, nil
}

// Dir 返回知识库根目录。
func (m *Manager) Dir() string { return m.dir }

// dbPath 把库 ID 解析为 .db 文件路径，并做路径安全校验（防目录穿越）。
func (m *Manager) dbPath(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return "", fmt.Errorf("非法的知识库 ID: %q", id)
	}
	return filepath.Join(m.dir, id+".db"), nil
}

// sanitizeID 由名称生成安全的文件 ID。
func sanitizeID(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		default:
			// 中文等非 ASCII 直接丢弃，最终可能为空，交给调用方补随机后缀。
		}
	}
	s := strings.Trim(b.String(), "-_")
	return s
}

func randomSuffix(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// Create 新建一个知识库，返回其信息。名称为空时自动命名。
func (m *Manager) Create(ctx context.Context, name string) (Info, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "未命名知识库"
	}
	id := sanitizeID(name)
	if id == "" {
		id = "kb"
	}
	// 避免覆盖：若已存在同名文件，追加随机后缀。
	finalID := id
	for i := 0; ; i++ {
		p := filepath.Join(m.dir, finalID+".db")
		if _, err := os.Stat(p); os.IsNotExist(err) {
			break
		}
		finalID = id + "-" + randomSuffix(3)
		if i > 5 {
			return Info{}, fmt.Errorf("无法分配唯一的知识库 ID")
		}
	}

	a, err := m.openApp(finalID)
	if err != nil {
		return Info{}, err
	}
	if err := a.Store.SetMeta(ctx, metaNameKey, name); err != nil {
		return Info{}, err
	}
	return m.info(ctx, finalID, a)
}

// openApp 依据全局配置打开（或复用）某个库对应的 App。
func (m *Manager) openApp(id string) (*app.App, error) {
	m.mu.RLock()
	if a, ok := m.cache[id]; ok {
		m.mu.RUnlock()
		return a, nil
	}
	m.mu.RUnlock()

	dbPath, err := m.dbPath(id)
	if err != nil {
		return nil, err
	}

	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()

	// 每个库可以有独立的解析方式、重排开关、切块与检索参数。
	ov := app.Overrides{}
	if st, err := store.Open(dbPath); err == nil {
		if s, err := loadSettings(context.Background(), st); err == nil {
			ov = s.overrides(cfg)
		}
		_ = st.Close()
	}

	a, cleanup, err := app.NewAt(cfg, id, dbPath, m.svc, m.logger, ov)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if existing, ok := m.cache[id]; ok {
		m.mu.Unlock()
		cleanup()
		return existing, nil
	}
	m.cache[id] = a
	m.mu.Unlock()
	return a, nil
}

// Open 打开已存在的知识库（不创建）。
func (m *Manager) Open(id string) (*app.App, error) {
	p, err := m.dbPath(id)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(p); err != nil {
		return nil, fmt.Errorf("知识库不存在: %s", id)
	}
	return m.openApp(id)
}

// Get 返回知识库的 App；库不存在时报错。
func (m *Manager) Get(id string) (*app.App, error) { return m.Open(id) }

// Delete 关闭并删除知识库。
func (m *Manager) Delete(ctx context.Context, id string) error {
	p, err := m.dbPath(id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if a, ok := m.cache[id]; ok {
		_ = a.Store.Close()
		delete(m.cache, id)
	}
	m.mu.Unlock()
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除知识库文件失败: %w", err)
	}
	return nil
}

// List 列出全部知识库。为避免同时打开过多库，这里仅对已缓存的库读取计数，
// 未缓存的库临时打开读取元信息后关闭。
func (m *Manager) List(ctx context.Context) ([]Info, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, err
	}
	infos := make([]Info, 0)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".db")
		info, err := m.infoLazy(ctx, id)
		if err != nil {
			m.logger.Warn("读取知识库信息失败", "id", id, "err", err)
			continue
		}
		infos = append(infos, info)
	}
	sort.SliceStable(infos, func(i, j int) bool { return infos[i].CreatedAt.After(infos[j].CreatedAt) })
	return infos, nil
}

// infoLazy 尽量不长期打开库：已缓存的直接读，否则临时打开读后关闭。
func (m *Manager) infoLazy(ctx context.Context, id string) (Info, error) {
	m.mu.RLock()
	a, cached := m.cache[id]
	m.mu.RUnlock()
	if cached {
		return m.info(ctx, id, a)
	}

	p, err := m.dbPath(id)
	if err != nil {
		return Info{}, err
	}
	st, err := store.Open(p)
	if err != nil {
		return Info{}, err
	}
	defer st.Close()
	return m.infoFromStore(ctx, id, p, st)
}

func (m *Manager) info(ctx context.Context, id string, a *app.App) (Info, error) {
	p, _ := m.dbPath(id)
	return m.infoFromStore(ctx, id, p, a.Store)
}

func (m *Manager) infoFromStore(ctx context.Context, id, path string, st store.Store) (Info, error) {
	docs, err := st.ListDocuments(ctx)
	if err != nil {
		return Info{}, err
	}
	chunks, err := st.CountChunks(ctx, "")
	if err != nil {
		return Info{}, err
	}
	name, ok, err := st.GetMeta(ctx, metaNameKey)
	if err != nil {
		return Info{}, err
	}
	if !ok || name == "" {
		name = id
	}
	var created time.Time
	if len(docs) > 0 {
		// 以最早文档近似创建时间；无文档时用文件修改时间。
		created = docs[len(docs)-1].CreatedAt
	}
	if created.IsZero() {
		if fi, err := os.Stat(path); err == nil {
			created = fi.ModTime()
		}
	}

	// 汇总各文档处理状态。
	parsed, embedded, pending, failed := 0, 0, 0, 0
	for _, d := range docs {
		if d.ParseStatus == "ok" {
			parsed++
		}
		if d.EmbedStatus == "ok" {
			embedded++
		}
		if d.ParseStatus == "error" || d.EmbedStatus == "error" {
			failed++
		} else if d.ParseStatus != "ok" || d.EmbedStatus != "ok" {
			pending++
		}
	}

	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()
	s, _ := loadSettings(ctx, st)
	eff := s.effective(cfg)
	rerankStatus := "off"
	if eff.RerankEnabled != nil && *eff.RerankEnabled {
		rerankStatus = "on"
	}

	return Info{
		ID: id, Name: name, Path: path,
		Documents: len(docs), Chunks: chunks, Dim: st.Dim(), CreatedAt: created,
		ParseMethod:  eff.ParseProvider,
		EmbedModel:   cfg.Embed.Endpoint.Model,
		EmbedDim:     st.Dim(),
		RerankStatus: rerankStatus,
		RerankModel:  cfg.Rerank.Endpoint.Model,
		ParsedDocs:   parsed, EmbeddedDocs: embedded, PendingDocs: pending, FailedDocs: failed,
	}, nil
}

// Rename 修改知识库显示名称。
func (m *Manager) Rename(ctx context.Context, id, name string) error {
	a, err := m.Open(id)
	if err != nil {
		return err
	}
	return a.Store.SetMeta(ctx, metaNameKey, strings.TrimSpace(name))
}

// Close 关闭所有已打开的库。
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, a := range m.cache {
		_ = a.Store.Close()
		delete(m.cache, id)
	}
	return nil
}

// Config 返回当前热更新后的配置副本。
func (m *Manager) Config() config.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// ApplyConfig 在设置变更后更新全局配置并清空已打开的库，
// 使下次访问时用新的服务实例与配置重建（保证热更新对所有库生效）。
func (m *Manager) ApplyConfig(cfg config.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
	for id, a := range m.cache {
		_ = a.Store.Close()
		delete(m.cache, id)
	}
}

// 让 model 包在本文件被引用（Info 未来可能携带文档样例）。
var _ = model.Document{}
