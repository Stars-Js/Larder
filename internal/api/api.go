// Package api 提供对外的 REST 接口与前端静态资源服务，支持多知识库。
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"larder/internal/activity"
	"larder/internal/app"
	"larder/internal/cite"
	"larder/internal/config"
	"larder/internal/desktop"
	"larder/internal/ingest"
	"larder/internal/kb"
	libsvc "larder/internal/library"
	"larder/internal/model"
	"larder/internal/provider/embed"
	"larder/internal/provider/parse"
	"larder/internal/provider/rerank"
	"larder/internal/search"
	"larder/internal/services"
	"larder/internal/web"
	"larder/internal/zotero"
)

// maxUpload 限制上传文件大小（与 MinerU 云 API 的 200MB 上限对齐）。
const maxUpload = 200 << 20

// ingestTimeout 是单次入库（含外部解析，可能是异步轮询）允许的最长时间。
// MinerU 云 API 解析大文档/扫描件可能耗时数分钟，这里放宽到 30 分钟，
// 并通过 ResponseController 把该请求的写超时一并延长，避免响应被服务端截断。
const ingestTimeout = 30 * time.Minute

// extendIngest 放宽该请求的写超时并返回带超时的 context 与 cancel。
func extendIngest(w http.ResponseWriter, r *http.Request) (context.Context, func()) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(ingestTimeout))
	ctx, cancel := context.WithTimeout(r.Context(), ingestTimeout)
	return ctx, cancel
}

// Deps 是 REST 层依赖。
type Deps struct {
	Manager    *kb.Manager
	Services   *services.Container
	Library    *libsvc.Service
	SaveConfig func(config.Config) error // 持久化到 larder.toml
	ConfigPath string                    // 当前使用的参数文件路径
	MCPURL     string                    // 实际监听地址对应的 MCP 端点
	Version    string
	Logger     *slog.Logger
	Activity   *activity.Logger
}

