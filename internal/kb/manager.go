// Package kb 管理多个知识库：每个知识库使用 data/<知识库名>/ 独立目录，
// 保存 SQLite、PDF/原始文件、解析图片与导出文件，互不干扰。
package kb

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"larder/internal/app"
	"larder/internal/config"
	"larder/internal/model"
	"larder/internal/services"
	"larder/internal/store"
)

// 每个库 meta 表中的身份键。
const (
	metaNameKey = "kb_name"
	metaIDKey   = "kb_id"
	metaDirKey  = "kb_dir"
)

// Info 是知识库的元信息，用于列表展示。
type Info struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	DataDir   string    `json:"data_dir,omitempty"`
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

	// Zotero 文献库信息。
	Kind             string `json:"kind,omitempty"`              // manual / zotero
	ZoteroCollection string `json:"zotero_collection,omitempty"` // 集合名称
	ZoteroDataDir    string `json:"zotero_data_dir,omitempty"`   // Zotero 数据目录
	LastSync         string `json:"last_sync,omitempty"`         // 最近同步时间
}

// Manager 负责知识库的创建、列举、打开与删除。
type Manager struct {
	cfg    config.Config
	svc    *services.Container
	logger *slog.Logger

	mu    sync.RWMutex
	dir   string
	paths map[string]string   // id -> .db 文件
	dirs  map[string]string   // id -> 知识库数据目录
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
	m := &Manager{cfg: cfg, svc: svc, logger: logger, dir: dir,
		paths: map[string]string{}, dirs: map[string]string{}, cache: map[string]*app.App{}}
	if err := m.discover(); err != nil {
		return nil, err
	}
	return m, nil
}

// Dir 返回知识库根目录。
func (m *Manager) Dir() string { return m.dir }

// dbPath 把库 ID 解析为数据目录中的 .db 文件路径。
func (m *Manager) dbPath(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return "", fmt.Errorf("非法的知识库 ID: %q", id)
	}
	m.mu.RLock()
	p, ok := m.paths[id]
	m.mu.RUnlock()
	if ok {
		return p, nil
	}
	// 兼容尚未迁移的旧布局：data/<id>.db。
	legacy := filepath.Join(m.dir, id+".db")
	if _, err := os.Stat(legacy); err == nil {
		return legacy, nil
	}
	return "", fmt.Errorf("知识库不存在: %s", id)
}

// DataDir 返回知识库的数据目录（data/<知识库名>/）。
func (m *Manager) DataDir(id string) string {
	m.mu.RLock()
	d, ok := m.dirs[id]
	m.mu.RUnlock()
	if ok {
		return d
	}
	return filepath.Join(m.dir, id)
}

// discover 扫描 data/ 下的知识库目录，并把旧版 data/<id>.db 迁移到
// data/<知识库名>/<id>.db；同时迁移旧的全局 .assets/<docID> 图片目录。
func (m *Manager) discover() error {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return err
	}
	// 1) 已迁移的 data/<知识库名>/*.db。
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		folder := filepath.Join(m.dir, e.Name())
		dbs, _ := filepath.Glob(filepath.Join(folder, "*.db"))
		if len(dbs) == 0 {
			continue
		}
		p := dbs[0]
		id, name, _, err := readKBIdentity(p, strings.TrimSuffix(filepath.Base(p), ".db"), e.Name())
		if err != nil {
			m.logger.Warn("读取知识库失败", "path", p, "err", err)
			continue
		}
		if id == "" {
			id = strings.TrimSuffix(filepath.Base(p), ".db")
		}
		m.paths[id] = p
		m.dirs[id] = folder
		_ = os.MkdirAll(filepath.Join(folder, "pdf"), 0o755)
		_ = os.MkdirAll(filepath.Join(folder, "files"), 0o755)
		_ = os.MkdirAll(filepath.Join(folder, "exports"), 0o755)
		removeEmptyDir(filepath.Join(folder, "image"))
		_ = name
	}

	// 2) 旧版根目录 data/<id>.db：迁移到 data/<知识库名>/<id>.db。
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".db") {
			continue
		}
		legacyID := strings.TrimSuffix(e.Name(), ".db")
		if _, ok := m.paths[legacyID]; ok {
			continue
		}
		legacyPath := filepath.Join(m.dir, e.Name())
		if err := m.migrateLegacyKB(legacyID, legacyPath); err != nil {
			m.logger.Warn("迁移旧知识库目录失败，继续使用原路径", "kb", legacyID, "err", err)
			m.paths[legacyID] = legacyPath
			m.dirs[legacyID] = m.dir
		}
	}
	m.cleanupLegacyUploads()
	_ = os.Remove(filepath.Join(m.dir, ".assets")) // 仅在为空时成功
	return nil
}

