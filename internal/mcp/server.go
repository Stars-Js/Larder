// Package mcp 把 Larder 的知识库能力暴露为 MCP 工具，
// 使 Claude Desktop / Claude Code / Cursor 等 MCP 客户端可以直接检索本地知识。
package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"larder/internal/activity"
	"larder/internal/app"
	"larder/internal/config"
	"larder/internal/ingest"
	"larder/internal/kb"
	"larder/internal/model"
	"larder/internal/search"
)

// Server 包装 MCP 服务端与其 HTTP 处理器。
type Server struct {
	mgr      *kb.Manager
	server   *sdkmcp.Server
	handler  http.Handler
	baseURL  string
	activity *activity.Logger
	profile  string

	mu        sync.RWMutex
	defaultKB string
}

// New 构建 MCP 服务端。客户端通过 ?client=<平台> 选择引文格式；
// 未匹配到平台时使用 default 配置。
func New(mgr *kb.Manager, publicURL string, act *activity.Logger) *Server {
	instances := make(map[string]*Server, len(config.CitationProfiles))
	for _, p := range config.CitationProfiles {
		instances[p.ID] = newMCPServer(mgr, publicURL, act, p.ID)
	}
	root := &Server{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		profile := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("client")))
		inst, ok := instances[profile]
		if !ok {
			inst = instances["default"]
		}
		inst.handler.ServeHTTP(w, r)
	})}
	return root
}

func newMCPServer(mgr *kb.Manager, publicURL string, act *activity.Logger, profile string) *Server {
	s := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "larder",
		Version: app.Version,
	}, &sdkmcp.ServerOptions{Instructions: mcpInstructions()})
	srv := &Server{
		mgr: mgr, server: s, baseURL: strings.TrimRight(strings.TrimSpace(publicURL), "/"),
		activity: act, profile: profile,
	}
	registerTools(s, mgr, srv)
	srv.handler = sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return s
	}, &sdkmcp.StreamableHTTPOptions{Stateless: true})
	return srv
}

func mcpInstructions() string {
	return "Larder 是一个多知识库系统。使用 larder_list_kbs 查看可用知识库；" +
		"用户没有指定知识库时，默认检索全部知识库，不要反问；用户明确指定时，用 kb 参数或 larder_use_kb 选定。" +
		"入库/删除等写操作必须指定知识库。" +
		"每次检索回答的第一行必须原样复制检索结果里的 Markdown 右对齐小标（表格形式），" +
		"不要改成 HTML、图片或代码块。" +
		"引用格式以检索结果给出的为准：Markdown 模式保留完整可点击链接和 title；纯文本模式完整保留 [cite:id]（【日志】短链；【字段】…）。" +
		"引用紧跟在对应结论句末；严禁只输出 [cite:id] 或删掉括号里的日志、来源字段。"
}

// Handler 返回可直接挂到 HTTP 路由的处理器。
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) citationProfile(req *sdkmcp.CallToolRequest) string {
	profile := s.profile
	if profile == "default" && req != nil && req.ClientInfo() != nil {
		name := strings.ToLower(req.ClientInfo().Name)
		switch {
		case strings.Contains(name, "zcode"):
			profile = "zcode"
		case strings.Contains(name, "cherry"):
			profile = "cherrystudio"
		case strings.Contains(name, "cursor"):
			profile = "cursor"
		case strings.Contains(name, "dsh"):
			profile = "dsh"
		case strings.Contains(name, "claude") && strings.Contains(name, "code"):
			profile = "claude-code"
		case strings.Contains(name, "claude"):
			profile = "claude-desktop"
		}
	}
	return profile
}

func (s *Server) citationStyle(req *sdkmcp.CallToolRequest) config.CitationStyle {
	return s.mgr.Config().Citation.Style(s.citationProfile(req))
}

// setDefaultKB 记录当前 MCP 会话选择的知识库。
func (s *Server) setDefaultKB(id string) {
	s.mu.Lock()
	s.defaultKB = id
	s.mu.Unlock()
}