// Handler 返回装配好全部路由的 http.Handler。
func Handler(d Deps) http.Handler {
	mux := http.NewServeMux()

	// ---- 全局 ----
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		cfg := d.Services.Config()
		parser, emb, rr := d.Services.Snapshot()
		dim := emb.Dim()
		if dim == 0 {
			dim = cfg.Embed.Dim
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":       "ok",
			"version":      d.Version,
			"embed_dim":    dim,
			"embed_model":  cfg.Embed.Endpoint.Model,
			"rerank":       rr != nil,
			"rerank_model": cfg.Rerank.Endpoint.Model,
			"parse":        cfg.Parse.Provider,
			"parse_mode":   parser.Name(),
			"mcp_enabled":  cfg.Server.MCPEnabled,
			"mcp_path":     cfg.Server.MCPPath,
			"data_dir":     cfg.Storage.Dir,
			"config_path":  d.ConfigPath,
		})
	})

	// ---- 设置 ----
	// 引用短链：MCP 回答里的 [cite] 点击后跳到本次检索日志详情。
	mux.HandleFunc("GET /c/{event}", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("event"))
		if id == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, "/#/logs?event="+url.QueryEscape(id), http.StatusFound)
	})

	// 知识库徽标：供 MCP 回答中的 Markdown 图片使用，显示在回答右上角。
	mux.HandleFunc("GET /api/badge.svg", func(w http.ResponseWriter, r *http.Request) {
		name := "Larder"
		if id := strings.TrimSpace(r.URL.Query().Get("kb")); id != "" {
			if a, err := d.Manager.Get(id); err == nil {
				if n, ok, _ := a.Store.GetMeta(r.Context(), "kb_name"); ok && strings.TrimSpace(n) != "" {
					name = n
				} else {
					name = id
				}
			}
		}
		w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, badgeSVG(name))
	})

	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		cfg := d.Services.Config()
		writeJSON(w, http.StatusOK, settingsResponse(cfg, d.ConfigPath, d.MCPURL, d.Version))
	})

	// 引用格式预览：用固定样例按当前参考文献格式生成正文标记与文末条目。
	mux.HandleFunc("GET /api/citation/preview", func(w http.ResponseWriter, r *http.Request) {
		cfg := d.Services.Config()
		style := cite.NormalizeStyle(firstNonEmptyStr(r.URL.Query().Get("style"), cfg.Citation.ReferenceStyle, cfg.Zotero.CiteStyle))
		it := citationPreviewItem()
		ref := cite.Format(it, style)
		mark := cite.InText(it, 1, style)
		refLine := ref
		if cite.IsNumeric(style) {
			refLine = mark + " " + ref
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"style":          style,
			"mark":           mark,
			"reference":      ref,
			"reference_line": refLine,
		})
	})

	// ---- 多密钥管理（仅本机可读；用于复制/显示/逐条检测） ----
	mux.HandleFunc("GET /api/settings/keys", func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) {
			writeError(w, http.StatusForbidden, errors.New("仅允许本机访问"))
			return
		}
		cfg := d.Services.Config()
		var p config.Provider
		switch r.URL.Query().Get("service") {
		case "parse":
			p = cfg.Parse
		case "embed":
			p = cfg.Embed
		case "rerank":
			p = cfg.Rerank
		default:
			writeError(w, http.StatusBadRequest, errors.New("service 必须是 parse/embed/rerank"))
			return
		}
		keys := p.Endpoint.APIKeys
		if len(keys) == 0 && p.Endpoint.APIKey != "" {
			keys = []string{p.Endpoint.APIKey}
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": keys, "active": 0, "count": len(keys)})
	})
	// ---- MCP 一键接入 Agent ----
	mux.HandleFunc("GET /api/mcp/clients", func(w http.ResponseWriter, r *http.Request) {
		cfg := d.Services.Config()
		writeJSON(w, http.StatusOK, map[string]any{
			"endpoint": d.mcpEndpoint(),
			"clients":  listMCPClients(cfg.Storage.Dir, d.mcpEndpoint()),
		})
	})
	mux.HandleFunc("POST /api/mcp/install", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Client string `json:"client"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cfg := d.Services.Config()
		start := time.Now()
		res, err := installMCPClient(req.Client, d.mcpEndpoint(), cfg.Storage.Dir)
		if err != nil {
			d.record("MCP 注入", "", req.Client, "error", err.Error(), start)
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if res.Config == nil {
			res.Config = clientConfig(req.Client, withClient(d.mcpEndpoint(), req.Client))
		}
		d.record("MCP 注入", "", req.Client, "ok", res.Message, start)
		writeJSON(w, http.StatusOK, res)
	})

	mux.HandleFunc("POST /api/mcp/uninstall", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Client string `json:"client"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cfg := d.Services.Config()
		start := time.Now()
		res, err := uninstallMCPClient(req.Client, cfg.Storage.Dir)
		if err != nil {
			d.record("MCP 取消注入", "", req.Client, "error", err.Error(), start)
			writeError(w, http.StatusBadRequest, err)
			return
		}
		d.record("MCP 取消注入", "", req.Client, "ok", res.Message, start)
		writeJSON(w, http.StatusOK, res)
	})

	// ---- 模型列表 ----
	// 前端填好服务商 Base URL 与 API Key 后自动拉取可用模型。
	// api_key 留空时使用已保存的密钥；base_url 留空时使用已保存的地址。
	mux.HandleFunc("POST /api/models", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			BaseURL string `json:"base_url"`
			APIKey  string `json:"api_key"`
			Path    string `json:"path"`
			Kind    string `json:"kind"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cfg := d.Services.Config()
		baseURL, apiKey, path := req.BaseURL, req.APIKey, req.Path
		switch req.Kind {
		case "embed":
			if baseURL == "" {
				baseURL = cfg.Embed.Endpoint.BaseURL
			}
			if apiKey == "" {
				apiKey = cfg.Embed.Endpoint.APIKey
			}
		case "rerank":
			if baseURL == "" {
				baseURL = cfg.Rerank.Endpoint.BaseURL
			}
			if apiKey == "" {
				apiKey = cfg.Rerank.Endpoint.APIKey
			}
		case "parse":
			if baseURL == "" {
				baseURL = cfg.Parse.Endpoint.BaseURL
			}
			if apiKey == "" {
				apiKey = cfg.Parse.Endpoint.APIKey
			}
		}
		models, err := services.FetchModels(r.Context(), baseURL, apiKey, path, req.Kind)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": models, "count": len(models)})
	})

	// ---- 服务连通性测试（保存前即可验证向量/重排服务） ----
	mux.HandleFunc("POST /api/services/test", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Kind    string `json:"kind"`
			BaseURL string `json:"base_url"`
			APIKey  string `json:"api_key"`
			Model   string `json:"model"`
			Path    string `json:"path"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cfg := d.Services.Config()
		_, currentEmb, currentRR := d.Services.Snapshot()
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		start := time.Now()

		switch req.Kind {
		case "embed":
			base := firstNonEmptyStr(req.BaseURL, cfg.Embed.Endpoint.BaseURL)
			key := firstNonEmptyStr(req.APIKey, cfg.Embed.Endpoint.APIKey)
			model := firstNonEmptyStr(req.Model, cfg.Embed.Endpoint.Model)
			path := firstNonEmptyStr(req.Path, cfg.Embed.Endpoint.Path)
			timeout := cfg.Embed.Endpoint.Timeout.Std()
			if timeout <= 0 {
				timeout = 30 * time.Second
			}
			emb := embed.Embedder(embed.NewOpenAI(base, key, model, path, 0, timeout, 0, 0))
			if req.BaseURL == "" && req.APIKey == "" && req.Model == "" {
				emb = currentEmb
			}
			vecs, err := emb.Embed(ctx, []string{"Larder 连接测试"})
			if err != nil {
				writeError(w, http.StatusBadGateway, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "kind": "embed", "dim": len(vecs[0]),
				"model": model, "latency_ms": time.Since(start).Milliseconds(),
			})
		case "rerank":
			base := firstNonEmptyStr(req.BaseURL, cfg.Rerank.Endpoint.BaseURL)
			key := firstNonEmptyStr(req.APIKey, cfg.Rerank.Endpoint.APIKey)
			model := firstNonEmptyStr(req.Model, cfg.Rerank.Endpoint.Model)
			path := firstNonEmptyStr(req.Path, cfg.Rerank.Endpoint.Path)
			timeout := cfg.Rerank.Endpoint.Timeout.Std()
			if timeout <= 0 {
				timeout = 30 * time.Second
			}
			rr := rerank.Reranker(rerank.NewOpenAI(base, key, model, path, timeout, 0, 0))
			if req.BaseURL == "" && req.APIKey == "" && req.Model == "" {
				rr = currentRR
			}
			if rr == nil {
				writeError(w, http.StatusBadRequest, errors.New("重排服务未启用"))
				return
			}
			scores, err := rr.Rerank(ctx, "连接测试", []string{"Larder 本地知识库", "无关的测试文本"})
			if err != nil {
				writeError(w, http.StatusBadGateway, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "kind": "rerank", "scores": scores,
				"model": model, "latency_ms": time.Since(start).Milliseconds(),
			})
		case "parse":
			base := firstNonEmptyStr(req.BaseURL, cfg.Parse.Endpoint.BaseURL)
			key := firstNonEmptyStr(req.APIKey, cfg.Parse.Endpoint.APIKey)
			if base == "" {
				writeError(w, http.StatusBadRequest, errors.New("请填写解析服务 API 地址"))
				return
			}
			provider := services.DetectParseProvider(base)
			if provider == "" || provider == "plain" {
				provider = cfg.Parse.Provider
				if provider == "" || provider == "plain" {
					provider = "mineru"
				}
			}
			if provider == "mineru" {
				path := firstNonEmptyStr(req.Path, cfg.Parse.Endpoint.Path)
				timeout := cfg.Parse.Endpoint.Timeout.Std()
				if timeout <= 0 {
					timeout = 60 * time.Second
				}
				m := parse.NewMinerU(base, key, path, timeout, 0, 0)
				if model := firstNonEmptyStr(req.Model, cfg.Parse.Endpoint.Model); model != "" {
					m.WithModelVersion(model)
				}
				if err := m.Probe(ctx); err != nil {
					writeError(w, http.StatusBadGateway, err)
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"ok": true, "kind": "parse", "mode": "mineru",
					"note": "MinerU 地址与密钥有效", "latency_ms": time.Since(start).Milliseconds(),
				})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "kind": "parse", "mode": provider,
				"note": "该解析协议没有轻量探活接口，将在入库时验证",
			})
		default:
			writeError(w, http.StatusBadRequest, errors.New("kind 必须是 embed/rerank/parse"))
		}
	})
	mux.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		var dto settingsDTO
		if err := decodeJSON(r, &dto); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cfg := d.Services.Config()
		newCfg, err := dto.applyTo(cfg)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := d.Services.Update(newCfg); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if d.SaveConfig != nil {
			if err := d.SaveConfig(newCfg); err != nil {
				d.Logger.Warn("保存配置失败", "err", err)
			}
		}
		d.Manager.ApplyConfig(newCfg)
		d.record("设置", "", "保存设置", "ok", newCfg.Parse.Provider+" / "+newCfg.Embed.Endpoint.Model, start)
		writeJSON(w, http.StatusOK, settingsResponse(newCfg, d.ConfigPath, d.MCPURL, d.Version))
	})

	// ---- 用量 ----
	// 交互日志：检索、入库、设置、MCP 调用等关键动作。
	mux.HandleFunc("GET /api/logs", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 500 {
			limit = 200
		}
		events := d.Activity.Recent(limit)
		writeJSON(w, http.StatusOK, map[string]any{"events": events, "count": len(events)})
	})

	mux.HandleFunc("GET /api/usage", func(w http.ResponseWriter, r *http.Request) {
		days := 30
		if v := strings.TrimSpace(r.URL.Query().Get("days")); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 365 {
				days = n
			}
		}
		since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		sum, err := d.Manager.Usage(r.Context(), since)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, sum)
	})

	// ---- 知识库 ----
	mux.HandleFunc("GET /api/kbs", func(w http.ResponseWriter, r *http.Request) {
		infos, err := d.Manager.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(infos), "kbs": infos})
	})
	mux.HandleFunc("POST /api/kbs", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		info, err := d.Manager.Create(r.Context(), req.Name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, info)
	})
	mux.HandleFunc("GET /api/kbs/{kb}", func(w http.ResponseWriter, r *http.Request) {
		a, ok := d.getApp(w, r)
		if !ok {
			return
		}
		docs, err := a.Store.ListDocuments(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		chunks, _ := a.Store.CountChunks(r.Context(), "")
		name, _, _ := a.Store.GetMeta(r.Context(), "kb_name")

		parsed, embedded, pending, failed := 0, 0, 0, 0
		for _, doc := range docs {
			if doc.ParseStatus == "ok" {
				parsed++
			}
			if doc.EmbedStatus == "ok" {
				embedded++
			}
			if doc.ParseStatus == "error" || doc.EmbedStatus == "error" {
				failed++
			} else if doc.ParseStatus != "ok" || doc.EmbedStatus != "ok" {
				pending++
			}
		}
		cfg := a.Cfg
		rerankStatus := "off"
		if a.RerankEnabled() {
			rerankStatus = "on"
		}
		settings, _ := d.Manager.GetSettings(r.Context(), a.ID)
		writeJSON(w, http.StatusOK, map[string]any{
			"id": a.ID, "name": name, "documents": docs,
			"doc_count": len(docs), "chunk_count": chunks, "dim": a.EmbedDim(),
			"parse_method": cfg.Parse.Provider, "parse_mode": a.Parser.Name(),
			"embed_model":   cfg.Embed.Endpoint.Model,
			"rerank_status": rerankStatus, "rerank_model": cfg.Rerank.Endpoint.Model,
			"parsed_docs": parsed, "embedded_docs": embedded, "pending_docs": pending, "failed_docs": failed,
			"top_k": cfg.Search.TopK, "chunk_max_runes": cfg.Chunk.MaxRunes,
			"settings": settings,
			"kind":     kbMeta(r.Context(), a, "kb_kind"), "zotero_collection": kbMeta(r.Context(), a, "zotero_collection_name"),
			"zotero_last_sync":  kbMeta(r.Context(), a, "zotero_last_sync"),
			"zotero_parse_mode": kbMeta(r.Context(), a, "zotero_parse_mode"),
			"zotero_cite_style": kbMeta(r.Context(), a, "zotero_cite_style"),
		})
	})
	mux.HandleFunc("PATCH /api/kbs/{kb}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("kb")
		var req struct {
			Name string `json:"name"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			writeError(w, http.StatusBadRequest, errors.New("名称不能为空"))
			return
		}
		if err := d.Manager.Rename(r.Context(), id, req.Name); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("DELETE /api/kbs/{kb}", func(w http.ResponseWriter, r *http.Request) {
		if err := d.Manager.Delete(r.Context(), r.PathValue("kb")); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	})

	// ---- 知识库设置 ----
	mux.HandleFunc("GET /api/kbs/{kb}/settings", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("kb")
		s, err := d.Manager.GetSettings(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		cfg := d.Services.Config()
		raw, _ := d.Manager.GetSettingsRaw(r.Context(), id)
		writeJSON(w, http.StatusOK, map[string]any{
			"settings":      s,
			"raw":           raw,
			"parse_service": cfg.Parse.Provider,
			"embed_service": map[string]any{"model": cfg.Embed.Endpoint.Model, "dim": cfg.Embed.Dim},
			"rerank_service": map[string]any{
				"enabled": cfg.Rerank.Enabled, "model": cfg.Rerank.Endpoint.Model,
			},
		})
	})
	mux.HandleFunc("PUT /api/kbs/{kb}/settings", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("kb")
		var s kb.Settings
		if err := decodeJSON(r, &s); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		updated, err := d.Manager.UpdateSettings(r.Context(), id, s)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": updated})
	})

	// ---- 文档 ----
	mux.HandleFunc("GET /api/kbs/{kb}/documents", func(w http.ResponseWriter, r *http.Request) {
		a, ok := d.getApp(w, r)
		if !ok {
			return
		}
		docs, err := a.Store.ListDocuments(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(docs), "documents": docs})
	})
	mux.HandleFunc("GET /api/kbs/{kb}/documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		a, ok := d.getApp(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		doc, found, err := a.Store.GetDocument(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, errors.New("文档不存在"))
			return
		}
		chunks, _ := a.Store.CountChunks(r.Context(), id)
		writeJSON(w, http.StatusOK, map[string]any{"document": doc, "chunks": chunks})
	})
	// 文档内容：完整 Markdown 原文 + Chunks，供界面预览。
	mux.HandleFunc("GET /api/kbs/{kb}/documents/{id}/content", func(w http.ResponseWriter, r *http.Request) {
		a, ok := d.getApp(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		doc, found, err := a.Store.GetDocument(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, errors.New("文档不存在"))
			return
		}
		markdown, merr := a.Store.GetDocumentMarkdown(r.Context(), id)
		if merr != nil && !errors.Is(merr, sql.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, merr)
			return
		}
		chunks, cerr := a.Store.ListChunks(r.Context(), id)
		if cerr != nil {
			writeError(w, http.StatusInternalServerError, cerr)
			return
		}
		reconstructed := false
		if strings.TrimSpace(markdown) == "" && len(chunks) > 0 {
			markdown = reconstructMarkdown(chunks)
			reconstructed = true
		}
		markdown = rewriteAssetURLs(r.PathValue("kb"), id, markdown)
		hasFile := false
		if doc.OriginPath != "" {
			if doc.OriginType == "url" {
				hasFile = true
			} else if _, statErr := os.Stat(doc.OriginPath); statErr == nil {
				hasFile = true
			}
		}
		fileName := ""
		fileKind := ""
		if hasFile {
			fileName = filepath.Base(doc.Source)
			if fileName == "" || fileName == "." {
				fileName = filepath.Base(doc.OriginPath)
			}
			fileKind = strings.TrimPrefix(strings.ToLower(filepath.Ext(fileName)), ".")
			if fileKind == "" {
				fileKind = strings.TrimPrefix(strings.ToLower(filepath.Ext(doc.OriginPath)), ".")
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"document": doc, "markdown": markdown, "chunks": chunks, "reconstructed": reconstructed,
			"has_file": hasFile, "file_name": fileName, "file_kind": fileKind,
		})
	})
	// 解析图片：MinerU/Docling 抽出的图片按文档存在 data/.assets/<docID>/。
	mux.HandleFunc("GET /api/kbs/{kb}/documents/{id}/assets/{name}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := d.getApp(w, r); !ok {
			return
		}
		name := r.PathValue("name")
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			http.NotFound(w, r)
			return
		}
		path := filepath.Join(d.Services.Config().Storage.Dir, ".assets", r.PathValue("id"), filepath.Base(name))
		if _, err := os.Stat(path); err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFile(w, r, path)
	})

	// 原始文件：PDF 在预览标签页内嵌阅读，其它格式可直接下载。
	mux.HandleFunc("GET /api/kbs/{kb}/documents/{id}/file", func(w http.ResponseWriter, r *http.Request) {
		a, ok := d.getApp(w, r)
		if !ok {
			return
		}
		doc, found, err := a.Store.GetDocument(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, errors.New("文档不存在"))
			return
		}
		if doc.OriginPath == "" {
			writeError(w, http.StatusNotFound, errors.New("原始文件不可用"))
			return
		}
		if doc.OriginType == "url" {
			http.Redirect(w, r, doc.OriginPath, http.StatusFound)
			return
		}
		if _, statErr := os.Stat(doc.OriginPath); statErr != nil {
			writeError(w, http.StatusNotFound, errors.New("原始文件已不存在"))
			return
		}
		name := filepath.Base(doc.Source)
		if name == "" || name == "." {
			name = filepath.Base(doc.OriginPath)
		}
		disp := "inline"
		if r.URL.Query().Get("download") == "1" {
			disp = "attachment"
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": name}))
		http.ServeFile(w, r, doc.OriginPath)
	})
	mux.HandleFunc("DELETE /api/kbs/{kb}/documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		a, ok := d.getApp(w, r)
		if !ok {
			return
		}
		id := r.PathValue("id")
		if id == "" {
			writeError(w, http.StatusBadRequest, errors.New("id 不能为空"))
			return
		}
		if err := a.Store.DeleteDocument(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "doc_id": id})
	})

	// ---- 重新处理（使用已保存的原文件/URL，配置好解析服务后可重试失败文档） ----
	mux.HandleFunc("POST /api/kbs/{kb}/documents/{id}/reprocess", func(w http.ResponseWriter, r *http.Request) {
		a, ok := d.getApp(w, r)
		if !ok {
			return
		}
		ctx, cancel := extendIngest(w, r)
		defer cancel()
		start := time.Now()
		res, err := a.Ingest.Reprocess(ctx, r.PathValue("id"))
		if err != nil {
			d.record("重新处理", r.PathValue("kb"), r.PathValue("id"), "error", err.Error(), start)
			writeIngestError(w, res, err)
			return
		}
		d.record("重新处理", r.PathValue("kb"), res.Title, "ok", fmt.Sprintf("%d 个切块", res.Chunks), start)
		writeJSON(w, http.StatusOK, res)
	})

	// ---- 检索 ----
	mux.HandleFunc("POST /api/kbs/{kb}/search", func(w http.ResponseWriter, r *http.Request) {
		a, ok := d.getApp(w, r)
		if !ok {
			return
		}
		var req struct {
			Query string `json:"query"`
			TopK  int    `json:"top_k"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		start := time.Now()
		topK := clampTopK(a, req.TopK)
		res, err := a.Search.Search(r.Context(), req.Query, search.Options{
			TopK:       topK,
			CandidateK: candidateK(a, topK),
		})
		if err != nil {
			d.record("检索", r.PathValue("kb"), req.Query, "error", err.Error(), start)
			writeError(w, http.StatusBadRequest, err)
			return
		}
		detail := fmt.Sprintf("%d 条", len(res.Hits))
		if res.Reranked {
			detail += " · 已重排"
		}
		d.record("检索", r.PathValue("kb"), req.Query, "ok", detail, start, snippetsFromHits(a.ID, res.Hits)...)
		writeJSON(w, http.StatusOK, map[string]any{
			"count": len(res.Hits), "results": res.Hits, "top_k": topK,
			"reranked": res.Reranked, "rerank_model": a.Cfg.Rerank.Endpoint.Model,
			"rerank_error": res.RerankError,
		})
	})

	// ---- 入库 ----
	mux.HandleFunc("POST /api/kbs/{kb}/ingest/upload", func(w http.ResponseWriter, r *http.Request) {
		a, ok := d.getApp(w, r)
		if !ok {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
		// 超过 32MB 的文件由标准库落到临时文件，避免 200MB 上限时整段占用内存。
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("缺少文件字段 file"))
			return
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		title := r.FormValue("title")
		source := r.FormValue("source")
		mime := header.Header.Get("Content-Type")
		refresh := r.FormValue("refresh") == "true" || r.FormValue("refresh") == "1"
		ctx, cancel := extendIngest(w, r)
		defer cancel()
		start := time.Now()
		res, err := a.Ingest.IngestData(ctx, source, header.Filename, mime, data, ingest.Options{Title: title, Refresh: refresh})
		if err != nil {
			d.record("入库", r.PathValue("kb"), firstNonEmptyStr(title, header.Filename), "error", err.Error(), start)
			writeIngestError(w, res, err)
			return
		}
		d.record("入库", r.PathValue("kb"), res.Title, "ok", fmt.Sprintf("%d 个切块", res.Chunks), start)
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("POST /api/kbs/{kb}/ingest/file", func(w http.ResponseWriter, r *http.Request) {
		a, ok := d.getApp(w, r)
		if !ok {
			return
		}
		var req struct {
			Path    string `json:"path"`
			Title   string `json:"title"`
			Refresh bool   `json:"refresh"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.Path) == "" {
			writeError(w, http.StatusBadRequest, errors.New("path 不能为空"))
			return
		}
		ctx, cancel := extendIngest(w, r)
		defer cancel()
		start := time.Now()
		res, err := a.Ingest.IngestFile(ctx, req.Path, ingest.Options{Title: req.Title, Refresh: req.Refresh})
		if err != nil {
			d.record("入库", r.PathValue("kb"), firstNonEmptyStr(req.Title, req.Path), "error", err.Error(), start)
			writeIngestError(w, res, err)
			return
		}
		d.record("入库", r.PathValue("kb"), res.Title, "ok", fmt.Sprintf("%d 个切块", res.Chunks), start)
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("POST /api/kbs/{kb}/ingest/url", func(w http.ResponseWriter, r *http.Request) {
		a, ok := d.getApp(w, r)
		if !ok {
			return
		}
		var req struct {
			URL     string `json:"url"`
			Title   string `json:"title"`
			Refresh bool   `json:"refresh"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.URL) == "" {
			writeError(w, http.StatusBadRequest, errors.New("url 不能为空"))
			return
		}
		ctx, cancel := extendIngest(w, r)
		defer cancel()
		start := time.Now()
		res, err := a.Ingest.IngestURL(ctx, req.URL, ingest.Options{Title: req.Title, Refresh: req.Refresh})
		if err != nil {
			d.record("入库", r.PathValue("kb"), firstNonEmptyStr(req.Title, req.URL), "error", err.Error(), start)
			writeIngestError(w, res, err)
			return
		}
		d.record("入库", r.PathValue("kb"), res.Title, "ok", fmt.Sprintf("%d 个切块", res.Chunks), start)
		writeJSON(w, http.StatusOK, res)
	})

	// ---- Zotero 文献库 ----
	// 原生文件夹选择：仅本机可调用，用于选择 Zotero 数据目录。
	mux.HandleFunc("POST /api/system/pick-folder", func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) {
			writeError(w, http.StatusForbidden, errors.New("仅允许本机调用"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		path, err := pickFolder(ctx)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"path": path})
	})
	// 原生标题栏跟随应用主题（太阳/月亮切换）。
	mux.HandleFunc("POST /api/system/window-theme", func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) {
			writeError(w, http.StatusForbidden, errors.New("仅允许本机调用"))
			return
		}
		var req struct {
			Theme string `json:"theme"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		theme := "light"
		if strings.EqualFold(strings.TrimSpace(req.Theme), "dark") {
			theme = "dark"
		}
		desktop.SetTheme(theme == "dark")
		writeJSON(w, http.StatusOK, map[string]string{"theme": theme})
	})
	mux.HandleFunc("GET /api/zotero/settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, d.Services.Config().Zotero)
	})
	mux.HandleFunc("PUT /api/zotero/settings", func(w http.ResponseWriter, r *http.Request) {
		var z config.ZoteroConfig
		if err := decodeJSON(r, &z); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		config.NormalizeZotero(&z)
		cfg := d.Services.Config()
		cfg.Zotero = z
		if err := d.Services.Update(cfg); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if d.SaveConfig != nil {
			if err := d.SaveConfig(cfg); err != nil {
				d.Logger.Warn("保存 Zotero 设置失败", "err", err)
			}
		}
		d.Manager.ApplyConfig(cfg)
		writeJSON(w, http.StatusOK, cfg.Zotero)
	})
	mux.HandleFunc("GET /api/zotero/status", func(w http.ResponseWriter, r *http.Request) {
		cfg := d.Services.Config()
		dataDir := cfg.Zotero.DataDir
		detected := config.DefaultZoteroDataDir()
		if dataDir == "" {
			dataDir = detected
		}
		dbAvailable := false
		if _, err := os.Stat(filepath.Join(dataDir, "zotero.sqlite")); err == nil {
			dbAvailable = true
		}
		apiAvailable, apiError := false, ""
		c := zotero.New(zotero.Config{DataDir: dataDir, APIBase: cfg.Zotero.APIBase, LibraryID: cfg.Zotero.LibraryID})
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		if err := c.ProbeAPI(ctx); err != nil {
			apiError = err.Error()
		} else {
			apiAvailable = true
		}
		cancel()
		_ = c.Close()
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": cfg.Zotero.Enabled, "data_dir": dataDir, "detected_data_dir": detected,
			"api_base": cfg.Zotero.APIBase, "library_id": cfg.Zotero.LibraryID, "db_available": dbAvailable,
			"api_available": apiAvailable, "api_error": apiError,
			"md_mode": cfg.Zotero.MDMode, "parse_mode": cfg.Zotero.ParseMode,
			"cite_style": cfg.Zotero.CiteStyle,
			"watch":      cfg.Zotero.Watch, "watch_interval": cfg.Zotero.WatchInterval,
		})
	})
	mux.HandleFunc("GET /api/zotero/collections", func(w http.ResponseWriter, r *http.Request) {
		if d.Library == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("文献库服务未启用"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		tree, err := d.Library.Collections(ctx,
			r.URL.Query().Get("data_dir"), r.URL.Query().Get("api_base"), r.URL.Query().Get("library_id"))
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		count := 0
		var walk func([]*zotero.Collection)
		walk = func(list []*zotero.Collection) {
			for _, c := range list {
				count++
				walk(c.Children)
			}
		}
		walk(tree)
		writeJSON(w, http.StatusOK, map[string]any{"count": count, "collections": tree})
	})
	mux.HandleFunc("POST /api/zotero/libraries", func(w http.ResponseWriter, r *http.Request) {
		if d.Library == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("文献库服务未启用"))
			return
		}
		var req libsvc.CreateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		info, err := d.Library.Create(r.Context(), req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, info)
	})
	mux.HandleFunc("GET /api/kbs/{kb}/zotero/status", func(w http.ResponseWriter, r *http.Request) {
		a, ok := d.getApp(w, r)
		if !ok {
			return
		}
		meta := map[string]string{}
		for _, k := range []string{"kb_kind", "zotero_collection_name", "zotero_collection_key", "zotero_data_dir", "zotero_api_base", "zotero_last_sync", "zotero_parse_mode", "zotero_cite_style"} {
			if v, found, _ := a.Store.GetMeta(r.Context(), k); found {
				meta[k] = v
			}
		}
		total, synced, failed := 0, 0, 0
		job := &libsvc.Job{KBID: r.PathValue("kb"), State: "idle"}
		if d.Library != nil {
			total, synced, failed, _ = d.Library.Counts(r.Context(), r.PathValue("kb"))
			job = d.Library.Job(r.PathValue("kb"))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"kind": meta["kb_kind"], "collection": meta["zotero_collection_name"],
			"collection_key": meta["zotero_collection_key"], "data_dir": meta["zotero_data_dir"],
			"api_base":  meta["zotero_api_base"],
			"last_sync": meta["zotero_last_sync"], "parse_mode": meta["zotero_parse_mode"],
			"cite_style": meta["zotero_cite_style"],
			"total":      total, "synced": synced,
			"failed": failed, "job": job,
		})
	})
	mux.HandleFunc("PATCH /api/kbs/{kb}/zotero/settings", func(w http.ResponseWriter, r *http.Request) {
		a, ok := d.getApp(w, r)
		if !ok {
			return
		}
		var req struct {
			ParseMode string `json:"parse_mode,omitempty"`
			CiteStyle string `json:"cite_style,omitempty"`
			MDMode    string `json:"md_mode,omitempty"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if req.ParseMode != "" {
			switch strings.ToLower(req.ParseMode) {
			case "auto", "layout", "zotero":
				_ = a.Store.SetMeta(r.Context(), "zotero_parse_mode", strings.ToLower(req.ParseMode))
			default:
				writeError(w, http.StatusBadRequest, fmt.Errorf("parse_mode 必须是 auto/layout/zotero"))
				return
			}
		}
		if req.CiteStyle != "" {
			_ = a.Store.SetMeta(r.Context(), "zotero_cite_style", cite.NormalizeStyle(req.CiteStyle))
		}
		if req.MDMode != "" {
			switch strings.ToLower(req.MDMode) {
			case "storage", "larder", "off":
				_ = a.Store.SetMeta(r.Context(), "zotero_md_mode", strings.ToLower(req.MDMode))
			default:
				writeError(w, http.StatusBadRequest, fmt.Errorf("md_mode 必须是 storage/larder/off"))
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"parse_mode": kbMeta(r.Context(), a, "zotero_parse_mode"),
			"cite_style": kbMeta(r.Context(), a, "zotero_cite_style"),
			"md_mode":    kbMeta(r.Context(), a, "zotero_md_mode"),
		})
	})
	mux.HandleFunc("POST /api/kbs/{kb}/zotero/sync", func(w http.ResponseWriter, r *http.Request) {
		if d.Library == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("文献库服务未启用"))
			return
		}
		var opt libsvc.SyncOptions
		_ = decodeJSON(r, &opt)
		job, err := d.Library.StartSync(context.Background(), r.PathValue("kb"), opt)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, job)
	})
	mux.HandleFunc("POST /api/kbs/{kb}/zotero/cancel", func(w http.ResponseWriter, r *http.Request) {
		if d.Library == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("文献库服务未启用"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"canceled": d.Library.Cancel(r.PathValue("kb"))})
	})
	mux.HandleFunc("GET /api/kbs/{kb}/library-items", func(w http.ResponseWriter, r *http.Request) {
		if d.Library == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("文献库服务未启用"))
			return
		}
		items, err := d.Library.AllItems(r.Context(), r.PathValue("kb"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(items), "items": items})
	})
	mux.HandleFunc("GET /api/kbs/{kb}/references", func(w http.ResponseWriter, r *http.Request) {
		if d.Library == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("文献库服务未启用"))
			return
		}
		a, ok := d.getApp(w, r)
		if !ok {
			return
		}
		style := cite.NormalizeStyle(firstNonEmptyStr(r.URL.Query().Get("style"), d.Services.Config().Citation.ReferenceStyle, d.Services.Config().Zotero.CiteStyle))
		docIDs := parseCSV(r.URL.Query().Get("doc_ids"))
		var items []model.LibraryItem
		var err error
		if len(docIDs) > 0 {
			items, err = d.Library.ItemsByDocIDs(r.Context(), r.PathValue("kb"), docIDs)
		} else {
			items, err = d.Library.AllItems(r.Context(), r.PathValue("kb"))
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if len(items) == 0 && len(docIDs) == 0 {
			// 普通知识库没有 Zotero 元数据：用文档标题生成最小参考文献。
			if docs, derr := a.Store.ListDocuments(r.Context()); derr == nil {
				for _, doc := range docs {
					items = append(items, model.LibraryItem{
						ItemKey: doc.ID, DocID: doc.ID, Title: doc.Title,
						ItemType: "document", AttachmentPath: doc.OriginPath,
					})
				}
			}
		}
		refs := cite.BuildReferences(citeItemsFromLibrary(items), style)
		writeJSON(w, http.StatusOK, map[string]any{
			"count": len(refs), "style": style,
			"references": refs, "markdown": referenceMarkdown(refs, style),
			"bibtex": referenceBibTeX(refs),
		})
	})
	mux.HandleFunc("GET /api/kbs/{kb}/bibliography.bib", func(w http.ResponseWriter, r *http.Request) {
		if d.Library == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("文献库服务未启用"))
			return
		}
		if _, ok := d.getApp(w, r); !ok {
			return
		}
		items, err := d.Library.AllItems(r.Context(), r.PathValue("kb"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		style := firstNonEmptyStr(d.Services.Config().Citation.ReferenceStyle, d.Services.Config().Zotero.CiteStyle)
		refs := cite.BuildReferences(citeItemsFromLibrary(items), style)
		w.Header().Set("Content-Type", "application/x-bibtex; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="references.bib"`)
		_, _ = io.WriteString(w, referenceBibTeX(refs))
	})
	// 引用卡片：MCP 回答里的 [1] 悬停/点击后打开。
	mux.HandleFunc("GET /r/{kb}/{doc}", func(w http.ResponseWriter, r *http.Request) {
		a, ok := d.getApp(w, r)
		if !ok {
			return
		}
		doc, found, err := a.Store.GetDocument(r.Context(), r.PathValue("doc"))
		if err != nil || !found {
			http.NotFound(w, r)
			return
		}
		item, hasItem, _ := a.Store.GetLibraryItemByDocID(r.Context(), doc.ID)
		writeReferenceCard(w, r.PathValue("kb"), doc, item, hasItem)
	})
	mux.HandleFunc("GET /z/{kb}/{doc}", func(w http.ResponseWriter, r *http.Request) {
		a, ok := d.getApp(w, r)
		if !ok {
			return
		}
		item, found, _ := a.Store.GetLibraryItemByDocID(r.Context(), r.PathValue("doc"))
		if !found || item.ItemKey == "" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, zoteroSelectURL(item), http.StatusFound)
	})
	mux.HandleFunc("GET /p/{kb}/{doc}", func(w http.ResponseWriter, r *http.Request) {
		a, ok := d.getApp(w, r)
		if !ok {
			return
		}
		item, found, _ := a.Store.GetLibraryItemByDocID(r.Context(), r.PathValue("doc"))
		if !found || item.AttachmentKey == "" {
			http.NotFound(w, r)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 0 {
			page = 0
		}
		http.Redirect(w, r, zoteroPDFURL(item, page), http.StatusFound)
	})

	// ---- 前端静态资源（放在最后，兜底 "/"）----
	mux.Handle("/", web.Handler())

	return mux
}

