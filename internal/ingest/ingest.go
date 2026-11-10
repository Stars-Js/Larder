// Package ingest 负责“来源 → 解析 → 切块 → 向量化 → 落库”的完整入库流程。
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"larder/internal/chunk"
	"larder/internal/model"
	"larder/internal/provider/embed"
	"larder/internal/provider/parse"
	"larder/internal/store"
)

// maxSourceBytes 限制单个来源大小，防止误喂超大文件（与 MinerU 云 API 200MB 上限对齐）。
const maxSourceBytes = 200 << 20

// Service 编排入库流程。
type Service struct {
	store         store.Store
	parser        parse.Parser
	embedder      embed.Embedder
	chunkOpt      chunk.Options
	embedModel    string
	rerankEnabled bool
	uploadDir     string
	http          *http.Client
}

// New 创建入库服务。embedModel 仅用于记录（展示向量化所用模型），
// rerankEnabled 表示入库时是否启用重排，写入文档状态；uploadDir 用于缓存上传原文件，
// 便于解析失败后配置好服务再“重新处理”。
func New(st store.Store, parser parse.Parser, embedder embed.Embedder, chunkOpt chunk.Options, embedModel string, rerankEnabled bool, uploadDir string) *Service {
	return &Service{
		store:         st,
		parser:        parser,
		embedder:      embedder,
		chunkOpt:      chunkOpt,
		embedModel:    embedModel,
		rerankEnabled: rerankEnabled,
		uploadDir:     uploadDir,
		http:          &http.Client{Timeout: 120 * time.Second},
	}
}

// Options 控制一次入库行为。
type Options struct {
	Title   string // 可选，覆盖自动推断的标题
	Refresh bool   // true 时即使内容未变也重新入库
	// 以下字段由服务内部在“重新处理”时使用。
	DocID      string
	OriginPath string
	OriginType string // file / url / upload
}

// Result 描述入库结果。失败时 DocID 仍会返回，便于前端定位已落库的失败记录。
type Result struct {
	DocID   string `json:"doc_id"`
	Title   string `json:"title"`
	Source  string `json:"source"`
	Chunks  int    `json:"chunks"`
	Skipped bool   `json:"skipped"`
	Reason  string `json:"reason,omitempty"`

	ParseStatus  string `json:"parse_status,omitempty"`
	ParseMethod  string `json:"parse_method,omitempty"`
	ParseNote    string `json:"parse_note,omitempty"`
	EmbedStatus  string `json:"embed_status,omitempty"`
	EmbedModel   string `json:"embed_model,omitempty"`
	EmbedNote    string `json:"embed_note,omitempty"`
	EmbedDim     int    `json:"embed_dim,omitempty"`
	RerankStatus string `json:"rerank_status,omitempty"`
}

// IngestFile 从本地文件入库。
func (s *Service) IngestFile(ctx context.Context, path string, opt Options) (Result, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Result{}, fmt.Errorf("解析路径失败: %w", err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return Result{}, fmt.Errorf("读取文件失败: %w", err)
	}
	if fi.IsDir() {
		return Result{}, fmt.Errorf("%s 是目录，请传入具体文件", abs)
	}
	if fi.Size() > maxSourceBytes {
		return Result{}, fmt.Errorf("文件过大（%d MB），上限为 %d MB", fi.Size()>>20, maxSourceBytes>>20)
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return Result{}, fmt.Errorf("读取文件失败: %w", err)
	}
	opt.OriginPath = abs
	opt.OriginType = "file"
	return s.ingestBytes(ctx, abs, filepath.Base(abs), "", data, opt)
}

// IngestURL 从 URL 下载后入库。
func (s *Service) IngestURL(ctx context.Context, url string, opt Options) (Result, error) {
	data, mime, name, err := s.downloadURL(ctx, url)
	if err != nil {
		return Result{}, err
	}
	opt.OriginPath = url
	opt.OriginType = "url"
	return s.ingestBytes(ctx, url, name, mime, data, opt)
}

// IngestData 直接入库一段字节（供 HTTP 上传使用）。source 用作来源标识。
// 上传内容会缓存到 data/.uploads/，解析失败后可重新处理而无需再次拖入。
func (s *Service) IngestData(ctx context.Context, source, fileName, mime string, data []byte, opt Options) (Result, error) {
	if source == "" {
		source = fileName
	}
	if cached, err := s.cacheUpload(data, fileName); err == nil {
		opt.OriginPath = cached
		opt.OriginType = "upload"
	} else {
		opt.OriginType = "upload"
	}
	return s.ingestBytes(ctx, source, fileName, mime, data, opt)
}