// resolveKB 优先使用显式 kb，其次使用 larder_use_kb 选择的默认知识库。
func (s *Server) resolveKB(ctx context.Context, explicit string) (string, *app.App, string, error) {
	id := strings.TrimSpace(explicit)
	if id == "" {
		s.mu.RLock()
		id = s.defaultKB
		s.mu.RUnlock()
	}
	if id == "" {
		return "", nil, "", fmt.Errorf("请先选择知识库：调用 larder_list_kbs 查看，再调用 larder_use_kb 选择；或在 kb 参数中直接指定")
	}
	a, err := s.mgr.Get(id)
	if err != nil {
		return "", nil, "", err
	}
	name, ok, err := a.Store.GetMeta(ctx, "kb_name")
	if err != nil {
		return "", nil, "", err
	}
	if !ok || name == "" {
		name = id
	}
	return id, a, name, nil
}

// target 表示检索目标：单个知识库或全部知识库。
type target struct {
	id   string
	name string
	all  bool
}

// resolveTarget 优先显式 kb，其次默认知识库；两者都没有时返回“全部知识库”。
func (s *Server) resolveTarget(ctx context.Context, explicit string) (target, error) {
	id := strings.TrimSpace(explicit)
	if id == "" {
		s.mu.RLock()
		id = s.defaultKB
		s.mu.RUnlock()
	}
	if id == "" || strings.EqualFold(id, "all") {
		return target{all: true, name: "全部知识库"}, nil
	}
	_, _, name, err := s.resolveKB(ctx, id)
	if err != nil {
		return target{}, err
	}
	return target{id: id, name: name}, nil
}

// --- 工具入参/出参类型 ---

type useKBInput struct {
	KB string `json:"kb" jsonschema:"要选择的知识库 ID（用 larder_list_kbs 查看）；填 all 或留空表示全部知识库"`
}

type searchInput struct {
	KB    string `json:"kb,omitempty" jsonschema:"知识库 ID；留空默认检索全部知识库（除非已用 larder_use_kb 选定）"`
	Query string `json:"query" jsonschema:"检索的自然语言问题或关键词"`
	TopK  int    `json:"top_k,omitempty" jsonschema:"返回条数，默认使用服务端配置"`
}

type citation struct {
	KBName   string  `json:"kb_name,omitempty" jsonschema:"命中所属知识库"`
	CiteID   string  `json:"cite_id" jsonschema:"行内引用标记 [cite:id]；ZCode 下为含日志/来源的完整引用串"`
	CiteLink string  `json:"cite_link" jsonschema:"完整行内引用串（含日志/来源）；回答中必须原样使用"`
	Title    string  `json:"title" jsonschema:"文档标题"`
	Source   string  `json:"source" jsonschema:"来源路径或 URL"`
	Heading  string  `json:"heading,omitempty" jsonschema:"文档内的标题路径"`
	Score    float64 `json:"score" jsonschema:"相关性得分"`
	Text     string  `json:"text" jsonschema:"命中片段的原文"`
}

type searchOutput struct {
	KBID          string     `json:"kb_id" jsonschema:"知识库 ID"`
	KBName        string     `json:"kb_name" jsonschema:"知识库名称"`
	Badge         string     `json:"badge" jsonschema:"知识库名称小标"`
	BadgeMarkdown string     `json:"badge_markdown" jsonschema:"右上角表格小标的 Markdown；原样放在回答第一行"`
	Count         int        `json:"count" jsonschema:"命中数量"`
	Results       []citation `json:"results" jsonschema:"检索结果列表"`
}

type ingestFileInput struct {
	KB      string `json:"kb,omitempty" jsonschema:"知识库 ID；留空则使用 larder_use_kb 选择的默认知识库"`
	Path    string `json:"path" jsonschema:"本地文件的绝对或相对路径"`
	Title   string `json:"title,omitempty" jsonschema:"可选，自定义文档标题"`
	Refresh bool   `json:"refresh,omitempty" jsonschema:"内容未变时是否强制重新入库"`
}

type ingestURLInput struct {
	KB      string `json:"kb,omitempty" jsonschema:"知识库 ID；留空则使用 larder_use_kb 选择的默认知识库"`
	URL     string `json:"url" jsonschema:"要抓取的网页或文件 URL"`
	Title   string `json:"title,omitempty" jsonschema:"可选，自定义文档标题"`
	Refresh bool   `json:"refresh,omitempty" jsonschema:"内容未变时是否强制重新入库"`
}