// getApp 从路径参数解析知识库并取回 App；失败时已写好响应。
func (d Deps) getApp(w http.ResponseWriter, r *http.Request) (*app.App, bool) {
	id := r.PathValue("kb")
	if id == "" {
		writeError(w, http.StatusBadRequest, errors.New("缺少知识库 ID"))
		return nil, false
	}
	a, err := d.Manager.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return nil, false
	}
	return a, true
}

func clampTopK(a *app.App, k int) int {
	if k <= 0 {
		return a.Cfg.Search.TopK
	}
	if k > a.Cfg.Search.MaxTopK {
		return a.Cfg.Search.MaxTopK
	}
	return k
}

func candidateK(a *app.App, topK int) int {
	if !a.RerankEnabled() {
		return topK
	}
	k := a.Cfg.Search.CandidateK
	if k < topK {
		k = topK
	}
	if k > a.Cfg.Search.MaxCandidates {
		k = a.Cfg.Search.MaxCandidates
	}
	return k
}

// settingsDTO 是前端提交的设置结构。
type settingsDTO struct {
	Server struct {
		Addr       string `json:"addr"`
		MCPEnabled *bool  `json:"mcp_enabled"`
	} `json:"server"`
	Parse    services.ServiceDTO    `json:"parse"`
	Embed    services.ServiceDTO    `json:"embed"`
	Rerank   services.ServiceDTO    `json:"rerank"`
	Citation *config.CitationConfig `json:"citation,omitempty"`
	Zotero   *config.ZoteroConfig   `json:"zotero,omitempty"`
}

