// Package library 负责把 Zotero 集合同步成 Larder 文献库：
// 读取条目元数据与附件、解析/嵌入、保存引用信息，并按需导出 Markdown。
package library

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"larder/internal/app"
	"larder/internal/cite"
	"larder/internal/ingest"
	"larder/internal/kb"
	"larder/internal/model"
	"larder/internal/zotero"
)

// 知识库 meta 键。
const (
	MetaKind                 = "kb_kind"
	MetaZoteroDataDir        = "zotero_data_dir"
	MetaZoteroAPIBase        = "zotero_api_base"
	MetaZoteroLibraryID      = "zotero_library_id"
	MetaZoteroCollection     = "zotero_collection_key"
	MetaZoteroCollectionName = "zotero_collection_name"
	MetaZoteroIncludeSub     = "zotero_include_subcollections"
	MetaZoteroMDMode         = "zotero_md_mode"
	MetaZoteroParseMode      = "zotero_parse_mode"
	MetaZoteroCiteStyle      = "zotero_cite_style"
	MetaZoteroLastSync       = "zotero_last_sync"
	MetaZoteroSynced         = "zotero_synced"
	MetaZoteroFailed         = "zotero_failed"
)

// CreateRequest 是从 Zotero 集合创建文献库的请求。
type CreateRequest struct {
	Name                  string `json:"name"`
	DataDir               string `json:"data_dir"`
	APIBase               string `json:"api_base"`
	LibraryID             string `json:"library_id"`
	CollectionKey         string `json:"collection_key"`
	CollectionName        string `json:"collection_name"`
	IncludeSubcollections bool   `json:"include_subcollections"`
	MDMode                string `json:"md_mode"`
	ParseMode             string `json:"parse_mode"`
	CiteStyle             string `json:"cite_style"`
	SyncNow               bool   `json:"sync_now"`
}

// SyncOptions 控制一次同步。
type SyncOptions struct {
	Force bool `json:"force"` // 忽略 dateModified，重新解析
}