// cleanupLegacyUploads 删除已不再被任何文档引用的旧 .uploads 缓存。
func (m *Manager) cleanupLegacyUploads() {
	dir := filepath.Join(m.dir, ".uploads")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	refs := map[string]bool{}
	for id, db := range m.paths {
		st, err := store.Open(db)
		if err != nil {
			continue
		}
		if list, lerr := st.ListDocuments(context.Background()); lerr == nil {
			for _, doc := range list {
				if doc.OriginPath != "" {
					refs[filepath.Clean(doc.OriginPath)] = true
					refs[filepath.Base(doc.OriginPath)] = true
				}
			}
		}
		_ = st.Close()
		_ = id
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if !refs[filepath.Clean(p)] && !refs[filepath.Base(p)] {
			_ = os.Remove(p)
		}
	}
	_ = os.Remove(dir)
}

// readKBIdentity 读取/补齐 kb_id、kb_name、kb_dir。
func readKBIdentity(path, fallbackID, folderName string) (id, name, dir string, err error) {
	st, err := store.Open(path)
	if err != nil {
		return "", "", "", err
	}
	defer st.Close()
	ctx := context.Background()
	id, _, _ = st.GetMeta(ctx, metaIDKey)
	name, _, _ = st.GetMeta(ctx, metaNameKey)
	dir, _, _ = st.GetMeta(ctx, metaDirKey)
	if strings.TrimSpace(id) == "" {
		id = fallbackID
		_ = st.SetMeta(ctx, metaIDKey, id)
	}
	if strings.TrimSpace(name) == "" {
		name = id
		_ = st.SetMeta(ctx, metaNameKey, name)
	}
	if strings.TrimSpace(dir) == "" {
		dir = folderName
		_ = st.SetMeta(ctx, metaDirKey, dir)
	}
	return id, name, dir, nil
}