type ingestOutput struct {
	DocID   string `json:"doc_id" jsonschema:"文档唯一 ID"`
	Title   string `json:"title" jsonschema:"文档标题"`
	Source  string `json:"source" jsonschema:"来源路径或 URL"`
	Chunks  int    `json:"chunks" jsonschema:"生成的切块数量"`
	Skipped bool   `json:"skipped" jsonschema:"是否因内容未变而跳过"`
	Message string `json:"message" jsonschema:"结果说明"`
}

type listInput struct {
	KB string `json:"kb,omitempty" jsonschema:"知识库 ID；留空则使用 larder_use_kb 选择的默认知识库"`
}

// kbListInput 用于列出知识库。
type kbListInput struct{}

type docBrief struct {
	KBName  string `json:"kb_name,omitempty" jsonschema:"所属知识库"`
	DocID   string `json:"doc_id" jsonschema:"文档 ID"`
	Title   string `json:"title" jsonschema:"标题"`
	Source  string `json:"source" jsonschema:"来源"`
	Chunks  int    `json:"chunks" jsonschema:"切块数"`
	Created string `json:"created_at" jsonschema:"入库时间"`
}

type listOutput struct {
	KBID      string     `json:"kb_id" jsonschema:"知识库 ID"`
	KBName    string     `json:"kb_name" jsonschema:"知识库名称"`
	Count     int        `json:"count" jsonschema:"文档总数"`
	Documents []docBrief `json:"documents" jsonschema:"文档列表"`
}

type getDocInput struct {
	KB string `json:"kb,omitempty" jsonschema:"知识库 ID；留空则使用 larder_use_kb 选择的默认知识库"`
	ID string `json:"id" jsonschema:"文档 ID"`
}

type getDocOutput struct {
	KBName  string `json:"kb_name,omitempty" jsonschema:"所属知识库"`
	DocID   string `json:"doc_id" jsonschema:"文档 ID"`
	Title   string `json:"title" jsonschema:"标题"`
	Source  string `json:"source" jsonschema:"来源"`
	MIME    string `json:"mime,omitempty" jsonschema:"MIME 类型"`
	Chunks  int    `json:"chunks" jsonschema:"切块数"`
	Created string `json:"created_at" jsonschema:"入库时间"`
}

type deleteInput struct {
	KB string `json:"kb,omitempty" jsonschema:"知识库 ID；留空则使用 larder_use_kb 选择的默认知识库"`
	ID string `json:"id" jsonschema:"要删除的文档 ID"`
}

type deleteOutput struct {
	DocID   string `json:"doc_id" jsonschema:"文档 ID"`
	Deleted bool   `json:"deleted" jsonschema:"是否删除成功"`
	Message string `json:"message" jsonschema:"结果说明"`
}