func settingsResponse(c config.Config, configPath, mcpURL, version string) map[string]any {
	logPath := ""
	if c.Storage.Dir != "" {
		logPath = filepath.Join(c.Storage.Dir, "larder.log")
	}
	return map[string]any{
		"server": map[string]any{
			"addr":        c.Server.Addr,
			"data_dir":    c.Storage.Dir,
			"config_path": configPath,
			"log_path":    logPath,
			"mcp_path":    c.Server.MCPPath,
			"mcp_url":     mcpURL,
			"mcp_enabled": c.Server.MCPEnabled,
			"version":     version,
		},
		"parse":              services.ToServiceDTO(c.Parse),
		"embed":              services.ToServiceDTO(c.Embed),
		"rerank":             services.ToServiceDTO(c.Rerank),
		"citation":           c.Citation,
		"citation_platforms": config.CitationProfiles,
		"zotero":             c.Zotero,
	}
}

func (dto settingsDTO) applyTo(c config.Config) (config.Config, error) {
	if dto.Server.Addr != "" {
		c.Server.Addr = dto.Server.Addr
	}
	if dto.Server.MCPEnabled != nil {
		c.Server.MCPEnabled = *dto.Server.MCPEnabled
	}
	var err error
	if c.Parse, err = services.ApplyService(dto.Parse, c.Parse); err != nil {
		return c, err
	}
	// 解析设置只暴露地址与密钥：地址为空即内置解析；能识别 mineru/docling/tika
	// 就自动选择；自定义地址在已有外部 provider 时保留，否则默认按 MinerU 协议。
	if p := services.DetectParseProvider(c.Parse.Endpoint.BaseURL); p != "" {
		c.Parse.Provider = p
	} else if c.Parse.Provider == "" || c.Parse.Provider == "plain" {
		c.Parse.Provider = "mineru"
	}
	if c.Embed, err = services.ApplyService(dto.Embed, c.Embed); err != nil {
		return c, err
	}
	if c.Rerank, err = services.ApplyService(dto.Rerank, c.Rerank); err != nil {
		return c, err
	}
	if dto.Citation != nil {
		if dto.Citation.Version == 0 {
			dto.Citation.Version = c.Citation.Version
		}
		c.Citation = *dto.Citation
		config.NormalizeCitation(&c.Citation)
	}
	if dto.Zotero != nil {
		c.Zotero = *dto.Zotero
		config.NormalizeZotero(&c.Zotero)
	}
	if !c.Embed.Enabled {
		return c, errors.New("向量化服务为入库流程的必需环节，不能关闭")
	}
	if c.Embed.Endpoint.BaseURL == "" {
		return c, errors.New("向量服务的 Base URL 不能为空")
	}
	if c.Rerank.Enabled && strings.TrimSpace(c.Rerank.Endpoint.BaseURL) == "" {
		return c, errors.New("重排服务已启用，Base URL 不能为空")
	}
	if c.Parse.Provider != "plain" && strings.TrimSpace(c.Parse.Endpoint.BaseURL) == "" {
		return c, errors.New("解析方式不是 plain，解析服务的 Base URL 不能为空")
	}
	return c, nil
}