// migrateLegacyKB 把 data/<id>.db 及旧的 .assets 图片迁移到 data/<知识库名>/。
func (m *Manager) migrateLegacyKB(id, legacyPath string) error {
	name := id
	docs := []string{}
	st, err := store.Open(legacyPath)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if n, ok, _ := st.GetMeta(ctx, metaNameKey); ok && strings.TrimSpace(n) != "" {
		name = n
	}
	if list, lerr := st.ListDocuments(ctx); lerr == nil {
		for _, d := range list {
			docs = append(docs, d.ID)
		}
	}
	dirName := safeDirName(name)
	if dirName == "" {
		dirName = id
	}
	targetDir := filepath.Join(m.dir, dirName)
	if _, statErr := os.Stat(targetDir); statErr == nil {
		targetDir = filepath.Join(m.dir, dirName+" ["+id+"]")
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		_ = st.Close()
		return err
	}
	_ = st.SetMeta(ctx, metaIDKey, id)
	_ = st.SetMeta(ctx, metaNameKey, name)
	_ = st.SetMeta(ctx, metaDirKey, filepath.Base(targetDir))
	if err := st.Close(); err != nil {
		return err
	}
	targetDB := filepath.Join(targetDir, id+".db")
	if err := moveFile(legacyPath, targetDB); err != nil {
		return err
	}
	_ = moveFile(legacyPath+"-wal", targetDB+"-wal")
	_ = moveFile(legacyPath+"-shm", targetDB+"-shm")

	for _, docID := range docs {
		src := filepath.Join(m.dir, ".assets", docID)
		dst := filepath.Join(targetDir, "image", docID)
		if _, err := os.Stat(src); err == nil {
			if _, statErr := os.Stat(dst); os.IsNotExist(statErr) {
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err == nil {
					_ = os.Rename(src, dst)
				}
			}
		}
	}
	// 旧版上传缓存 .uploads/<hash>.<ext> 复制到本知识库的 pdf/ 或 files/，
	// 并更新 documents.origin_path，保证“打开原文件/重新处理”继续可用。
	if st2, err := store.Open(targetDB); err == nil {
		if list, lerr := st2.ListDocuments(ctx); lerr == nil {
			for _, doc := range list {
				if filepath.Base(filepath.Dir(doc.OriginPath)) != ".uploads" {
					continue
				}
				if _, serr := os.Stat(doc.OriginPath); serr != nil {
					continue
				}
				sub := "files"
				if strings.EqualFold(filepath.Ext(doc.OriginPath), ".pdf") {
					sub = "pdf"
				}
				name := safeDirName(filepath.Base(doc.Source))
				if name == "" || name == "." {
					name = filepath.Base(doc.OriginPath)
				}
				dst := filepath.Join(targetDir, sub, name)
				if _, serr := os.Stat(dst); serr == nil {
					if same, _ := sameFileContent(doc.OriginPath, dst); !same {
						ext := filepath.Ext(name)
						suffix := doc.ID
						if len(suffix) > 8 {
							suffix = suffix[:8]
						}
						dst = filepath.Join(targetDir, sub, strings.TrimSuffix(name, ext)+"-"+suffix+ext)
					}
				}
				if copyErr := copyFile(doc.OriginPath, dst); copyErr == nil {
					_ = st2.SetDocumentOrigin(ctx, doc.ID, dst, "upload")
				}
			}
		}
		_ = st2.Close()
	}
	for _, sub := range []string{"pdf", "files", "exports"} {
		_ = os.MkdirAll(filepath.Join(targetDir, sub), 0o755)
	}
	removeEmptyDir(filepath.Join(targetDir, "image"))
	m.paths[id] = targetDB
	m.dirs[id] = targetDir
	return nil
}

func moveFile(src, dst string) error {
	if src == dst {
		return nil
	}
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// 目标存在或跨卷时复制后删除。
	if _, err := os.Stat(dst); err == nil {
		_ = os.Remove(src)
		return nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return err
	}
	return os.Remove(src)
}

// safeDirName 把知识库名称转换成 Windows 可用的目录名，保留中文。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func sameFileContent(a, b string) (bool, error) {
	ab, err := os.ReadFile(a)
	if err != nil {
		return false, err
	}
	bb, err := os.ReadFile(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(ab, bb), nil
}

func removeEmptyDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}
}

func safeDirName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20:
			continue
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.Trim(b.String(), " .")
	if s == "" {
		return ""
	}
	switch strings.ToUpper(s) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		s = "_" + s
	}
	if utf8.RuneCountInString(s) > 80 {
		s = string([]rune(s)[:80])
	}
	return s
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