// Reprocess 使用已保存的原始文件/URL 重新执行解析→切块→向量化。
func (s *Service) Reprocess(ctx context.Context, docID string) (Result, error) {
	doc, ok, err := s.store.GetDocument(ctx, docID)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, fmt.Errorf("文档不存在: %s", docID)
	}

	originPath := doc.OriginPath
	originType := doc.OriginType
	if originPath == "" {
		switch {
		case strings.HasPrefix(doc.Source, "http://") || strings.HasPrefix(doc.Source, "https://"):
			originPath, originType = doc.Source, "url"
		case fileExists(doc.Source):
			originPath, originType = doc.Source, "file"
		}
	}
	if originPath == "" {
		return Result{}, fmt.Errorf("找不到原始文件，无法重新处理；请重新拖入文件")
	}

	var data []byte
	var mime, name string
	switch originType {
	case "url":
		data, mime, name, err = s.downloadURL(ctx, originPath)
		if err != nil {
			return Result{}, err
		}
	case "file", "upload":
		data, err = os.ReadFile(originPath)
		if err != nil {
			return Result{}, fmt.Errorf("读取原始文件失败（%s）: %w", originPath, err)
		}
		name = filepath.Base(doc.Source)
		if name == "" || name == "." {
			name = filepath.Base(originPath)
		}
	default:
		return Result{}, fmt.Errorf("未知的来源类型 %q，请重新拖入文件", originType)
	}
	if mime == "" {
		mime = doc.MIME
	}

	return s.ingestBytes(ctx, doc.Source, name, mime, data, Options{
		DocID:      doc.ID,
		Title:      doc.Title,
		Refresh:    true,
		OriginPath: originPath,
		OriginType: originType,
	})
}

func (s *Service) downloadURL(ctx context.Context, url string) (data []byte, mime, name string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", "", fmt.Errorf("构造请求失败: %w", err)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", "", fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, maxSourceBytes))
	if err != nil {
		return nil, "", "", fmt.Errorf("读取响应失败: %w", err)
	}
	return data, resp.Header.Get("Content-Type"), fileNameFromURL(url), nil
}

func (s *Service) cacheUpload(data []byte, fileName string) (string, error) {
	if s.uploadDir == "" {
		return "", fmt.Errorf("未配置上传缓存目录")
	}
	if err := os.MkdirAll(s.uploadDir, 0o755); err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(fileName))
	if len(ext) > 12 {
		ext = ""
	}
	p := filepath.Join(s.uploadDir, hashBytes(data)+ext)
	if fileExists(p) {
		return p, nil
	}
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