// registerTools 把所有工具挂到 MCP 服务端。
func registerTools(s *sdkmcp.Server, mgr *kb.Manager, state *Server) {
	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "larder_use_kb",
		Description: "选择后续工具默认使用的知识库（在当前 MCP 服务进程内生效）。用户没有指定知识库时，先列出并询问，再调用本工具。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in useKBInput) (*sdkmcp.CallToolResult, map[string]any, error) {
		if strings.TrimSpace(in.KB) == "" || strings.EqualFold(strings.TrimSpace(in.KB), "all") {
			state.setDefaultKB("")
			state.record("选择知识库", "全部知识库", "全部知识库", "ok", "all", time.Now())
			msg := "已切换为全部知识库；检索会跨所有知识库合并结果。"
			return textResult(msg), map[string]any{"kb_id": "", "kb_name": "全部知识库", "message": msg}, nil
		}
		id, _, name, err := state.resolveKB(ctx, in.KB)
		if err != nil {
			return errResult(err), map[string]any{}, nil
		}
		state.setDefaultKB(id)
		state.record("选择知识库", name, name, "ok", id, time.Now())
		msg := fmt.Sprintf("已选择知识库：%s（id=%s）。后续工具可以省略 kb 参数。", name, id)
		return textResult(msg), map[string]any{"kb_id": id, "kb_name": name, "message": msg}, nil
	})

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "larder_list_kbs",
		Description: "列出所有知识库及其 ID、名称、文档数；随后用 larder_use_kb 选择要检索的知识库。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in kbListInput) (*sdkmcp.CallToolResult, map[string]any, error) {
		infos, err := mgr.List(ctx)
		if err != nil {
			return errResult(err), map[string]any{}, nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "共 %d 个知识库：\n", len(infos))
		for _, i := range infos {
			fmt.Fprintf(&b, "- id=%s  %s（%d 篇文档）\n", i.ID, i.Name, i.Documents)
		}
		return textResult(b.String()), map[string]any{"count": len(infos), "kbs": infos}, nil
	})

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "larder_search",
		Description: "在知识库中做语义检索，返回带来源引用的相关片段。不指定 kb 时检索全部知识库。" +
			"结果第一行是 Markdown 右对齐表格小标，请原样放在回答最上方；不要用 HTML、图片或代码块。" +
			"回答中的每个引用必须原样复制结果里的完整引用串（cite_link 字段，ZCode 下 cite_id 同样完整），" +
			"包含 [cite:id] 后括号内的【日志】链接和【文档】/【文件】/【章节】/【原文】字段，" +
			"并紧跟在对应结论句末；严禁只输出 [cite:id]，严禁删除、缩写或改写括号内容，严禁把引用集中堆在文末。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in searchInput) (*sdkmcp.CallToolResult, searchOutput, error) {
		tgt, err := state.resolveTarget(ctx, in.KB)
		if err != nil {
			return errResult(err), searchOutput{}, nil
		}
		start := time.Now()
		topK := in.TopK
		if topK <= 0 {
			topK = 8
		}
		if topK > 50 {
			topK = 50
		}
		var hits []model.SearchHit
		reranked := false
		if tgt.all {
			hits, err = state.searchAll(ctx, in.Query, topK)
			if err != nil {
				state.record("MCP 检索", tgt.name, in.Query, "error", err.Error(), start)
				return errResult(err), searchOutput{}, nil
			}
		} else {
			_, a, _, gerr := state.resolveKB(ctx, tgt.id)
			if gerr != nil {
				return errResult(gerr), searchOutput{}, nil
			}
			if topK > a.Cfg.Search.MaxTopK {
				topK = a.Cfg.Search.MaxTopK
			}
			res, serr := a.Search.Search(ctx, in.Query, searchOptions(a, topK))
			if serr != nil {
				state.record("MCP 检索", tgt.name, in.Query, "error", serr.Error(), start)
				return errResult(serr), searchOutput{}, nil
			}
			hits = res.Hits
			reranked = res.Reranked
			for i := range hits {
				hits[i].KBID = tgt.id
				hits[i].KBName = tgt.name
			}
		}
		detail := fmt.Sprintf("%d 条", len(hits))
		if reranked {
			detail += " · 已重排"
		}
		logID := state.record("MCP 检索", tgt.name, in.Query, "ok", detail, start, mcpSnippets(tgt.id, hits)...)
		profile := state.citationProfile(req)
		style := state.mgr.Config().Citation.Style(profile)
		textLimit := citationTextLimit(profile)
		badge := badgeMarkdown(tgt.name)
		out := searchOutput{
			KBID: tgt.id, KBName: tgt.name, Badge: "Larder · " + tgt.name,
			BadgeMarkdown: badge,
			Count:         len(hits), Results: toCitations(hits, state.baseURL, logID, style, profile == "zcode", textLimit),
		}
		return textResult(formatCitations(hits, tgt.name, badge, state.baseURL, logID, style, textLimit)), out, nil
	})

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "larder_ingest_file",
		Description: "把本地文件解析、切块、向量化后加入指定知识库。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in ingestFileInput) (*sdkmcp.CallToolResult, ingestOutput, error) {
		_, a, kbName, err := state.resolveKB(ctx, in.KB)
		if err != nil {
			return errResult(err), ingestOutput{}, nil
		}
		start := time.Now()
		res, err := a.Ingest.IngestFile(ctx, in.Path, ingest.Options{Title: in.Title, Refresh: in.Refresh})
		if err != nil {
			state.record("MCP 入库", kbName, firstOf(in.Title, in.Path), "error", err.Error(), start)
			return errResult(err), ingestOutput{}, nil
		}
		out := toIngestOutput(res)
		state.record("MCP 入库", kbName, out.Title, "ok", fmt.Sprintf("%d 个切块", out.Chunks), start)
		return textResult(out.Message), out, nil
	})

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "larder_ingest_url",
		Description: "抓取网页或在线文件，解析后加入指定知识库。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in ingestURLInput) (*sdkmcp.CallToolResult, ingestOutput, error) {
		_, a, kbName, err := state.resolveKB(ctx, in.KB)
		if err != nil {
			return errResult(err), ingestOutput{}, nil
		}
		start := time.Now()
		res, err := a.Ingest.IngestURL(ctx, in.URL, ingest.Options{Title: in.Title, Refresh: in.Refresh})
		if err != nil {
			state.record("MCP 入库", kbName, firstOf(in.Title, in.URL), "error", err.Error(), start)
			return errResult(err), ingestOutput{}, nil
		}
		out := toIngestOutput(res)
		state.record("MCP 入库", kbName, out.Title, "ok", fmt.Sprintf("%d 个切块", out.Chunks), start)
		return textResult(out.Message), out, nil
	})

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "larder_list_documents",
		Description: "列出知识库中的文档；不指定 kb 时列出全部知识库的文档。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in listInput) (*sdkmcp.CallToolResult, listOutput, error) {
		tgt, err := state.resolveTarget(ctx, in.KB)
		if err != nil {
			return errResult(err), listOutput{}, nil
		}
		out := listOutput{KBID: tgt.id, KBName: tgt.name, Documents: make([]docBrief, 0)}
		var b strings.Builder
		if tgt.all {
			infos, lerr := state.mgr.List(ctx)
			if lerr != nil {
				return errResult(lerr), listOutput{}, nil
			}
			for _, info := range infos {
				a, gerr := state.mgr.Get(info.ID)
				if gerr != nil {
					continue
				}
				docs, derr := a.Store.ListDocuments(ctx)
				if derr != nil {
					continue
				}
				for _, d := range docs {
					out.Documents = append(out.Documents, docBrief{
						KBName: info.Name, DocID: d.ID, Title: d.Title, Source: d.Source,
						Chunks: d.Chunks, Created: d.CreatedAt.Format("2006-01-02 15:04"),
					})
				}
			}
			out.Count = len(out.Documents)
			fmt.Fprintf(&b, "全部知识库共 %d 篇文档：\n", out.Count)
			for _, d := range out.Documents {
				fmt.Fprintf(&b, "- [%s] %s / %s (%d 块) — %s\n", short(d.DocID), d.KBName, d.Title, d.Chunks, d.Source)
			}
			return textResult(b.String()), out, nil
		}
		a, gerr := state.mgr.Get(tgt.id)
		if gerr != nil {
			return errResult(gerr), listOutput{}, nil
		}
		docs, derr := a.Store.ListDocuments(ctx)
		if derr != nil {
			return errResult(derr), listOutput{}, nil
		}
		for _, d := range docs {
			out.Documents = append(out.Documents, docBrief{
				KBName: tgt.name, DocID: d.ID, Title: d.Title, Source: d.Source,
				Chunks: d.Chunks, Created: d.CreatedAt.Format("2006-01-02 15:04"),
			})
		}
		out.Count = len(out.Documents)
		fmt.Fprintf(&b, "共 %d 篇文档：\n", out.Count)
		for _, d := range out.Documents {
			fmt.Fprintf(&b, "- [%s] %s (%d 块) — %s\n", short(d.DocID), d.Title, d.Chunks, d.Source)
		}
		return textResult(b.String()), out, nil
	})

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "larder_get_document",
		Description: "查看指定知识库中某篇文档的元信息。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in getDocInput) (*sdkmcp.CallToolResult, getDocOutput, error) {
		tgt, err := state.resolveTarget(ctx, in.KB)
		if err != nil {
			return errResult(err), getDocOutput{}, nil
		}
		var a *app.App
		var doc model.Document
		var ok bool
		kbName := tgt.name
		if tgt.all {
			a, kbName, doc, ok, err = state.findDocumentAll(ctx, in.ID)
		} else {
			a, err = state.mgr.Get(tgt.id)
			if err == nil {
				doc, ok, err = a.Store.GetDocument(ctx, in.ID)
			}
		}
		if err != nil {
			return errResult(err), getDocOutput{}, nil
		}
		if !ok {
			return errResult(fmt.Errorf("未找到文档 %s", in.ID)), getDocOutput{}, nil
		}
		_ = a
		out := getDocOutput{
			KBName: kbName, DocID: doc.ID, Title: doc.Title, Source: doc.Source, MIME: doc.MIME,
			Chunks: doc.Chunks, Created: doc.CreatedAt.Format("2006-01-02 15:04"),
		}
		return textResult(fmt.Sprintf("知识库：%s\n%s\n来源：%s\n切块：%d\n入库：%s", kbName, doc.Title, doc.Source, doc.Chunks, out.Created)), out, nil
	})

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "larder_delete_document",
		Description: "从指定知识库删除某篇文档及其全部切块与向量。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in deleteInput) (*sdkmcp.CallToolResult, deleteOutput, error) {
		if in.ID == "" {
			return errResult(fmt.Errorf("id 不能为空")), deleteOutput{}, nil
		}
		tgt, err := state.resolveTarget(ctx, in.KB)
		if err != nil {
			return errResult(err), deleteOutput{}, nil
		}
		var a *app.App
		kbName := tgt.name
		if tgt.all {
			a, kbName, _, _, err = state.findDocumentAll(ctx, in.ID)
		} else {
			a, err = state.mgr.Get(tgt.id)
		}
		if err != nil {
			return errResult(err), deleteOutput{}, nil
		}
		if a == nil {
			return errResult(fmt.Errorf("未找到文档 %s", in.ID)), deleteOutput{}, nil
		}
		start := time.Now()
		if err := a.Store.DeleteDocument(ctx, in.ID); err != nil {
			state.record("MCP 删除", kbName, in.ID, "error", err.Error(), start)
			return errResult(err), deleteOutput{}, nil
		}
		state.record("MCP 删除", kbName, in.ID, "ok", "", start)
		out := deleteOutput{DocID: in.ID, Deleted: true, Message: "已删除文档 " + in.ID}
		return textResult(out.Message), out, nil
	})
}