// Create 新建一个知识库，数据目录为 data/<知识库名>/。
func (m *Manager) Create(ctx context.Context, name string) (Info, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "未命名知识库"
	}
	baseID := sanitizeID(name)
	if baseID == "" {
		baseID = "kb"
	}
	finalID := baseID
	m.mu.RLock()
	_, exists := m.paths[finalID]
	m.mu.RUnlock()
	for i := 0; exists; i++ {
		finalID = baseID + "-" + randomSuffix(3)
		m.mu.RLock()
		_, exists = m.paths[finalID]
		m.mu.RUnlock()
		if i > 5 {
			return Info{}, fmt.Errorf("无法分配唯一的知识库 ID")
		}
	}

	dirName := safeDirName(name)
	if dirName == "" {
		dirName = finalID
	}
	targetDir := filepath.Join(m.dir, dirName)
	for i := 0; ; i++ {
		if _, err := os.Stat(targetDir); os.IsNotExist(err) {
			break
		}
		targetDir = filepath.Join(m.dir, fmt.Sprintf("%s [%s]", dirName, finalID))
		if i > 5 {
			return Info{}, fmt.Errorf("无法分配唯一的知识库目录")
		}
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return Info{}, err
	}
	for _, sub := range []string{"pdf", "files", "exports"} {
		_ = os.MkdirAll(filepath.Join(targetDir, sub), 0o755)
	}
	dbPath := filepath.Join(targetDir, finalID+".db")
	m.mu.Lock()
	m.paths[finalID] = dbPath
	m.dirs[finalID] = targetDir
	m.mu.Unlock()

	a, err := m.openApp(finalID)
	if err != nil {
		m.mu.Lock()
		delete(m.paths, finalID)
		delete(m.dirs, finalID)
		m.mu.Unlock()
		_ = os.RemoveAll(targetDir)
		return Info{}, err
	}
	if err := a.Store.SetMeta(ctx, metaNameKey, name); err != nil {
		return Info{}, err
	}
	_ = a.Store.SetMeta(ctx, metaIDKey, finalID)
	_ = a.Store.SetMeta(ctx, metaDirKey, filepath.Base(targetDir))
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

// Delete 关闭并删除知识库及其数据目录。
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
	dataDir := m.dirs[id]
	delete(m.paths, id)
	delete(m.dirs, id)
	m.mu.Unlock()
	_ = os.Remove(p + "-wal")
	_ = os.Remove(p + "-shm")
	if dataDir != "" && filepath.Clean(dataDir) != filepath.Clean(m.dir) {
		if err := os.RemoveAll(dataDir); err != nil {
			return fmt.Errorf("删除知识库目录失败: %w", err)
		}
		return nil
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除知识库文件失败: %w", err)
	}
	return nil
}

// List 列出全部知识库。为避免同时打开过多库，这里仅对已缓存的库读取计数，
// 未缓存的库临时打开读取元信息后关闭。
func (m *Manager) List(ctx context.Context) ([]Info, error) {
	m.mu.RLock()
	ids := make([]string, 0, len(m.paths))
	for id := range m.paths {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	infos := make([]Info, 0, len(ids))
	for _, id := range ids {
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
	stats, err := st.Stats(ctx)
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
	created := stats.CreatedAt
	if created.IsZero() {
		if fi, err := os.Stat(path); err == nil {
			created = fi.ModTime()
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
	kind, _, _ := st.GetMeta(ctx, "kb_kind")
	zoteroCollection, _, _ := st.GetMeta(ctx, "zotero_collection_name")
	zoteroDataDir, _, _ := st.GetMeta(ctx, "zotero_data_dir")
	lastSync, _, _ := st.GetMeta(ctx, "zotero_last_sync")

	return Info{
		ID: id, Name: name, Path: path, DataDir: m.DataDir(id),
		Documents: stats.Total, Chunks: chunks, Dim: st.Dim(), CreatedAt: created,
		ParseMethod:  eff.ParseProvider,
		EmbedModel:   cfg.Embed.Endpoint.Model,
		EmbedDim:     st.Dim(),
		RerankStatus: rerankStatus,
		RerankModel:  cfg.Rerank.Endpoint.Model,
		ParsedDocs:   stats.Parsed, EmbeddedDocs: stats.Embedded, PendingDocs: stats.Pending, FailedDocs: stats.Failed,
		Kind: kind, ZoteroCollection: zoteroCollection, ZoteroDataDir: zoteroDataDir, LastSync: lastSync,
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