func (s *Service) ingestBytes(ctx context.Context, source, fileName, mime string, data []byte, opt Options) (Result, error) {
	if len(data) == 0 {
		return Result{}, fmt.Errorf("来源内容为空: %s", source)
	}

	rawHash := hashBytes(data)
	rerankStatus := "off"
	if s.rerankEnabled {
		rerankStatus = "on"
	}

	// 判重 / 复用失败记录。
	var existing model.Document
	hasExisting := false
	if opt.DocID != "" {
		doc, ok, err := s.store.GetDocument(ctx, opt.DocID)
		if err != nil {
			return Result{}, err
		}
		if !ok {
			return Result{}, fmt.Errorf("文档不存在: %s", opt.DocID)
		}
		existing, hasExisting = doc, true
	}
	if !hasExisting && !opt.Refresh {
		if doc, ok, err := s.store.FindDocByHash(ctx, source, rawHash); err != nil {
			return Result{}, err
		} else if ok {
			if doc.ParseStatus == "ok" && doc.EmbedStatus == "ok" {
				return resultFromDoc(doc, true, "内容未变化，已跳过"), nil
			}
			existing, hasExisting = doc, true
		}
	}
	if !hasExisting && !opt.Refresh {
		// 同一来源上次处理失败（或内容已变化但旧版本失败）时复用该记录。
		if doc, ok, err := s.store.FindDocBySource(ctx, source); err != nil {
			return Result{}, err
		} else if ok && (doc.ParseStatus != "ok" || doc.EmbedStatus != "ok") {
			existing, hasExisting = doc, true
		}
	}

	docID := uuid.NewString()
	if hasExisting {
		docID = existing.ID
	}
	title := firstNonEmpty(opt.Title, existing.Title, fileStem(fileName))
	originPath := firstNonEmpty(opt.OriginPath, existing.OriginPath)
	originType := firstNonEmpty(opt.OriginType, existing.OriginType)
	oldChunks := existing.Chunks

	// 先落一条 pending 记录，让前端立刻看到“处理中”的文件与环节状态。
	pending := store.Document{
		ID: docID, Source: source, Title: title, MIME: mime, Hash: rawHash, Chunks: oldChunks,
		ParseStatus: "running", ParseMethod: s.parser.Name(), ParseNote: "",
		EmbedStatus: "pending", EmbedModel: s.embedModel, EmbedDim: existing.EmbedDim,
		RerankStatus: rerankStatus, OriginPath: originPath, OriginType: originType,
	}
	if err := s.store.UpsertDocument(ctx, pending); err != nil {
		return Result{}, err
	}

	failParse := func(err error) (Result, error) {
		st := store.DocStatus{
			ParseStatus: "error", ParseMethod: s.parser.Name(), ParseNote: err.Error(),
			EmbedStatus: "pending", EmbedModel: s.embedModel, EmbedNote: "", EmbedDim: existing.EmbedDim,
			RerankStatus: rerankStatus, Chunks: oldChunks,
		}
		_ = s.store.SetDocumentStatus(ctx, docID, st)
		return Result{
			DocID: docID, Title: title, Source: source, Chunks: oldChunks,
			ParseStatus: "error", ParseMethod: st.ParseMethod, ParseNote: st.ParseNote,
			EmbedStatus: "pending", EmbedModel: s.embedModel, EmbedDim: existing.EmbedDim, RerankStatus: rerankStatus,
		}, fmt.Errorf("解析失败: %w", err)
	}

	// 解析为 Markdown。
	parsed, err := s.parser.Parse(ctx, parse.Input{
		FileName: fileName,
		MIME:     mime,
		Data:     data,
	})
	if err != nil {
		s.recordUsage(ctx, store.UsageEvent{Kind: "parse", Model: s.parser.Name(), Status: "error"})
		return failParse(err)
	}
	if strings.TrimSpace(parsed.Markdown) == "" {
		s.recordUsage(ctx, store.UsageEvent{Kind: "parse", Model: s.parser.Name(), Status: "error"})
		return failParse(fmt.Errorf("解析结果为空"))
	}
	method := firstNonEmpty(parsed.Method, s.parser.Name())
	title = firstNonEmpty(opt.Title, parsed.Title, existing.Title, fileStem(fileName))
	parseChars := int64(len([]rune(parsed.Markdown)))
	parseNote := fmt.Sprintf("%d 字符", parseChars)
	if parsed.Note != "" {
		parseNote = parsed.Note + "；" + parseNote
	}
	s.recordUsage(ctx, store.UsageEvent{Kind: "parse", Model: method, Chars: parseChars, Status: "ok"})

	// 切块。
	chunks := chunk.Split(parsed.Markdown, s.chunkOpt)
	if len(chunks) == 0 {
		return failParse(fmt.Errorf("切块结果为空"))
	}

	// 向量化。
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = embedText(c.Heading, c.Text)
	}
	embedFail := func(err error) (Result, error) {
		st := store.DocStatus{
			ParseStatus: "ok", ParseMethod: method, ParseNote: parseNote,
			EmbedStatus: "error", EmbedModel: s.embedModel, EmbedNote: err.Error(), EmbedDim: existing.EmbedDim,
			RerankStatus: rerankStatus, Chunks: oldChunks,
		}
		_ = s.store.SetDocumentStatus(ctx, docID, st)
		return Result{
			DocID: docID, Title: title, Source: source, Chunks: oldChunks,
			ParseStatus: "ok", ParseMethod: method, ParseNote: parseNote,
			EmbedStatus: "error", EmbedModel: s.embedModel, EmbedNote: err.Error(), EmbedDim: existing.EmbedDim,
			RerankStatus: rerankStatus,
		}, fmt.Errorf("向量化失败: %w", err)
	}

	// 解析成功、进入向量化阶段，先更新状态。
	_ = s.store.SetDocumentStatus(ctx, docID, store.DocStatus{
		ParseStatus: "ok", ParseMethod: method, ParseNote: parseNote,
		EmbedStatus: "running", EmbedModel: s.embedModel, EmbedDim: existing.EmbedDim,
		RerankStatus: rerankStatus, Chunks: oldChunks,
	})

	embedChars := int64(0)
	for _, t := range texts {
		embedChars += int64(len([]rune(t)))
	}
	vectors, err := s.embedder.Embed(ctx, texts)
	if err != nil {
		s.recordUsage(ctx, store.UsageEvent{Kind: "embed", Model: s.embedModel, Chars: embedChars, Status: "error"})
		return embedFail(err)
	}
	embedTokens := embedChars / 3
	if embedTokens < 1 {
		embedTokens = 1
	}
	if reporter, ok := s.embedder.(embed.UsageReporter); ok {
		if u := reporter.LastUsage(); u.TotalTokens > 0 {
			embedTokens = int64(u.TotalTokens)
		}
	}
	s.recordUsage(ctx, store.UsageEvent{Kind: "embed", Model: s.embedModel, Tokens: embedTokens, Chars: embedChars, Status: "ok"})
	if len(vectors) != len(chunks) {
		return embedFail(fmt.Errorf("向量数(%d)与切块数(%d)不一致", len(vectors), len(chunks)))
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return embedFail(fmt.Errorf("向量服务返回空向量"))
	}

	// 首次入库时确定维度并建向量表。
	if s.store.Dim() == 0 {
		if err := s.store.EnsureDim(len(vectors[0])); err != nil {
			return embedFail(err)
		}
	}
	dim := len(vectors[0])
	if s.store.Dim() != dim {
		return embedFail(fmt.Errorf("向量维度冲突：库中为 %d，当前模型为 %d；请更换数据文件或清空索引后重试", s.store.Dim(), dim))
	}

	modelChunks := chunk.ToModelChunks(docID, chunks)
	if err := s.store.ReplaceChunks(ctx, docID, modelChunks, vectors); err != nil {
		return embedFail(err)
	}
	if err := s.store.UpsertDocument(ctx, store.Document{
		ID: docID, Source: source, Title: title, MIME: parsed.MIME, Hash: rawHash, Chunks: len(modelChunks),
		ParseStatus: "ok", ParseMethod: method, ParseNote: parseNote,
		EmbedStatus: "ok", EmbedModel: s.embedModel, EmbedNote: "", EmbedDim: dim,
		RerankStatus: rerankStatus, OriginPath: originPath, OriginType: originType,
		Markdown: parsed.Markdown,
	}); err != nil {
		return Result{}, err
	}
	// 成功入库后清掉同一原始文件的旧版本（同一路径被修改后重新入库）。
	if originPath != "" {
		_ = s.store.DeleteDocumentsByOriginExcept(ctx, originPath, docID)
	}

	return Result{
		DocID: docID, Title: title, Source: source, Chunks: len(modelChunks),
		ParseStatus: "ok", ParseMethod: method, ParseNote: parseNote,
		EmbedStatus: "ok", EmbedModel: s.embedModel, EmbedDim: dim,
		RerankStatus: rerankStatus,
	}, nil
}

func (s *Service) recordUsage(ctx context.Context, ev store.UsageEvent) {
	_ = s.store.AddUsage(ctx, ev)
}

// embedText 把标题路径拼进正文，提升检索命中率。
func embedText(heading, text string) string {
	if heading == "" {
		return text
	}
	return heading + "\n\n" + text
}

func resultFromDoc(doc model.Document, skipped bool, reason string) Result {
	return Result{
		DocID: doc.ID, Title: doc.Title, Source: doc.Source, Chunks: doc.Chunks,
		Skipped: skipped, Reason: reason,
		ParseStatus: doc.ParseStatus, ParseMethod: doc.ParseMethod, ParseNote: doc.ParseNote,
		EmbedStatus: doc.EmbedStatus, EmbedModel: doc.EmbedModel, EmbedNote: doc.EmbedNote, EmbedDim: doc.EmbedDim,
		RerankStatus: doc.RerankStatus,
	}
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func fileStem(name string) string {
	base := filepath.Base(name)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func fileNameFromURL(url string) string {
	u := url
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	base := filepath.Base(u)
	if base == "." || base == "/" || base == "" {
		return "download"
	}
	return base
}