// pickFolder 在 Windows 上打开原生文件夹选择对话框，返回用户选择的绝对路径。
func pickFolder(ctx context.Context) (string, error) {
	if runtime.GOOS != "windows" {
		return "", errors.New("当前系统暂不支持原生文件夹选择，请手动填写路径")
	}
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		ps, err = exec.LookPath("powershell")
	}
	if err != nil {
		return "", errors.New("找不到 PowerShell，无法打开文件夹选择器")
	}
	script := `Add-Type -AssemblyName System.Windows.Forms; ` +
		`$owner = New-Object System.Windows.Forms.Form; ` +
		`$owner.TopMost = $true; $owner.ShowInTaskbar = $false; $owner.WindowState = 'Minimized'; ` +
		`$d = New-Object System.Windows.Forms.FolderBrowserDialog; ` +
		`$d.Description = '选择 Zotero 数据目录'; ` +
		`if ($d.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Out.Write($d.SelectedPath) }; ` +
		`$owner.Dispose()`
	out, err := exec.CommandContext(ctx, ps, "-NoLogo", "-NoProfile", "-STA", "-WindowStyle", "Hidden", "-Command", script).Output()
	if err != nil {
		return "", fmt.Errorf("打开文件夹选择器失败: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// isLoopback 判断请求是否来自本机（用于返回明文密钥的管理接口）。
func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

var assetRefRE = regexp.MustCompile(`asset://([A-Za-z0-9._%-]+)`)

// rewriteAssetURLs 把库内 asset:// 图片引用替换成可访问的 HTTP 地址。
func rewriteAssetURLs(kbID, docID, markdown string) string {
	return assetRefRE.ReplaceAllStringFunc(markdown, func(m string) string {
		name := strings.TrimPrefix(m, "asset://")
		return "/api/kbs/" + url.PathEscape(kbID) + "/documents/" + url.PathEscape(docID) + "/assets/" + url.PathEscape(name)
	})
}

// reconstructMarkdown 为旧版本文档用切块重组一份可读的 Markdown。
func reconstructMarkdown(chunks []model.Chunk) string {
	var b strings.Builder
	lastHeading := ""
	for _, c := range chunks {
		if c.Heading != "" && c.Heading != lastHeading {
			b.WriteString("## ")
			b.WriteString(c.Heading)
			b.WriteString("\n\n")
			lastHeading = c.Heading
		}
		b.WriteString(c.Text)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

// decodeJSON 解析请求体并拒绝未知字段。
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

// badgeSVG 生成一张透明画布，右侧是知识库小标。图片在 Cherry 等客户端中
// 会随消息宽度缩放，因此小标始终贴在回答右上角；不依赖客户端渲染 HTML。
func kbMeta(ctx context.Context, a *app.App, key string) string {
	v, _, _ := a.Store.GetMeta(ctx, key)
	return strings.TrimSpace(v)
}

func parseCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return out
}

func citeItemsFromLibrary(items []model.LibraryItem) []cite.Item {
	out := make([]cite.Item, 0, len(items))
	for _, it := range items {
		out = append(out, libsvc.CiteItem(it))
	}
	return out
}

func citationPreviewItem() cite.Item {
	return cite.Item{
		Key:      "citation-preview",
		ItemType: "journalArticle",
		Title:    "组内研究进展",
		Authors:  []string{"张三", "李四", "王五"},
		Year:     2026,
		Journal:  "计算机学报",
		Volume:   "49",
		Issue:    "3",
		Pages:    "1-12",
		DOI:      "10.1000/xyz",
		Language: "zh",
	}
}

func referenceMarkdown(refs []cite.Reference, style string) string {
	if len(refs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## 参考文献\n\n")
	for _, r := range refs {
		if cite.IsNumeric(style) {
			b.WriteString(r.Mark)
			b.WriteString(" ")
		}
		b.WriteString(r.Text)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

func referenceBibTeX(refs []cite.Reference) string {
	var b strings.Builder
	for _, r := range refs {
		if strings.TrimSpace(r.BibTeX) == "" {
			continue
		}
		b.WriteString(strings.TrimSpace(r.BibTeX))
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func zoteroSelectURL(item model.LibraryItem) string {
	if item.LibraryID != "" && item.LibraryID != "0" {
		return "zotero://select/groups/" + item.LibraryID + "/items/" + item.ItemKey
	}
	return "zotero://select/library/items/" + item.ItemKey
}

func zoteroPDFURL(item model.LibraryItem, page int) string {
	if item.LibraryID != "" && item.LibraryID != "0" {
		return fmt.Sprintf("zotero://open-pdf/groups/%s/items/%s?page=%d", item.LibraryID, item.AttachmentKey, page)
	}
	return fmt.Sprintf("zotero://open-pdf/library/items/%s?page=%d", item.AttachmentKey, page)
}

// writeReferenceCard 渲染一个最小的引用详情页，供 MCP 悬浮链接点击后查看。
func writeReferenceCard(w http.ResponseWriter, kbID string, doc model.Document, item model.LibraryItem, hasItem bool) {
	title := firstNonEmptyStr(item.Title, doc.Title)
	if title == "" {
		title = "未命名文献"
	}
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<title>` + html.EscapeString(title) + `</title>`)
	b.WriteString(`<style>
body{margin:0;background:#f5f3fa;color:#262231;font:15px/1.7 -apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",sans-serif}
.wrap{max-width:780px;margin:40px auto;padding:0 20px}
.card{background:#fff;border:1px solid #e7e2f1;border-radius:18px;padding:28px 30px;box-shadow:0 12px 40px rgba(80,60,130,.08)}
h1{font-size:23px;line-height:1.4;margin:0 0 14px}
.meta{color:#635a76;margin:2px 0}
.abstract{margin-top:18px;padding-top:16px;border-top:1px solid #eee9f5;color:#3c354d}
.actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:22px}
.btn{display:inline-block;padding:9px 15px;border-radius:10px;background:#7c6bb8;color:#fff;text-decoration:none;font-size:14px}
.btn.ghost{background:#f0edf8;color:#5c4d8a}
.foot{margin-top:18px;color:#8c849d;font-size:13px}
a{color:#6b58a8}
</style></head><body><div class="wrap"><div class="card">`)
	b.WriteString(`<h1>` + html.EscapeString(title) + `</h1>`)
	if len(item.Authors) > 0 {
		b.WriteString(`<div class="meta"><b>作者：</b>` + html.EscapeString(strings.Join(item.Authors, "; ")) + `</div>`)
	}
	if item.Journal != "" {
		b.WriteString(`<div class="meta"><b>来源：</b>` + html.EscapeString(item.Journal))
		if item.Year > 0 {
			b.WriteString(`, ` + strconv.Itoa(item.Year))
		}
		if item.Volume != "" {
			b.WriteString(`, ` + html.EscapeString(item.Volume))
			if item.Issue != "" {
				b.WriteString(`(` + html.EscapeString(item.Issue) + `)`)
			}
		}
		if item.Pages != "" {
			b.WriteString(`: ` + html.EscapeString(item.Pages))
		}
		b.WriteString(`</div>`)
	}
	if item.DOI != "" {
		b.WriteString(`<div class="meta"><b>DOI：</b><a href="https://doi.org/` + html.EscapeString(item.DOI) + `" target="_blank" rel="noreferrer">` + html.EscapeString(item.DOI) + `</a></div>`)
	}
	if item.URL != "" {
		b.WriteString(`<div class="meta"><b>URL：</b><a href="` + html.EscapeString(item.URL) + `" target="_blank" rel="noreferrer">` + html.EscapeString(item.URL) + `</a></div>`)
	}
	if item.Abstract != "" {
		b.WriteString(`<div class="abstract"><b>摘要</b><br>` + html.EscapeString(item.Abstract) + `</div>`)
	}
	b.WriteString(`<div class="actions">`)
	if hasItem && item.ItemKey != "" {
		b.WriteString(`<a class="btn" href="/z/` + html.EscapeString(kbID) + `/` + html.EscapeString(doc.ID) + `">在 Zotero 中打开</a>`)
	}
	if hasItem && item.AttachmentKey != "" {
		b.WriteString(`<a class="btn ghost" href="/p/` + html.EscapeString(kbID) + `/` + html.EscapeString(doc.ID) + `?page=0">打开 PDF</a>`)
	}
	b.WriteString(`<a class="btn ghost" href="/">返回 Larder</a>`)
	b.WriteString(`</div>`)
	b.WriteString(`<div class="foot">来源：` + html.EscapeString(firstNonEmptyStr(doc.Source, item.AttachmentPath)) + `</div>`)
	b.WriteString(`</div></div></body></html>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, b.String())
}

func (d Deps) record(kind, kb, title, status, detail string, start time.Time, snippets ...activity.Snippet) string {
	if d.Activity == nil {
		return ""
	}
	return d.Activity.Add(activity.Event{
		Kind: kind, KB: kb, Title: title, Status: status, Detail: detail,
		DurationMS: time.Since(start).Milliseconds(), Snippets: snippets,
	})
}

func snippetsFromHits(kbID string, hits []model.SearchHit) []activity.Snippet {
	n := len(hits)
	if n > 12 {
		n = 12
	}
	out := make([]activity.Snippet, 0, n)
	for _, h := range hits[:n] {
		text := activity.CleanText(h.Text)
		if r := []rune(text); len(r) > 1200 {
			text = string(r[:1200]) + "…"
		}
		out = append(out, activity.Snippet{
			KBID: kbID, DocID: h.DocID, Doc: h.DocTitle, Source: h.Source,
			Heading: h.Heading, Text: text, Score: h.Score,
		})
	}
	return out
}

func badgeSVG(name string) string {
	label := "📚 Larder · " + strings.TrimSpace(name)
	if strings.TrimSpace(name) == "" {
		label = "📚 Larder"
	}
	runes := []rune(label)
	if len(runes) > 52 {
		label = string(runes[:51]) + "…"
	}
	textW := 0
	for _, r := range label {
		if r > 0x2e80 {
			textW += 14
		} else {
			textW += 7
		}
	}
	pillW := textW + 26
	if pillW > 560 {
		pillW = 560
	}
	shift := pillW + 8
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="100%%" height="34">
  <rect x="100%%" y="3" width="%d" height="28" rx="14" transform="translate(-%d,0)" fill="#211a2e" fill-opacity="0.94" stroke="#8b7bc8" stroke-opacity="0.55"/>
  <text x="100%%" y="22" text-anchor="end" transform="translate(-20,0)" font-family="Segoe UI, Microsoft YaHei UI, Microsoft YaHei, sans-serif" font-size="13" font-weight="600" fill="#e8ddff">%s</text>
</svg>`, pillW, shift, html.EscapeString(label))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

// writeIngestError 把已落库的失败文档一并返回，前端可刷新列表并显示失败原因。
func writeIngestError(w http.ResponseWriter, res ingest.Result, err error) {
	if res.DocID != "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":    err.Error(),
			"document": res,
			"doc_id":   res.DocID,
		})
		return
	}
	writeError(w, http.StatusBadRequest, err)
}