func (s *Server) record(kind, kb, title, status, detail string, start time.Time, snippets ...activity.Snippet) string {
	if s.activity == nil {
		return ""
	}
	return s.activity.Add(activity.Event{
		Kind: kind, KB: kb, Title: title, Status: status, Detail: detail,
		DurationMS: time.Since(start).Milliseconds(), Snippets: snippets,
	})
}

func mcpSnippets(kbID string, hits []model.SearchHit) []activity.Snippet {
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
		kid := h.KBID
		if kid == "" {
			kid = kbID
		}
		out = append(out, activity.Snippet{
			KBID: kid, DocID: h.DocID, Doc: h.DocTitle, Source: h.Source,
			Heading: h.Heading, Text: text, Score: h.Score,
		})
	}
	return out
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (s *Server) findDocumentAll(ctx context.Context, id string) (*app.App, string, model.Document, bool, error) {
	infos, err := s.mgr.List(ctx)
	if err != nil {
		return nil, "", model.Document{}, false, err
	}
	for _, info := range infos {
		a, err := s.mgr.Get(info.ID)
		if err != nil {
			continue
		}
		doc, ok, err := a.Store.GetDocument(ctx, id)
		if err != nil {
			return nil, "", model.Document{}, false, err
		}
		if ok {
			return a, info.Name, doc, true, nil
		}
	}
	return nil, "", model.Document{}, false, nil
}