// Job 是一次同步的实时状态。
type Job struct {
	KBID       string    `json:"kb_id"`
	State      string    `json:"state"` // running / ok / error / canceled
	Total      int       `json:"total"`
	Done       int       `json:"done"`
	Failed     int       `json:"failed"`
	Skipped    int       `json:"skipped"`
	Current    string    `json:"current,omitempty"`
	Message    string    `json:"message,omitempty"`
	Errors     []string  `json:"errors,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	cancel     context.CancelFunc
}

// Service 管理 Zotero 文献库的创建与同步。
type Service struct {
	mgr    *kb.Manager
	logger *slog.Logger

	mu   sync.Mutex
	jobs map[string]*Job
}

// New 创建文献库服务。
func New(mgr *kb.Manager, logger *slog.Logger) *Service {
	return &Service{mgr: mgr, logger: logger, jobs: map[string]*Job{}}
}

// Watch 监听 Zotero 数据库/存储目录变化：启动时先增量同步一次，
// 之后只要有新文献或附件写入，就自动触发对应文献库同步。
func (s *Service) Watch(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	targets := make(map[string][]string) // data dir -> kb ids
	lastSig := make(map[string]string)
	refresh := func() {
		infos, err := s.mgr.List(ctx)
		if err != nil {
			return
		}
		cfgDir := s.mgr.Config().Zotero.DataDir
		next := make(map[string][]string)
		for _, info := range infos {
			if info.Kind != "zotero" {
				continue
			}
			dir := strings.TrimSpace(info.ZoteroDataDir)
			if dir == "" {
				dir = cfgDir
			}
			if dir == "" {
				continue
			}
			next[dir] = append(next[dir], info.ID)
		}
		targets = next
	}
	syncIDs := func(ids []string) {
		for _, id := range ids {
			if _, err := s.StartSync(context.Background(), id, SyncOptions{}); err != nil {
				s.logger.Warn("Zotero 自动同步启动失败", "kb", id, "err", err)
			}
		}
	}

	refresh()
	watching := s.mgr.Config().Zotero.Watch
	syncAll := func() {
		seen := map[string]bool{}
		for _, ids := range targets {
			for _, id := range ids {
				if !seen[id] {
					seen[id] = true
					syncIDs([]string{id})
				}
			}
		}
	}
	// 启动时补一次，覆盖 Larder 关闭期间新增的文献。
	if watching {
		syncAll()
	}
	for dir := range targets {
		lastSig[dir] = zoteroDataSignature(dir)
	}

	// 目标列表每分钟刷新一次，避免频繁打开所有知识库。
	lastRefresh := time.Now()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nowWatching := s.mgr.Config().Zotero.Watch
			if time.Since(lastRefresh) > time.Minute {
				refresh()
				lastRefresh = time.Now()
			}
			if nowWatching && !watching {
				// 用户在界面重新开启监听：补一次同步。
				syncAll()
				for dir := range targets {
					lastSig[dir] = zoteroDataSignature(dir)
				}
			}
			watching = nowWatching
			if !nowWatching {
				continue
			}
			for dir, ids := range targets {
				sig := zoteroDataSignature(dir)
				old, ok := lastSig[dir]
				if !ok || old == "" {
					lastSig[dir] = sig
					continue
				}
				if sig != old {
					lastSig[dir] = sig
					s.logger.Info("检测到 Zotero 变更，自动同步", "data_dir", dir, "libraries", len(ids))
					syncIDs(ids)
				}
			}
		}
	}
}

// zoteroDataSignature 用 sqlite/wal/storage 的 mtime+size 判断 Zotero 是否有写入。
func zoteroDataSignature(dataDir string) string {
	var b strings.Builder
	for _, name := range []string{"zotero.sqlite", "zotero.sqlite-wal", "storage"} {
		fi, err := os.Stat(filepath.Join(dataDir, name))
		if err != nil {
			b.WriteString(name)
			b.WriteString(":-;")
			continue
		}
		fmt.Fprintf(&b, "%s:%d:%d;", name, fi.ModTime().UnixNano(), fi.Size())
	}
	return b.String()
}

// Collections 读取指定 Zotero 数据目录下的集合树。
func (s *Service) Collections(ctx context.Context, dataDir, apiBase, libraryID string) ([]*zotero.Collection, error) {
	cfg := s.mgr.Config()
	if strings.TrimSpace(dataDir) == "" {
		dataDir = cfg.Zotero.DataDir
	}
	if strings.TrimSpace(apiBase) == "" {
		apiBase = cfg.Zotero.APIBase
	}
	if strings.TrimSpace(libraryID) == "" {
		libraryID = cfg.Zotero.LibraryID
	}
	c := zotero.New(zotero.Config{DataDir: dataDir, APIBase: apiBase, LibraryID: libraryID})
	defer c.Close()
	cols, err := c.Collections(ctx)
	if err != nil {
		return nil, err
	}
	return zotero.CollectionTree(cols), nil
}

// Create 为一个 Zotero 集合创建文献库，并写入联动配置。
func (s *Service) Create(ctx context.Context, req CreateRequest) (kb.Info, error) {
	cfg := s.mgr.Config()
	if !cfg.Zotero.Enabled {
		return kb.Info{}, fmt.Errorf("请先在「文献库」页开启 Zotero 联动")
	}
	dataDir := firstNonEmpty(req.DataDir, cfg.Zotero.DataDir)
	apiBase := firstNonEmpty(req.APIBase, cfg.Zotero.APIBase)
	libraryID := firstNonEmpty(req.LibraryID, cfg.Zotero.LibraryID)
	collectionKey := strings.TrimSpace(req.CollectionKey)
	if collectionKey == "" {
		return kb.Info{}, fmt.Errorf("请选择 Zotero 集合")
	}
	c := zotero.New(zotero.Config{DataDir: dataDir, APIBase: apiBase, LibraryID: libraryID})
	defer c.Close()
	cols, err := c.Collections(ctx)
	if err != nil {
		return kb.Info{}, err
	}
	collectionName := strings.TrimSpace(req.CollectionName)
	for _, col := range cols {
		if col.Key == collectionKey {
			if collectionName == "" {
				collectionName = col.Name
			}
			break
		}
	}
	if collectionName == "" {
		collectionName = collectionKey
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = collectionName
	}
	info, err := s.mgr.Create(ctx, name)
	if err != nil {
		return kb.Info{}, err
	}
	a, err := s.mgr.Get(info.ID)
	if err != nil {
		return kb.Info{}, err
	}
	mdMode := normalizeMDMode(firstNonEmpty(req.MDMode, cfg.Zotero.MDMode))
	parseMode := normalizeZoteroParseMode(firstNonEmpty(req.ParseMode, cfg.Zotero.ParseMode))
	style := cite.NormalizeStyle(firstNonEmpty(req.CiteStyle, cfg.Zotero.CiteStyle))
	metas := map[string]string{
		MetaKind:                 "zotero",
		MetaZoteroDataDir:        dataDir,
		MetaZoteroAPIBase:        apiBase,
		MetaZoteroLibraryID:      libraryID,
		MetaZoteroCollection:     collectionKey,
		MetaZoteroCollectionName: collectionName,
		MetaZoteroIncludeSub:     boolString(req.IncludeSubcollections),
		MetaZoteroMDMode:         mdMode,
		MetaZoteroParseMode:      parseMode,
		MetaZoteroCiteStyle:      style,
	}
	for k, v := range metas {
		if err := a.Store.SetMeta(ctx, k, v); err != nil {
			return kb.Info{}, err
		}
	}
	info.Kind = "zotero"
	info.ZoteroCollection = collectionName
	info.ZoteroDataDir = dataDir
	if req.SyncNow {
		go func() {
			if _, err := s.Sync(context.Background(), info.ID, SyncOptions{}); err != nil {
				s.logger.Warn("Zotero 首次同步启动失败", "kb", info.ID, "err", err)
			}
		}()
	}
	return info, nil
}

// StartSync 启动后台同步，立即返回任务状态。
func (s *Service) StartSync(ctx context.Context, kbID string, opt SyncOptions) (*Job, error) {
	s.mu.Lock()
	if job, ok := s.jobs[kbID]; ok && job.State == "running" {
		cp := *job
		s.mu.Unlock()
		return &cp, nil
	}
	s.mu.Unlock()
	// 后台任务不能绑定 HTTP 请求 context，否则 handler 返回即被取消。
	return s.Sync(context.Background(), kbID, opt)
}

// Sync 同步一个文献库。ctx 取消时任务进入 canceled。
func (s *Service) Sync(ctx context.Context, kbID string, opt SyncOptions) (*Job, error) {
	s.mu.Lock()
	if job, ok := s.jobs[kbID]; ok && job.State == "running" {
		cp := *job
		s.mu.Unlock()
		return &cp, nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	job := &Job{KBID: kbID, State: "running", StartedAt: time.Now(), cancel: cancel}
	s.jobs[kbID] = job
	s.mu.Unlock()

	// 同步在后台跑，调用方立即拿到 job。
	go s.run(runCtx, kbID, opt, job)
	return s.Job(kbID), nil
}

// Job 返回某个库最近一次同步状态；没有任务时返回 idle 状态。
func (s *Service) Job(kbID string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, ok := s.jobs[kbID]; ok {
		cp := *job
		cp.cancel = nil
		if len(job.Errors) > 0 {
			cp.Errors = append([]string(nil), job.Errors...)
		}
		return &cp
	}
	return &Job{KBID: kbID, State: "idle"}
}

// Cancel 取消正在运行的同步。
func (s *Service) Cancel(kbID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, ok := s.jobs[kbID]; ok && job.cancel != nil && job.State == "running" {
		job.cancel()
		return true
	}
	return false
}

func (s *Service) run(ctx context.Context, kbID string, opt SyncOptions, job *Job) {
	defer func() {
		s.mu.Lock()
		job.cancel = nil
		if job.State == "running" {
			job.State = "ok"
		}
		job.FinishedAt = time.Now()
		s.mu.Unlock()
	}()

	a, err := s.mgr.Get(kbID)
	if err != nil {
		s.failJob(job, err)
		return
	}
	meta := s.meta(a)
	dataDir := firstNonEmpty(meta[MetaZoteroDataDir], s.mgr.Config().Zotero.DataDir)
	apiBase := firstNonEmpty(meta[MetaZoteroAPIBase], s.mgr.Config().Zotero.APIBase, "http://127.0.0.1:23119/api")
	libraryID := firstNonEmpty(meta[MetaZoteroLibraryID], s.mgr.Config().Zotero.LibraryID)
	collectionKey := meta[MetaZoteroCollection]
	collectionName := meta[MetaZoteroCollectionName]
	includeSub := meta[MetaZoteroIncludeSub] == "1" || strings.EqualFold(meta[MetaZoteroIncludeSub], "true")
	mdMode := normalizeMDMode(firstNonEmpty(meta[MetaZoteroMDMode], s.mgr.Config().Zotero.MDMode))
	parseMode := normalizeZoteroParseMode(firstNonEmpty(meta[MetaZoteroParseMode], s.mgr.Config().Zotero.ParseMode))
	if collectionKey == "" {
		s.failJob(job, fmt.Errorf("该知识库不是 Zotero 文献库"))
		return
	}

	source := zotero.New(zotero.Config{DataDir: dataDir, APIBase: apiBase, LibraryID: libraryID})
	defer source.Close()

	// 先取集合名（旧库可能没有缓存）。
	if collectionName == "" {
		if cols, err := source.Collections(ctx); err == nil {
			for _, col := range cols {
				if col.Key == collectionKey {
					collectionName = col.Name
					break
				}
			}
		}
	}
	if collectionName == "" {
		collectionName = collectionKey
	}

	items, err := source.CollectionItems(ctx, collectionKey, includeSub)
	if err != nil {
		s.failJob(job, err)
		return
	}
	s.mu.Lock()
	job.Total = len(items)
	job.Message = fmt.Sprintf("发现 %d 篇文献", len(items))
	s.mu.Unlock()

	// 解析/全文/嵌入以网络 I/O 为主，适度并发可显著缩短大库首次同步时间；
	// 数据库写入仍由 store 的单连接串行化。
	const syncWorkers = 3
	itemCh := make(chan zotero.Item)
	var wg sync.WaitGroup
	for i := 0; i < syncWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range itemCh {
				if ctx.Err() != nil {
					return
				}
				s.setCurrent(job, item.Title)
				skipped, err := s.syncItem(ctx, a, source, item, libraryID, collectionKey, collectionName, dataDir, mdMode, parseMode, opt)
				s.mu.Lock()
				job.Done++
				if err != nil {
					job.Failed++
					if len(job.Errors) < 20 {
						job.Errors = append(job.Errors, item.Title+"："+err.Error())
					}
				} else if skipped {
					job.Skipped++
				}
				s.mu.Unlock()
				if err != nil {
					s.logger.Warn("Zotero 文献同步失败", "kb", kbID, "item", item.Key, "err", err)
					_ = a.Store.SetLibraryItemStatus(ctx, item.Key, "error", err.Error())
				}
			}
		}()
	}
sendLoop:
	for _, item := range items {
		select {
		case <-ctx.Done():
			break sendLoop
		case itemCh <- item:
		}
	}
	close(itemCh)
	wg.Wait()
	if ctx.Err() != nil {
		s.mu.Lock()
		job.State = "canceled"
		job.Message = "已取消"
		s.mu.Unlock()
		return
	}

	// 汇总状态。
	list, _ := a.Store.ListLibraryItems(ctx)
	synced, failed := 0, 0
	for _, it := range list {
		switch it.SyncStatus {
		case "ok":
			synced++
		case "error":
			failed++
		}
	}
	_ = a.Store.SetMeta(ctx, MetaZoteroLastSync, time.Now().Format(time.RFC3339))
	_ = a.Store.SetMeta(ctx, MetaZoteroSynced, fmt.Sprintf("%d", synced))
	_ = a.Store.SetMeta(ctx, MetaZoteroFailed, fmt.Sprintf("%d", failed))

	s.mu.Lock()
	job.Message = fmt.Sprintf("同步完成：成功 %d，失败 %d", synced, failed)
	s.mu.Unlock()
}

func (s *Service) syncItem(ctx context.Context, a *app.App, source *zotero.Client, item zotero.Item, libraryID, collectionKey, collectionName, dataDir, mdMode, parseMode string, opt SyncOptions) (bool, error) {
	// 一次读取旧条目：既用于跳过判断，也用于保留稳定的 citation key。
	oldItem, oldExists, _ := a.Store.GetLibraryItem(ctx, item.Key)
	if !opt.Force && oldExists && oldItem.DateModified == item.DateModified && oldItem.DocID != "" {
		if doc, ok, _ := a.Store.GetDocument(ctx, oldItem.DocID); ok && doc.EmbedStatus == "ok" && doc.ParseStatus == "ok" {
			_ = a.Store.SetLibraryItemStatus(ctx, item.Key, "ok", "内容未变化，已跳过")
			return true, nil
		}
	}
	_ = a.Store.SetLibraryItemStatus(ctx, item.Key, "running", "")

	att, hasAtt := zotero.PrimaryAttachment(item.Attachments)
	attPath := ""
	if hasAtt {
		attPath = source.AttachmentPath(att)
		// 链接文件路径失效时，用本地 API 下载到 Larder 缓存兜底。
		if attPath == "" {
			if b, rerr := source.ReadAttachment(ctx, att); rerr == nil && len(b) > 0 {
				if p, werr := cacheAttachment(dataDir, att, b); werr == nil {
					attPath = p
				}
			}
		}
	}

	var res ingest.Result
	var err error
	fulltextSource := "parsed"
	ingested := false

	// layout：优先用 MinerU/Docling 解析 PDF，保留标题层级、版式、表格与图片；
	// auto：配置了外部解析器就用版面解析，失败后回退 Zotero 全文缓存；
	// zotero：只用 Zotero 全文缓存（快，但为纯文本，无图片）。
	if parseMode == "layout" {
		if !a.HasLayoutParser() {
			return false, fmt.Errorf("layout 模式需要先在设置中配置 MinerU / Docling / Tika 解析服务")
		}
		if attPath == "" {
			return false, fmt.Errorf("layout 模式需要 PDF 附件，但该条目没有可用附件")
		}
	}
	layoutFirst := attPath != "" && (parseMode == "layout" || (parseMode == "auto" && a.HasLayoutParser()))
	if layoutFirst {
		res, err = a.Ingest.IngestFile(ctx, attPath, ingest.Options{Title: item.Title, PreferExternal: true})
		if err == nil {
			fulltextSource = "layout"
			ingested = true
		} else if parseMode == "layout" {
			return false, fmt.Errorf("PDF 版面解析失败: %w", err)
		} else {
			s.logger.Warn("Zotero 版面解析失败，回退全文缓存", "item", item.Key, "err", err)
			err = nil
		}
	}

	if !ingested && hasAtt {
		fulltext, hasFulltext, ferr := source.Fulltext(ctx, att.Key)
		if ferr == nil && hasFulltext && usefulFulltext(fulltext) {
			fulltextSource = "zotero"
			fileName := att.FileName
			if fileName == "" {
				fileName = item.Key + ".pdf"
			}
			res, err = a.Ingest.IngestMarkdown(ctx, zoteroSource(libraryID, item.Key), fileName, "text/markdown", fulltext.Content, ingest.Options{
				Title: item.Title, OriginPath: attPath, OriginType: "file",
			})
			ingested = true
		}
	}
	if !ingested {
		if attPath != "" {
			res, err = a.Ingest.IngestFile(ctx, attPath, ingest.Options{Title: item.Title})
		} else {
			// 没有可用附件时，把元数据 + 摘要作为一条短文档，至少让引用可检索。
			text := metadataText(item)
			if strings.TrimSpace(text) == "" {
				err = fmt.Errorf("没有可用附件或全文")
			} else {
				fulltextSource = "metadata"
				res, err = a.Ingest.IngestMarkdown(ctx, zoteroSource(libraryID, item.Key), item.Key+".md", "text/markdown", text, ingest.Options{
					Title: item.Title, OriginType: "zotero",
				})
			}
		}
	}
	if err != nil {
		return false, err
	}

	markdown, _ := a.Store.GetDocumentMarkdown(ctx, res.DocID)
	if mdMode != "off" && strings.TrimSpace(markdown) != "" {
		assetsDir := filepath.Join(a.Cfg.Storage.Dir, ".assets", res.DocID)
		if _, werr := writeMarkdownSidecar(dataDir, mdMode, collectionName, att, item, markdown, assetsDir); werr != nil {
			s.logger.Warn("写入 Zotero Markdown 失败", "item", item.Key, "err", werr)
		}
	}

	citeItem := citeItemFromZotero(item)
	citeKey := cite.CitationKey(citeItem)
	// 中文题名/作者会被 ASCII 过滤，生成的关键字可能退化成 "2017"/"ref2017"；
	// 这种情况回退到 Zotero item key，保证唯一且稳定。
	if weakCiteKey(citeKey) {
		citeKey = item.Key
	}
	if oldExists && oldItem.CiteKey != "" {
		// 保留已经稳定下来的 citation key，避免 BibTeX/LaTeX 引用漂移。
		citeKey = oldItem.CiteKey
	}
	citeItem.Key = citeKey
	libItem := model.LibraryItem{
		ItemKey: item.Key, LibraryID: libraryID, CollectionKey: collectionKey,
		ItemType: item.ItemType, Title: item.Title, Year: item.Year(), Date: item.Date,
		DOI: item.DOI, URL: item.URL, Authors: item.Authors(), Journal: item.Journal,
		Volume: item.Volume, Issue: item.Issue, Pages: item.Pages, Publisher: item.Publisher,
		Language: item.Language, Abstract: item.Abstract,
		CiteKey: citeKey, BibTeX: cite.BibTeX(citeItem),
		AttachmentKey: att.Key, AttachmentPath: attPath, FulltextSource: fulltextSource,
		ItemVersion: item.Version, DateModified: item.DateModified, DocID: res.DocID,
		SyncStatus: "ok", UpdatedAt: time.Now().UTC(),
	}
	if err := a.Store.UpsertLibraryItem(ctx, libItem); err != nil {
		return false, err
	}
	return false, nil
}

// weakCiteKey 判断生成的关键字是否缺少足够的作者/题名信息。
func weakCiteKey(key string) bool {
	key = strings.TrimSpace(key)
	if len(key) < 5 {
		return true
	}
	if strings.HasPrefix(key, "ref") {
		return true
	}
	allDigits := true
	for _, r := range key {
		if r < '0' || r > '9' {
			allDigits = false
			break
		}
	}
	return allDigits
}

// citeItemFromZotero 把 Zotero 条目转成引用格式化结构。
// 优先使用 BBT 的 citation key；没有时由 cite 包生成稳定 key。
func citeItemFromZotero(it zotero.Item) cite.Item {
	return cite.Item{
		Key: it.CitationKey(), ItemType: it.ItemType, Title: it.Title,
		Authors: it.Authors(), Year: it.Year(), Date: it.Date,
		Journal: it.Journal, Volume: it.Volume, Issue: it.Issue, Pages: it.Pages,
		Publisher: it.Publisher, Place: it.Place, DOI: it.DOI, URL: it.URL,
		Language: it.Language, Abstract: it.Abstract,
	}
}

func (s *Service) failJob(job *Job, err error) {
	s.mu.Lock()
	job.State = "error"
	job.Message = err.Error()
	job.FinishedAt = time.Now()
	s.mu.Unlock()
	s.logger.Warn("Zotero 同步失败", "kb", job.KBID, "err", err)
}

func (s *Service) setCurrent(job *Job, title string) {
	s.mu.Lock()
	job.Current = title
	s.mu.Unlock()
}

func (s *Service) meta(a *app.App) map[string]string {
	keys := []string{
		MetaKind, MetaZoteroDataDir, MetaZoteroAPIBase, MetaZoteroLibraryID, MetaZoteroCollection,
		MetaZoteroCollectionName, MetaZoteroIncludeSub, MetaZoteroMDMode,
		MetaZoteroParseMode, MetaZoteroCiteStyle, MetaZoteroLastSync,
	}
	out := map[string]string{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, k := range keys {
		if v, ok, err := a.Store.GetMeta(ctx, k); err == nil && ok {
			out[k] = v
		}
	}
	return out
}

// CiteItem 把 LibraryItem 转成引用格式化所需的 Item。
func CiteItem(it model.LibraryItem) cite.Item {
	return cite.Item{
		Key: firstNonEmpty(it.CiteKey, it.ItemKey), ItemType: it.ItemType,
		Title: it.Title, Authors: it.Authors, Year: it.Year, Date: it.Date,
		Journal: it.Journal, Volume: it.Volume, Issue: it.Issue, Pages: it.Pages,
		Publisher: it.Publisher, Place: "", DOI: it.DOI, URL: it.URL,
		Language: it.Language, Abstract: it.Abstract,
	}
}

// ItemsByDocIDs 返回文档 ID 对应的文献条目，缺失的文档用占位条目补齐。
func (s *Service) ItemsByDocIDs(ctx context.Context, kbID string, docIDs []string) ([]model.LibraryItem, error) {
	a, err := s.mgr.Get(kbID)
	if err != nil {
		return nil, err
	}
	m, err := a.Store.GetLibraryItemsByDocIDs(ctx, docIDs)
	if err != nil {
		return nil, err
	}
	out := make([]model.LibraryItem, 0, len(docIDs))
	for _, id := range docIDs {
		if it, ok := m[id]; ok {
			out = append(out, it)
			continue
		}
		if doc, ok, _ := a.Store.GetDocument(ctx, id); ok {
			out = append(out, model.LibraryItem{
				ItemKey: doc.ID, Title: doc.Title, Authors: []string{}, DocID: doc.ID,
				AttachmentPath: doc.OriginPath, ItemType: "document",
			})
		}
	}
	return out, nil
}

// AllItems 返回知识库内的全部文献条目；非 Zotero 库返回空列表。
func (s *Service) AllItems(ctx context.Context, kbID string) ([]model.LibraryItem, error) {
	a, err := s.mgr.Get(kbID)
	if err != nil {
		return nil, err
	}
	return a.Store.ListLibraryItems(ctx)
}

// LibraryItemCounts 返回同步成功/失败数量。
func (s *Service) Counts(ctx context.Context, kbID string) (total, okCount, failed int, err error) {
	items, err := s.AllItems(ctx, kbID)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, it := range items {
		switch it.SyncStatus {
		case "ok":
			okCount++
		case "error":
			failed++
		}
	}
	return len(items), okCount, failed, nil
}

func zoteroSource(libraryID, itemKey string) string {
	return "zotero://" + libraryID + "/" + itemKey
}

func usefulFulltext(ft zotero.Fulltext) bool {
	if len(strings.TrimSpace(ft.Content)) < 400 {
		return false
	}
	// 有页数信息时要求至少索引了一半页面；扫描件常常只索引到几页。
	if ft.TotalPages > 0 && ft.IndexedPages > 0 && ft.IndexedPages*2 < ft.TotalPages {
		return false
	}
	return true
}

func metadataText(it zotero.Item) string {
	var b strings.Builder
	if it.Title != "" {
		b.WriteString("# ")
		b.WriteString(it.Title)
		b.WriteString("\n\n")
	}
	if len(it.Creators) > 0 {
		b.WriteString("作者：")
		b.WriteString(strings.Join(it.Authors(), ", "))
		b.WriteString("\n\n")
	}
	if it.Journal != "" {
		b.WriteString("来源：")
		b.WriteString(it.Journal)
		b.WriteString("\n\n")
	}
	if it.Abstract != "" {
		b.WriteString("## 摘要\n\n")
		b.WriteString(it.Abstract)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func normalizeMDMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "larder", "storage":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "storage"
	}
}

func normalizeZoteroParseMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "layout", "zotero":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "auto"
	}
}

func boolString(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// cacheAttachment 在链接文件路径失效时缓存附件字节，供解析与重新处理使用。
func cacheAttachment(dataDir string, att zotero.Attachment, data []byte) (string, error) {
	if strings.TrimSpace(dataDir) == "" || len(data) == 0 {
		return "", fmt.Errorf("数据目录或附件内容为空")
	}
	dir := filepath.Join(dataDir, ".zotero-cache", att.Key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := sanitizeFileName(att.FileName)
	if name == "" {
		name = att.Key
	}
	p := filepath.Join(dir, name)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return p, nil
}

// writeMarkdownSidecar 把解析后的 Markdown 写到 Zotero 目录。
// storage：PDF 同目录；larder：<数据目录>/larder-md/<集合>/。
var assetSidecarRE = regexp.MustCompile(`asset://([^\s)]+)`)

func writeMarkdownSidecar(dataDir, mode, collectionName string, att zotero.Attachment, item zotero.Item, markdown, assetsDir string) (string, error) {
	if strings.TrimSpace(dataDir) == "" || strings.TrimSpace(markdown) == "" {
		return "", fmt.Errorf("数据目录或内容为空")
	}
	title := sanitizeFileName(item.Title)
	if title == "" {
		title = item.Key
	}
	var dir, name string
	switch mode {
	case "larder":
		dir = filepath.Join(dataDir, "larder-md", sanitizeFileName(collectionName))
		name = title + " [" + item.Key + "].md"
	case "off":
		return "", nil
	default: // storage
		if att.Key == "" {
			return "", fmt.Errorf("没有附件 key，无法写入 PDF 同目录")
		}
		dir = filepath.Join(dataDir, "storage", att.Key)
		name = title + ".md"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if assetsDir != "" {
		markdown = copyAssetsToSidecar(assetsDir, dir, markdown)
	}
	p := filepath.Join(dir, name)
	// larder 模式下不同论文可能同名：追加 item key，避免互相覆盖。
	// storage 模式目录已按附件 key 隔离，同一篇论文更新时直接覆盖。
	if mode != "storage" {
		if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
			if old, rerr := os.ReadFile(p); rerr == nil && string(old) != markdown {
				p = filepath.Join(dir, title+" ["+item.Key+"].md")
			}
		}
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(markdown), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return p, nil
}

// copyAssetsToSidecar 把 asset:// 图片复制到 Markdown 同目录的 images/，
// 并把引用改成相对路径，保证导出的 Markdown 可以脱离 Larder 打开。
func copyAssetsToSidecar(assetsDir, mdDir, markdown string) string {
	names := map[string]bool{}
	for _, m := range assetSidecarRE.FindAllStringSubmatch(markdown, -1) {
		if len(m) == 2 {
			names[m[1]] = true
		}
	}
	if len(names) == 0 {
		return markdown
	}
	imageDir := filepath.Join(mdDir, "images")
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		return markdown
	}
	for name := range names {
		if name == "" || strings.ContainsAny(name, `/\`) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(assetsDir, name))
		if err != nil {
			continue
		}
		_ = os.WriteFile(filepath.Join(imageDir, name), b, 0o644)
	}
	return assetSidecarRE.ReplaceAllStringFunc(markdown, func(m string) string {
		name := strings.TrimPrefix(m, "asset://")
		if _, err := os.Stat(filepath.Join(imageDir, name)); err != nil {
			return m
		}
		return "images/" + name
	})
}

func sanitizeFileName(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20:
			continue
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), " .")
	// Windows 保留名。
	upper := strings.ToUpper(out)
	switch upper {
	case "CON", "PRN", "AUX", "NUL":
		out = "_" + out
	}
	if len([]rune(out)) > 120 {
		out = string([]rune(out)[:120])
	}
	return out
}