func (s *Server) searchAll(ctx context.Context, query string, topK int) ([]model.SearchHit, error) {
	infos, err := s.mgr.List(ctx)
	if err != nil {
		return nil, err
	}
	var hits []model.SearchHit
	var lastErr error
	okCount := 0
	for _, info := range infos {
		a, err := s.mgr.Get(info.ID)
		if err != nil {
			lastErr = err
			continue
		}
		res, err := a.Search.Search(ctx, query, searchOptions(a, topK))
		if err != nil {
			lastErr = err
			continue
		}
		okCount++
		name := info.Name
		if name == "" {
			name = info.ID
		}
		for _, h := range res.Hits {
			h.KBID = info.ID
			h.KBName = name
			hits = append(hits, h)
		}
	}
	if okCount == 0 && lastErr != nil {
		return nil, lastErr
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > topK {
		hits = hits[:topK]
	}
	return hits, nil
}

func searchOptions(a *app.App, topK int) search.Options {
	cand := a.Cfg.Search.CandidateK
	if !a.RerankEnabled() {
		// 无重排时，多取一些再截断即可。
		cand = topK
	}
	return search.Options{TopK: topK, CandidateK: cand}
}

func toCitations(hits []model.SearchHit, baseURL, logID string, style config.CitationStyle, fullToken bool, textLimit int) []citation {
	out := make([]citation, len(hits))
	base := appBase(baseURL)
	for i, h := range hits {
		id := citeID(h)
		link := logLink(base, logID)
		source := citationSource(h, style.Fields, textLimit)
		showCite := hasField(style.Fields, "cite")
		showLog := hasField(style.Fields, "log")
		md := citationText(id, source, link, showCite, showLog, false)
		if style.Mode == "markdown" && showCite && showLog {
			md = citationMarkdown(id, link, source)
		} else if style.Mode == "markdown" {
			md = citationText(id, source, link, showCite, showLog, false)
		}
		// ZCode 只用 cite_id 字段拼引用，若这里仍是裸 [cite:id]，模型会丢掉括号。
		// 因此对 ZCode 直接给出完整引用串，让 cite_id 与 cite_link 都可原样复制。
		citeToken := id
		if fullToken {
			citeToken = md
		}
		out[i] = citation{
			KBName: h.KBName, CiteID: citeToken, CiteLink: md,
			Title: h.DocTitle, Source: h.Source, Heading: h.Heading,
			Score: round(h.Score), Text: h.Text,
		}
	}
	return out
}

func appBase(publicURL string) string {
	base := strings.TrimSpace(publicURL)
	if u, err := url.Parse(base); err == nil && u.Scheme != "" {
		u.Path = ""
		u.RawQuery = ""
		u.Fragment = ""
		base = strings.TrimRight(u.String(), "/")
	}
	if base == "" {
		base = "http://127.0.0.1:8765"
	}
	return base
}

func logLink(base, eventID string) string {
	base = appBase(base)
	if strings.TrimSpace(eventID) == "" {
		return base + "/#/logs"
	}
	return base + "/c/" + url.PathEscape(eventID)
}

func citeID(h model.SearchHit) string {
	return "cite:" + short(h.DocID) + "-" + strconv.FormatInt(h.ChunkID, 10)
}

func hasField(fields []string, want string) bool {
	for _, f := range fields {
		if f == want {
			return true
		}
	}
	return false
}

func citationTextLimit(profile string) int {
	// ZCode 会自行缩写长引用，原文只保留便于定位的长度；其他客户端维持原样。
	if profile == "zcode" {
		return 120
	}
	return 220
}

func citationSource(h model.SearchHit, fields []string, textLimit int) string {
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		switch f {
		case "doc":
			if h.DocTitle != "" {
				parts = append(parts, "【文档】"+oneLine(h.DocTitle))
			}
		case "file":
			if h.Source != "" {
				parts = append(parts, "【文件】"+oneLine(h.Source))
			}
		case "heading":
			heading := oneLine(h.Heading)
			if heading == "" {
				heading = "—"
			}
			parts = append(parts, "【章节】"+heading)
		case "text":
			text := oneLine(activity.CleanText(h.Text))
			if r := []rune(text); len(r) > textLimit {
				text = string(r[:textLimit]) + "…"
			}
			if text != "" {
				parts = append(parts, "【原文】"+text)
			}
		}
	}
	return strings.Join(parts, "")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func citationMarkdown(id, link, source string) string {
	if source == "" {
		return fmt.Sprintf("[%s](%s)", "["+id+"]", link)
	}
	return fmt.Sprintf("[%s](%s \"%s\")", "["+id+"]", link, markdownTitle(source))
}

func citationText(id, source, link string, showCite, showLog, doubleBracket bool) string {
	body := make([]string, 0, 2)
	if showLog {
		body = append(body, "【日志】"+link)
	}
	if source != "" {
		body = append(body, source)
	}
	prefix := ""
	if showCite {
		if doubleBracket {
			prefix = "[[" + id + "]]"
		} else {
			prefix = "[" + id + "]"
		}
	}
	if len(body) == 0 {
		return prefix
	}
	return prefix + "（" + strings.Join(body, "；") + "）"
}

func markdownTitle(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 {
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return strings.ReplaceAll(b.String(), "\"", "'")
}

func badgeMarkdown(kbName string) string {
	kbName = strings.TrimSpace(kbName)
	if kbName == "" {
		kbName = "全部知识库"
	}
	kbName = strings.ReplaceAll(kbName, "|", `\|`)
	return fmt.Sprintf(`|  | **📚 Larder · %s** |
|:--|--:|
`, kbName)
}

func formatCitations(hits []model.SearchHit, kbName, badge, publicURL, logID string, style config.CitationStyle, textLimit int) string {
	if kbName == "" {
		kbName = "Larder"
	}
	prefix := fmt.Sprintf("【Larder · %s】", kbName)
	if badge != "" {
		prefix = badge
	}
	if len(hits) == 0 {
		return prefix + "\n\n未检索到相关内容。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n找到 %d 条相关片段：\n", prefix, len(hits))
	showCite := hasField(style.Fields, "cite")
	showLog := hasField(style.Fields, "log")
	for i, h := range hits {
		fmt.Fprintf(&b, "\n[%d] ", i+1)
		if h.KBName != "" {
			fmt.Fprintf(&b, "知识库：%s · ", h.KBName)
		}
		fmt.Fprintf(&b, "文档：%s\n", h.DocTitle)
		if h.Source != "" && h.Source != h.DocTitle {
			fmt.Fprintf(&b, "文件：%s\n", h.Source)
		}
		if h.Heading != "" {
			fmt.Fprintf(&b, "章节：%s\n", h.Heading)
		}
		id := citeID(h)
		link := logLink(appBase(publicURL), logID)
		source := citationSource(h, style.Fields, textLimit)
		if style.Mode == "markdown" && showCite && showLog {
			fmt.Fprintf(&b, "引用：%s\n", citationMarkdown(id, link, source))
		} else {
			fmt.Fprintf(&b, "引用：%s\n", citationText(id, source, link, showCite, showLog, false))
		}
		fmt.Fprintf(&b, "\n%s\n", h.Text)
	}
	fields := "[cite:id] 后括号内的来源字段"
	if showLog {
		fields = "[cite:id] 后括号内的【日志】链接与来源字段"
	}
	fmt.Fprintf(&b, "\n【引用要求】回答中的每个引用必须逐字复制上面「引用：」后的完整内容，"+
		"包括 %s，紧跟在对应结论句末；"+
		"严禁只输出 [cite:id]，严禁删除或缩写括号内容，严禁集中堆在文末。\n", fields)
	return b.String()
}

func toIngestOutput(r ingest.Result) ingestOutput {
	msg := fmt.Sprintf("已入库《%s》，生成 %d 个切块（doc_id=%s）", r.Title, r.Chunks, r.DocID)
	if r.Skipped {
		msg = fmt.Sprintf("《%s》内容未变化，已跳过（doc_id=%s）", r.Title, r.DocID)
	}
	return ingestOutput{
		DocID: r.DocID, Title: r.Title, Source: r.Source,
		Chunks: r.Chunks, Skipped: r.Skipped, Message: msg,
	}
}

func textResult(text string) *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text}},
	}
}

func errResult(err error) *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{
		IsError: true,
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "错误：" + err.Error()}},
	}
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func round(f float64) float64 {
	// 保留 4 位小数，避免输出过长。
	return float64(int64(f*10000+0.5)) / 10000
}
