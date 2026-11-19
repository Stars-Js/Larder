// Package mcp 把 Larder 的知识库能力暴露为 MCP 工具，
// 使 Claude Desktop / Claude Code / Cursor 等 MCP 客户端可以直接检索本地知识。
package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"larder/internal/activity"
	"larder/internal/app"
	"larder/internal/cite"
	"larder/internal/config"
	"larder/internal/ingest"
	"larder/internal/kb"
	libsvc "larder/internal/library"
	"larder/internal/model"
	"larder/internal/search"
)

// Server 包装 MCP 服务端与其 HTTP 处理器。
type Server struct {
	mgr      *kb.Manager
	library  *libsvc.Service
	server   *sdkmcp.Server
	handler  http.Handler
	baseURL  string
	activity *activity.Logger
	profile  string

	mu         sync.RWMutex
	defaultKB  string
	selections map[string]string // MCP session ID -> KB ID
}

// New 构建 MCP 服务端。客户端通过 ?client=<平台> 选择引文格式；
// 未匹配到平台时使用 default 配置。lib 可为 nil（仅测试/最小模式）。
func New(mgr *kb.Manager, lib *libsvc.Service, publicURL string, act *activity.Logger) *Server {
	instances := make(map[string]*Server, len(config.CitationProfiles))
	for _, p := range config.CitationProfiles {
		instances[p.ID] = newMCPServer(mgr, lib, publicURL, act, p.ID)
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

func newMCPServer(mgr *kb.Manager, lib *libsvc.Service, publicURL string, act *activity.Logger, profile string) *Server {
	s := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "larder",
		Version: app.Version,
	}, &sdkmcp.ServerOptions{Instructions: mcpInstructions()})
	srv := &Server{
		mgr: mgr, library: lib, server: s, baseURL: strings.TrimRight(strings.TrimSpace(publicURL), "/"),
		activity: act, profile: profile, selections: map[string]string{},
	}
	registerTools(s, mgr, srv)
	srv.handler = sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return s
	}, &sdkmcp.StreamableHTTPOptions{SessionTimeout: 30 * time.Minute})
	return srv
}

func mcpInstructions() string {
	return "Larder 是一个多知识库系统。使用 larder_list_libraries 查看可用文献库；" +
		"首次检索未指定 kb 且有多个库时，支持 MCP elicitation 的客户端会自动弹出文献库选项；" +
		"如果不支持，larder_search 会返回 selection_required=true，此时把 libraries 选项展示给用户，用户选择后调用 larder_select_library，后续检索带上 kb。" +
		"入库/删除等写操作必须指定知识库。" +
		"每次检索回答的第一行必须原样复制检索结果里的 Markdown 右对齐小标（表格形式），" +
		"不要改成 HTML、图片或代码块。" +
		"引用格式以检索结果给出的为准：论文级引用原样使用 results[].cite_link（悬浮模式是可点击链接，纯文本模式是 [1]（著者-年份样式为 (作者, 年)））；" +
		"只需要纯标记时用 results[].ref_mark，文末参考文献直接复制 reference_block；" +
		"需要更多格式时调用 larder_get_references。引用紧跟在对应结论句末，严禁编造参考文献。"
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

// sessionKey 返回当前 MCP 会话标识。无会话时退化为进程级默认值。
func (s *Server) sessionKey(req *sdkmcp.CallToolRequest) string {
	if req != nil && req.Session != nil {
		if id := strings.TrimSpace(req.Session.ID()); id != "" {
			return id
		}
	}
	return "_global"
}

// setDefaultKB 记录某个 MCP 会话选择的知识库。
func (s *Server) setDefaultKB(session, id string) {
	s.mu.Lock()
	if session == "" {
		session = "_global"
	}
	if s.selections == nil {
		s.selections = map[string]string{}
	}
	if id == "" {
		delete(s.selections, session)
	} else {
		s.selections[session] = id
	}
	// 保留旧的全局字段，兼容没有 session 的客户端。
	s.defaultKB = id
	s.mu.Unlock()
}

// defaultKBFor 返回某个 MCP 会话选择的知识库。
func (s *Server) defaultKBFor(session string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if session != "" {
		if id := s.selections[session]; id != "" {
			return id
		}
	}
	return s.defaultKB
}

// resolveKB 优先使用显式 kb，其次使用当前会话选择的知识库。
func (s *Server) resolveKB(ctx context.Context, explicit, session string) (string, *app.App, string, error) {
	id := strings.TrimSpace(explicit)
	if id == "" {
		id = s.defaultKBFor(session)
	}
	if id == "" {
		return "", nil, "", fmt.Errorf("请先选择文献库：调用 larder_list_libraries 查看，再调用 larder_select_library；或在 kb 参数中直接指定")
	}
	if strings.EqualFold(id, "all") {
		return "", nil, "", fmt.Errorf("该操作需要指定具体知识库，不能使用 kb=all")
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

// resolveTarget 优先显式 kb，其次会话选择；显式 all 表示全部知识库。
func (s *Server) resolveTarget(ctx context.Context, explicit, session string) (target, error) {
	id := strings.TrimSpace(explicit)
	if id == "" {
		id = s.defaultKBFor(session)
	}
	if strings.EqualFold(id, "all") {
		return target{all: true, name: "全部知识库"}, nil
	}
	if id == "" {
		return target{}, fmt.Errorf("请先选择文献库：调用 larder_list_libraries 查看，再调用 larder_select_library；或在 kb 参数中直接指定")
	}
	_, _, name, err := s.resolveKB(ctx, id, session)
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
	KB    string `json:"kb,omitempty" jsonschema:"文献库 ID；留空且存在多个库时返回 selection_required，单库时自动选择；显式填 all 表示全部"`
	Query string `json:"query" jsonschema:"检索的自然语言问题或关键词"`
	TopK  int    `json:"top_k,omitempty" jsonschema:"返回条数，默认使用服务端配置"`
}

type citation struct {
	KBName    string  `json:"kb_name,omitempty" jsonschema:"命中所属知识库"`
	DocID     string  `json:"doc_id,omitempty" jsonschema:"文档 ID"`
	CiteID    string  `json:"cite_id" jsonschema:"行内引用标记 [cite:id]；ZCode 下为含日志/来源的完整引用串"`
	CiteLink  string  `json:"cite_link" jsonschema:"完整行内引用串；回答中必须原样使用"`
	RefNo     int     `json:"ref_no,omitempty" jsonschema:"论文级参考文献编号"`
	RefMark   string  `json:"ref_mark,omitempty" jsonschema:"论文级正文引用标记，如 [1]"`
	Reference string  `json:"reference,omitempty" jsonschema:"对应的参考文献条目"`
	Title     string  `json:"title" jsonschema:"文档标题"`
	Source    string  `json:"source" jsonschema:"来源路径或 URL"`
	Heading   string  `json:"heading,omitempty" jsonschema:"文档内的标题路径"`
	Score     float64 `json:"score" jsonschema:"相关性得分"`
	Text      string  `json:"text" jsonschema:"命中片段的原文"`
}

type searchOutput struct {
	KBID              string          `json:"kb_id" jsonschema:"知识库 ID"`
	KBName            string          `json:"kb_name" jsonschema:"知识库名称"`
	Badge             string          `json:"badge" jsonschema:"知识库名称小标"`
	BadgeMarkdown     string          `json:"badge_markdown" jsonschema:"右上角表格小标的 Markdown；原样放在回答第一行"`
	Count             int             `json:"count" jsonschema:"命中数量"`
	Results           []citation      `json:"results" jsonschema:"检索结果列表"`
	References        []reference     `json:"references,omitempty" jsonschema:"论文级参考文献（去重后）"`
	ReferenceBlock    string          `json:"reference_block,omitempty" jsonschema:"可直接复制到回答末尾的参考文献 Markdown"`
	SelectionRequired bool            `json:"selection_required,omitempty" jsonschema:"true 时必须先让用户选择文献库"`
	Libraries         []libraryOption `json:"libraries,omitempty" jsonschema:"可选文献库列表"`
	Message           string          `json:"message,omitempty" jsonschema:"提示信息"`
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

// libraryOption 是首次检索时展示给用户的文献库选项。
type libraryOption struct {
	ID         string `json:"id" jsonschema:"文献库 ID"`
	Name       string `json:"name" jsonschema:"文献库名称"`
	Kind       string `json:"kind,omitempty" jsonschema:"manual 或 zotero"`
	Documents  int    `json:"documents" jsonschema:"文档数"`
	Collection string `json:"collection,omitempty" jsonschema:"Zotero 集合名"`
	LastSync   string `json:"last_sync,omitempty" jsonschema:"最近同步时间"`
}

const (
	libraryElicitKey = "library"
	// elicitationMinProtocol 是支持 MRTR/InputRequests 的协议版本。
	// 更低版本走 server->client 的 elicitation/create；没有 elicitation
	// 能力的客户端回退为文本列表。
	elicitationMinProtocol = "2026-07-28"
)

// librarySelectionElicit 构造文献库选择表单。支持 MRTR 或旧协议
// elicitation/create 的客户端会把它渲染成可点击选项。
func librarySelectionElicit(opts []libraryOption) *sdkmcp.ElicitParams {
	ids := make([]any, 0, len(opts)+1)
	names := make([]any, 0, len(opts)+1)
	for _, o := range opts {
		label := fmt.Sprintf("%s（%d 篇文档）", firstOf(o.Name, o.ID), o.Documents)
		if o.Kind == "zotero" && o.Collection != "" {
			label += " · Zotero 集合「" + o.Collection + "」"
		}
		ids = append(ids, o.ID)
		names = append(names, label)
	}
	ids = append(ids, "all")
	names = append(names, "全部文献库（跨库检索）")
	return &sdkmcp.ElicitParams{
		Mode:    "form",
		Message: "请选择要使用的文献库",
		RequestedSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				libraryElicitKey: map[string]any{
					"type":        "string",
					"title":       "文献库",
					"description": "选择一个文献库开始检索",
					"enum":        ids,
					"enumNames":   names,
				},
			},
			"required": []string{libraryElicitKey},
		},
	}
}

// elicitedLibrary 读取 MRTR 重试携带的文献库选择。
// answered=false 表示尚未询问；answered=true 且 id 为空表示用户取消/拒绝。
func elicitedLibrary(req *sdkmcp.CallToolRequest) (id string, answered bool) {
	if req == nil || req.Params == nil || len(req.Params.InputResponses) == 0 {
		return "", false
	}
	raw, ok := req.Params.InputResponses[libraryElicitKey]
	if !ok {
		return "", false
	}
	res, ok := raw.(*sdkmcp.ElicitResult)
	if !ok || res == nil {
		return "", true
	}
	if res.Action != "accept" {
		return "", true
	}
	v, _ := res.Content[libraryElicitKey].(string)
	return strings.TrimSpace(v), true
}

// supportsMRTR 判断客户端协议是否支持 InputRequests（多轮请求）。
func supportsMRTR(req *sdkmcp.CallToolRequest) bool {
	if req == nil || req.Session == nil {
		return false
	}
	ip := req.Session.InitializeParams()
	if ip == nil {
		// 无初始化参数时按最新协议处理。
		return true
	}
	return ip.ProtocolVersion >= elicitationMinProtocol
}

// supportsDirectElicitation 判断旧协议客户端是否声明了 elicitation 能力。
// 这类客户端（如 Cherry Studio）通过 server->client 的 elicitation/create
// 弹窗；新协议客户端必须走 InputRequests/MRTR。
func supportsDirectElicitation(req *sdkmcp.CallToolRequest) bool {
	if req == nil || req.Session == nil {
		return false
	}
	ip := req.Session.InitializeParams()
	if ip == nil || ip.Capabilities == nil || ip.Capabilities.Elicitation == nil {
		return false
	}
	return ip.ProtocolVersion < elicitationMinProtocol
}

type libraryChoice struct {
	ID       string
	Answered bool
	Input    *sdkmcp.CallToolResult
}

// requestLibraryChoice 统一处理三种选择路径：
//   - MRTR 客户端：返回 InputRequests，由客户端弹窗后重试；
//   - 旧协议但声明 elicitation 的客户端：直接发送 elicitation/create；
//   - 不支持 elicitation：返回零值，调用方回退为文本列表。
func requestLibraryChoice(ctx context.Context, req *sdkmcp.CallToolRequest, opts []libraryOption) libraryChoice {
	if id, answered := elicitedLibrary(req); answered {
		return libraryChoice{ID: id, Answered: true}
	}
	params := librarySelectionElicit(opts)
	if supportsMRTR(req) {
		return libraryChoice{Input: &sdkmcp.CallToolResult{
			InputRequests: sdkmcp.InputRequestMap{libraryElicitKey: params},
		}}
	}
	if supportsDirectElicitation(req) {
		res, err := req.Session.Elicit(ctx, params)
		if err != nil {
			return libraryChoice{}
		}
		if res == nil || res.Action != "accept" {
			return libraryChoice{Answered: true}
		}
		id, _ := res.Content[libraryElicitKey].(string)
		return libraryChoice{ID: strings.TrimSpace(id), Answered: true}
	}
	return libraryChoice{}
}

func libraryListText(opts []libraryOption, intro string) string {
	var b strings.Builder
	b.WriteString("【Larder · 请选择文献库】\n\n")
	b.WriteString(intro)
	b.WriteString("\n\n")
	for _, o := range opts {
		fmt.Fprintf(&b, "- %s（id=%s，%d 篇文档）\n", o.Name, o.ID, o.Documents)
	}
	if len(opts) == 0 {
		b.WriteString("当前还没有文献库。请先在「文献库」页创建。\n")
	}
	return b.String()
}

// reference 是论文级引用信息。
type reference struct {
	RefNo     int    `json:"ref_no" jsonschema:"参考文献编号"`
	DocID     string `json:"doc_id" jsonschema:"Larder 文档 ID"`
	ItemKey   string `json:"item_key,omitempty" jsonschema:"Zotero item key"`
	Title     string `json:"title" jsonschema:"标题"`
	Authors   string `json:"authors,omitempty" jsonschema:"作者"`
	Year      int    `json:"year,omitempty" jsonschema:"年份"`
	Mark      string `json:"cite_mark" jsonschema:"正文引用标记，如 [1]"`
	CiteLink  string `json:"cite_link" jsonschema:"行内引用：悬浮模式是可点击链接，纯文本模式是文内标记"`
	Text      string `json:"reference" jsonschema:"格式化后的参考文献条目"`
	BibTeX    string `json:"bibtex,omitempty" jsonschema:"BibTeX 条目"`
	ZoteroURL string `json:"zotero_url,omitempty" jsonschema:"zotero:// 深链"`
	PDFURL    string `json:"pdf_url,omitempty" jsonschema:"zotero://open-pdf 深链"`
}

type refsInput struct {
	KB       string `json:"kb,omitempty" jsonschema:"文献库 ID；留空使用当前选择"`
	DocIDs   string `json:"doc_ids,omitempty" jsonschema:"逗号分隔的 Larder 文档 ID；留空表示全部文献"`
	ItemKeys string `json:"item_keys,omitempty" jsonschema:"逗号分隔的 Zotero item key；留空表示全部文献"`
	Style    string `json:"style,omitempty" jsonschema:"引用样式：gb-t-7714-numeric / gb-t-7714-author-date / apa / ieee / vancouver / mla；文内标记由样式决定"`
}

type refsOutput struct {
	KBID       string      `json:"kb_id"`
	KBName     string      `json:"kb_name"`
	Style      string      `json:"style"`
	Count      int         `json:"count"`
	References []reference `json:"references"`
	Markdown   string      `json:"markdown" jsonschema:"可直接放到回答末尾的参考文献 Markdown"`
	BibTeX     string      `json:"bibtex" jsonschema:"BibTeX 文本"`
}

type exportInput struct {
	KB       string `json:"kb,omitempty" jsonschema:"文献库 ID；留空使用当前选择"`
	DocIDs   string `json:"doc_ids,omitempty" jsonschema:"逗号分隔的 Larder 文档 ID；留空表示全部文献"`
	ItemKeys string `json:"item_keys,omitempty" jsonschema:"逗号分隔的 Zotero item key；留空表示全部文献"`
	Style    string `json:"style,omitempty" jsonschema:"引用样式；默认使用文献库设置"`
	FileName string `json:"file_name,omitempty" jsonschema:"输出文件名，默认 references.bib"`
}

type exportOutput struct {
	Path    string `json:"path" jsonschema:"生成的 .bib 文件绝对路径"`
	Count   int    `json:"count"`
	Content string `json:"content" jsonschema:"BibTeX 内容"`
	Message string `json:"message"`
}

type syncInput struct {
	KB    string `json:"kb,omitempty" jsonschema:"Zotero 文献库 ID；留空使用当前选择"`
	Force bool   `json:"force,omitempty" jsonschema:"是否忽略 dateModified 强制重新解析"`
}

type syncOutput struct {
	KBID    string `json:"kb_id"`
	State   string `json:"state"`
	Total   int    `json:"total"`
	Done    int    `json:"done"`
	Failed  int    `json:"failed"`
	Skipped int    `json:"skipped"`
	Message string `json:"message,omitempty"`
}

// registerTools 把所有工具挂到 MCP 服务端。
func registerTools(s *sdkmcp.Server, mgr *kb.Manager, state *Server) {
	// listLibraryOptions 把知识库转换为带 kind/集合信息的选项。
	listLibraryOptions := func(ctx context.Context) ([]libraryOption, error) {
		infos, err := mgr.List(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]libraryOption, 0, len(infos))
		for _, i := range infos {
			out = append(out, libraryOption{
				ID: i.ID, Name: i.Name, Kind: i.Kind, Documents: i.Documents,
				Collection: i.ZoteroCollection, LastSync: i.LastSync,
			})
		}
		return out, nil
	}
	// selectKB 是 larder_use_kb / larder_select_library 的共同实现。
	// emptyAsAll=true 时留空表示全部（兼容旧 larder_use_kb）；
	// false 时留空返回可选项，供 agent 主动触发用户选择。
	selectKB := func(ctx context.Context, req *sdkmcp.CallToolRequest, id string, emptyAsAll bool) (*sdkmcp.CallToolResult, map[string]any, error) {
		session := state.sessionKey(req)
		trimmed := strings.TrimSpace(id)
		if trimmed == "" && !emptyAsAll {
			opts, err := listLibraryOptions(ctx)
			if err != nil {
				return errResult(err), map[string]any{}, nil
			}
			if len(opts) == 0 {
				msg := libraryListText(opts, "当前还没有文献库。请先在「文献库」页创建。")
				return textResult(msg), map[string]any{
					"selection_required": true, "libraries": opts, "message": msg,
				}, nil
			}
			choice := requestLibraryChoice(ctx, req, opts)
			if choice.Input != nil {
				return choice.Input, map[string]any{}, nil
			}
			if choice.Answered {
				trimmed = choice.ID
				if trimmed == "" {
					msg := libraryListText(opts, "已取消选择；可以重新调用本工具并选择文献库。")
					return textResult(msg), map[string]any{
						"selection_required": true, "libraries": opts, "message": msg,
					}, nil
				}
			} else {
				msg := libraryListText(opts, "请把下面的选项展示给用户，让用户选择一个；用户选择后再次调用 larder_select_library(library=\"...\")。")
				return textResult(msg), map[string]any{
					"selection_required": true, "libraries": opts, "message": msg,
				}, nil
			}
		}
		if trimmed == "" || strings.EqualFold(trimmed, "all") {
			state.setDefaultKB(session, "all")
			state.record("选择文献库", "全部知识库", "全部知识库", "ok", "all", time.Now())
			msg := "已切换为全部知识库；检索会跨所有知识库合并结果。后续检索可带 kb=all。"
			return textResult(msg), map[string]any{"kb_id": "all", "kb_name": "全部知识库", "message": msg}, nil
		}
		kbID, _, name, err := state.resolveKB(ctx, trimmed, session)
		if err != nil {
			return errResult(err), map[string]any{}, nil
		}
		state.setDefaultKB(session, kbID)
		state.record("选择文献库", name, name, "ok", kbID, time.Now())
		msg := fmt.Sprintf("已选择文献库：%s（id=%s）。后续工具可以省略 kb 参数；为兼容无会话客户端，建议同时带 kb=%s。", name, kbID, kbID)
		return textResult(msg), map[string]any{"kb_id": kbID, "kb_name": name, "message": msg}, nil
	}

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "larder_use_kb",
		Description: "选择后续工具默认使用的知识库。用户没有指定知识库时，先用 larder_list_libraries 列出并询问，再调用本工具。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in useKBInput) (*sdkmcp.CallToolResult, map[string]any, error) {
		return selectKB(ctx, req, in.KB, true)
	})

	type selectLibraryInput struct {
		Library string `json:"library,omitempty" jsonschema:"文献库 ID（来自 larder_list_libraries）；留空则返回可选项让用户选择；填 all 表示全部"`
	}
	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "larder_select_library",
		Description: "选择本次会话要使用的文献库。可以留空调用：支持 MCP elicitation 的客户端会弹出可点击选项；" +
			"不支持时返回 selection_required 和 libraries 列表，agent 应把选项展示给用户。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in selectLibraryInput) (*sdkmcp.CallToolResult, map[string]any, error) {
		return selectKB(ctx, req, in.Library, false)
	})

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "larder_list_kbs",
		Description: "列出所有知识库及其 ID、名称、文档数；随后用 larder_select_library 选择。",
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
		Name: "larder_list_libraries",
		Description: "列出可用于检索的文献库，包括 Zotero 集合名、文档数与上次同步时间。" +
			"首次问答或 larder_search 返回 selection_required=true 时使用。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in kbListInput) (*sdkmcp.CallToolResult, map[string]any, error) {
		opts, err := listLibraryOptions(ctx)
		if err != nil {
			return errResult(err), map[string]any{}, nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "共 %d 个文献库：\n", len(opts))
		for _, o := range opts {
			kind := ""
			if o.Kind == "zotero" {
				kind = fmt.Sprintf("；Zotero 集合：%s", firstOf(o.Collection, "—"))
				if o.LastSync != "" {
					kind += "；上次同步：" + o.LastSync
				}
			}
			fmt.Fprintf(&b, "- id=%s  %s（%d 篇文档%s）\n", o.ID, o.Name, o.Documents, kind)
		}
		return textResult(b.String()), map[string]any{"count": len(opts), "libraries": opts}, nil
	})

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "larder_search",
		Description: "在文献库中做语义检索，返回带来源引用的相关片段。" +
			"如果未指定 kb 且存在多个文献库，支持 MCP elicitation 的客户端会自动弹出可点击的文献库选项；" +
			"不支持时返回 selection_required=true 和 libraries，此时不要检索，先把选项展示给用户，用户选择后调用 larder_select_library，再带 kb 重新检索。" +
			"结果第一行是 Markdown 右对齐表格小标，请原样放在回答最上方；不要用 HTML、图片或代码块。" +
			"论文级引用原样使用 results[].cite_link（悬浮模式是可点击链接，纯文本模式是 [1]（著者-年份样式为 (作者, 年)））；" +
			"只需纯标记时用 results[].ref_mark；文末参考文献直接复制 reference_block；" +
			"需要其他样式时调用 larder_get_references；严禁编造未出现在 references 中的文献。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in searchInput) (*sdkmcp.CallToolResult, searchOutput, error) {
		session := state.sessionKey(req)
		explicit := strings.TrimSpace(in.KB)
		if explicit == "" {
			explicit = state.defaultKBFor(session)
		}
		// 首次问答：有多个文献库时优先弹出选择；不支持表单的客户端回退文本列表。
		if explicit == "" {
			opts, lerr := listLibraryOptions(ctx)
			if lerr != nil {
				return errResult(lerr), searchOutput{}, nil
			}
			if len(opts) > 1 {
				choice := requestLibraryChoice(ctx, req, opts)
				if choice.Input != nil {
					return choice.Input, searchOutput{}, nil
				}
				if choice.Answered {
					if choice.ID == "" {
						msg := libraryListText(opts, "已取消选择；请重新选择文献库后再检索。")
						return textResult(msg), searchOutput{
							SelectionRequired: true, Libraries: opts, Message: msg,
						}, nil
					}
					explicit = choice.ID
				} else {
					msg := libraryListText(opts, "当前有多个文献库。请先把下面的选项展示给用户，让用户选择一个；用户选择后调用 larder_select_library(library=\"...\")，然后带上 kb 参数重新检索。")
					return textResult(msg), searchOutput{
						SelectionRequired: true, Libraries: opts, Message: msg,
					}, nil
				}
			}
			if len(opts) == 1 {
				explicit = opts[0].ID
				// 记住单库自动选择，后续 larder_get_references 等不带 kb 也能用。
				state.setDefaultKB(session, explicit)
			}
		}
		tgt, err := state.resolveTarget(ctx, explicit, session)
		if err != nil {
			return errResult(err), searchOutput{}, nil
		}
		// 显式检索成功后也记住该库，方便连续追问和生成参考文献。
		if tgt.id != "" {
			state.setDefaultKB(session, tgt.id)
		} else if tgt.all {
			state.setDefaultKB(session, "all")
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
			_, a, _, gerr := state.resolveKB(ctx, tgt.id, session)
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

		// 论文级参考文献：同一篇论文的多个 chunk 共用一个编号。
		// 参考文献格式按当前客户端/agent 的独立配置。
		cfg := state.mgr.Config()
		citeStyle := firstOf(style.ReferenceStyle, cfg.Citation.ReferenceStyle, cfg.Zotero.CiteStyle)
		// 引用显示方式（悬浮/纯文本、悬停字段与排序）统一使用「设置 → 引用格式」。
		refs, refMap, _ := state.referencesForHits(ctx, tgt.id, hits, citeStyle, style, logID)

		out := searchOutput{
			KBID: tgt.id, KBName: tgt.name, Badge: "Larder · " + tgt.name,
			BadgeMarkdown: badge,
			Count:         len(hits), Results: toCitations(hits, state.baseURL, logID, style, profile == "zcode", textLimit),
			References: refs, ReferenceBlock: formatReferenceBlock(refs, citeStyle),
		}
		for i := range out.Results {
			if r, ok := refMap[out.Results[i].DocID]; ok {
				out.Results[i].RefNo = r.RefNo
				out.Results[i].RefMark = r.Mark
				out.Results[i].Reference = r.Text
				// 论文级引用：悬浮模式是可点击链接，纯文本模式是文内标记。
				out.Results[i].CiteID = r.CiteLink
				out.Results[i].CiteLink = r.CiteLink
			}
		}
		text := formatPaperCitations(hits, refMap, tgt.name, badge)
		if len(refs) > 0 {
			text += "\n\n" + out.ReferenceBlock
			if style.Mode == "plain" {
				markExample := "「(作者, 年)」"
				if cite.IsNumeric(citeStyle) {
					markExample = "「[1]」"
				}
				text += fmt.Sprintf("\n\n【引用要求】正文原样使用 results[].cite_link 作为文内引用标记（如 %s）；"+
					"纯文本模式不要把链接、文献词条、文件名或原文塞进行内，完整参考文献统一放在文末；"+
					"文末必须附上面的完整参考文献块，或调用 larder_export_bibliography 生成 .bib 文件；严禁编造未出现在 references 中的文献。", markExample)
			} else {
				text += "\n\n【引用要求】正文使用 references[].cite_link 作为悬浮链接，链接文字是文内引用标记（如 [1] 或 (作者, 年)）；" +
					"悬停内容按「设置 → 引用格式」里勾选并排序的字段生成；文末必须附上面的完整参考文献块。严禁编造未出现在 references 中的文献。"
			}
		}
		return textResult(text), out, nil
	})

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "larder_get_references",
		Description: "把指定文献格式化为参考文献。首次问答后、或用户要求换引用样式时使用。" +
			"传入 doc_ids / item_keys；留空则返回该文献库全部文献。返回 Markdown 参考文献块和 BibTeX；" +
			"正文引用使用 references[].cite_mark，文末直接复制 markdown 字段。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in refsInput) (*sdkmcp.CallToolResult, refsOutput, error) {
		session := state.sessionKey(req)
		kbID, a, kbName, err := state.resolveKB(ctx, in.KB, session)
		if err != nil {
			return errResult(err), refsOutput{}, nil
		}
		cfg := state.mgr.Config()
		display := cfg.Citation.Style(state.citationProfile(req))
		style := cite.NormalizeStyle(firstOf(in.Style, display.ReferenceStyle, cfg.Citation.ReferenceStyle, cfg.Zotero.CiteStyle))
		items, err := state.itemsForRefs(ctx, a, in.DocIDs, in.ItemKeys)
		if err != nil {
			return errResult(err), refsOutput{}, nil
		}
		refs := buildRefs(items, style)
		out := refsOutput{
			KBID: kbID, KBName: kbName, Style: style, Count: len(refs),
			References: refs, Markdown: formatReferenceBlock(refs, style), BibTeX: bibtexBlock(refs),
		}
		text := out.Markdown
		if out.BibTeX != "" {
			text += "\n\n```bibtex\n" + strings.TrimSpace(out.BibTeX) + "\n```"
		}
		return textResult(text), out, nil
	})

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name: "larder_export_bibliography",
		Description: "把文献库中的文献导出为 .bib 文件（写入 Larder 数据目录的 exports/<kb>/ 下），" +
			"同时返回 BibTeX 内容。可传 doc_ids / item_keys 限定范围；留空导出全部。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in exportInput) (*sdkmcp.CallToolResult, exportOutput, error) {
		session := state.sessionKey(req)
		kbID, a, _, err := state.resolveKB(ctx, in.KB, session)
		if err != nil {
			return errResult(err), exportOutput{}, nil
		}
		cfg := state.mgr.Config()
		display := cfg.Citation.Style(state.citationProfile(req))
		style := cite.NormalizeStyle(firstOf(in.Style, display.ReferenceStyle, cfg.Citation.ReferenceStyle, cfg.Zotero.CiteStyle))
		items, err := state.itemsForRefs(ctx, a, in.DocIDs, in.ItemKeys)
		if err != nil {
			return errResult(err), exportOutput{}, nil
		}
		refs := buildRefs(items, style)
		content := bibtexBlock(refs)
		name := sanitizeExportName(in.FileName)
		dir := filepath.Join(a.Cfg.Storage.Dir, "exports", kbID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return errResult(err), exportOutput{}, nil
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return errResult(err), exportOutput{}, nil
		}
		out := exportOutput{Path: path, Count: len(refs), Content: content,
			Message: fmt.Sprintf("已导出 %d 条参考文献到 %s", len(refs), path)}
		return textResult(out.Message + "\n\n```bibtex\n" + strings.TrimSpace(content) + "\n```"), out, nil
	})

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "larder_sync_library",
		Description: "触发 Zotero 文献库同步（后台执行）。返回任务状态；随后用 larder_library_status 查看进度。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in syncInput) (*sdkmcp.CallToolResult, syncOutput, error) {
		if state.library == nil {
			return errResult(fmt.Errorf("文献库服务未启用")), syncOutput{}, nil
		}
		session := state.sessionKey(req)
		kbID, _, _, err := state.resolveKB(ctx, in.KB, session)
		if err != nil {
			return errResult(err), syncOutput{}, nil
		}
		job, err := state.library.StartSync(context.Background(), kbID, libsvc.SyncOptions{Force: in.Force})
		if err != nil {
			return errResult(err), syncOutput{}, nil
		}
		out := syncOutput{KBID: job.KBID, State: job.State, Total: job.Total, Done: job.Done,
			Failed: job.Failed, Skipped: job.Skipped, Message: job.Message}
		return textResult(fmt.Sprintf("同步已启动：%s（%s）", kbID, job.Message)), out, nil
	})

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "larder_library_status",
		Description: "查看 Zotero 文献库的同步状态、成功/失败数量与最近任务进度。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in syncInput) (*sdkmcp.CallToolResult, map[string]any, error) {
		if state.library == nil {
			return errResult(fmt.Errorf("文献库服务未启用")), map[string]any{}, nil
		}
		session := state.sessionKey(req)
		kbID, _, name, err := state.resolveKB(ctx, in.KB, session)
		if err != nil {
			return errResult(err), map[string]any{}, nil
		}
		total, synced, failed, _ := state.library.Counts(ctx, kbID)
		job := state.library.Job(kbID)
		msg := fmt.Sprintf("文献库 %s：共 %d 条，成功 %d，失败 %d；任务状态 %s",
			name, total, synced, failed, job.State)
		return textResult(msg), map[string]any{
			"kb_id": kbID, "kb_name": name, "total": total, "synced": synced,
			"failed": failed, "job": job,
		}, nil
	})

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "larder_ingest_file",
		Description: "把本地文件解析、切块、向量化后加入指定知识库。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in ingestFileInput) (*sdkmcp.CallToolResult, ingestOutput, error) {
		_, a, kbName, err := state.resolveKB(ctx, in.KB, state.sessionKey(req))
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
		_, a, kbName, err := state.resolveKB(ctx, in.KB, state.sessionKey(req))
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
		Description: "列出知识库中的文档；未指定 kb 时使用当前选择，kb=all 列出全部知识库文档。",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, in listInput) (*sdkmcp.CallToolResult, listOutput, error) {
		tgt, err := state.resolveTarget(ctx, in.KB, state.sessionKey(req))
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
		tgt, err := state.resolveTarget(ctx, in.KB, state.sessionKey(req))
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
		tgt, err := state.resolveTarget(ctx, in.KB, state.sessionKey(req))
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
	type targetApp struct {
		info kb.Info
		a    *app.App
	}
	apps := make([]targetApp, 0, len(infos))
	for _, info := range infos {
		a, err := s.mgr.Get(info.ID)
		if err != nil {
			continue
		}
		apps = append(apps, targetApp{info: info, a: a})
	}
	// 查询向量只算一次；所有库共享同一个 embedding 服务，只有维度不一致时才回退到逐库计算。
	var shared []float32
	if len(apps) > 0 {
		shared, _ = apps[0].a.Search.EmbedQuery(ctx, query)
	}
	var hits []model.SearchHit
	var lastErr error
	okCount := 0
	for _, t := range apps {
		var res search.Result
		if len(shared) > 0 && (t.a.Search.Dim() == 0 || t.a.Search.Dim() == len(shared)) {
			res, err = t.a.Search.SearchVector(ctx, query, shared, searchOptions(t.a, topK))
		} else {
			res, err = t.a.Search.Search(ctx, query, searchOptions(t.a, topK))
		}
		if err != nil {
			lastErr = err
			continue
		}
		okCount++
		name := t.info.Name
		if name == "" {
			name = t.info.ID
		}
		for _, h := range res.Hits {
			h.KBID = t.info.ID
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
			KBName: h.KBName, DocID: h.DocID, CiteID: citeToken, CiteLink: md,
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
		case "reference":
			if h.DocTitle != "" {
				parts = append(parts, "【文献词条】"+oneLine(h.DocTitle))
			}
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
	return formatCitationsMode(hits, kbName, badge, publicURL, logID, style, textLimit, false)
}

// formatCitationsMode 在文献库模式下不再要求复制 chunk 级 cite:id，
// 而是让模型使用论文级 ref_mark 和参考文献块。
func formatCitationsMode(hits []model.SearchHit, kbName, badge, publicURL, logID string, style config.CitationStyle, textLimit int, referenceMode bool) string {
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
	if !referenceMode {
		fields := "[cite:id] 后括号内的来源字段"
		if showLog {
			fields = "[cite:id] 后括号内的【日志】链接与来源字段"
		}
		fmt.Fprintf(&b, "\n【引用要求】回答中的每个引用必须逐字复制上面「引用：」后的完整内容，"+
			"包括 %s，紧跟在对应结论句末；"+
			"严禁只输出 [cite:id]，严禁删除或缩写括号内容，严禁集中堆在文末。\n", fields)
	}
	return b.String()
}

// formatPaperCitations 输出论文级引用：正文只用参考文献文内样式，
// 悬浮模式下是可点击链接，纯文本模式下是行内标记。
func formatPaperCitations(hits []model.SearchHit, refMap map[string]reference, kbName, badge string) string {
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
		if r, ok := refMap[h.DocID]; ok && r.CiteLink != "" {
			fmt.Fprintf(&b, "引用：%s\n", r.CiteLink)
		}
		fmt.Fprintf(&b, "\n%s\n", h.Text)
	}
	return b.String()
}

// itemsForRefs 按 doc_ids / item_keys 取文献条目；都为空时返回全部。
func (s *Server) itemsForRefs(ctx context.Context, a *app.App, docIDs, itemKeys string) ([]model.LibraryItem, error) {
	ids := splitCSV(docIDs)
	keys := splitCSV(itemKeys)
	var out []model.LibraryItem
	switch {
	case len(ids) > 0:
		m, err := a.Store.GetLibraryItemsByDocIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if it, ok := m[id]; ok {
				out = append(out, it)
				continue
			}
			if doc, ok, _ := a.Store.GetDocument(ctx, id); ok {
				out = append(out, model.LibraryItem{ItemKey: doc.ID, DocID: doc.ID, Title: doc.Title, ItemType: "document", AttachmentPath: doc.OriginPath})
			}
		}
	case len(keys) > 0:
		for _, k := range keys {
			if it, ok, err := a.Store.GetLibraryItem(ctx, k); err == nil && ok {
				out = append(out, it)
			}
		}
	default:
		items, err := a.Store.ListLibraryItems(ctx)
		if err != nil {
			return nil, err
		}
		out = items
		// 普通知识库没有 Zotero 元数据：用文档标题/来源生成可用的参考文献。
		if len(out) == 0 {
			docs, derr := a.Store.ListDocuments(ctx)
			if derr != nil {
				return nil, derr
			}
			for _, d := range docs {
				out = append(out, model.LibraryItem{
					ItemKey: d.ID, DocID: d.ID, Title: d.Title, ItemType: "document",
					AttachmentPath: d.OriginPath,
				})
			}
		}
	}
	// 去重，保持输入顺序。
	seen := map[string]bool{}
	deduped := make([]model.LibraryItem, 0, len(out))
	for _, it := range out {
		key := firstOf(it.ItemKey, it.DocID, it.Title)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, it)
	}
	return deduped, nil
}

// buildRefs 把文献条目格式化为论文级引用；不做 hover 链接。
func buildRefs(items []model.LibraryItem, style string) []reference {
	style = cite.NormalizeStyle(style)
	out := make([]reference, 0, len(items))
	seen := map[string]bool{}
	for _, it := range items {
		key := firstOf(it.ItemKey, it.DocID, it.Title)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		ci := libsvc.CiteItem(it)
		n := len(out) + 1
		text := cite.Format(ci, style)
		markStr := cite.InText(ci, n, style)
		ref := reference{
			RefNo: n, DocID: it.DocID, ItemKey: it.ItemKey, Title: it.Title,
			Authors: strings.Join(it.Authors, "; "), Year: it.Year,
			Mark: markStr, CiteLink: markStr, Text: text, BibTeX: cite.BibTeX(ci),
		}
		if it.ItemKey != "" {
			ref.ZoteroURL = zoteroSelectURL(it)
		}
		if it.AttachmentKey != "" {
			ref.PDFURL = zoteroPDFURL(it, 0)
		}
		out = append(out, ref)
	}
	return out
}

// referencesForHits 按命中顺序去重到论文级，并生成可点击的 hover 引用。
func (s *Server) referencesForHits(ctx context.Context, kbID string, hits []model.SearchHit, style string, display config.CitationStyle, logID string) ([]reference, map[string]reference, error) {
	style = cite.NormalizeStyle(style)
	base := appBase(s.baseURL)
	hover := display.Mode != "plain"
	out := make([]reference, 0, len(hits))
	byDoc := map[string]reference{}
	seenDoc := map[string]bool{}
	seenItem := map[string]bool{}
	for _, h := range hits {
		if h.DocID == "" || seenDoc[h.DocID] {
			continue
		}
		seenDoc[h.DocID] = true
		kb := firstOf(h.KBID, kbID)
		a, err := s.mgr.Get(kb)
		if err != nil {
			continue
		}
		item, ok, _ := a.Store.GetLibraryItemByDocID(ctx, h.DocID)
		if !ok {
			doc, found, _ := a.Store.GetDocument(ctx, h.DocID)
			if !found {
				continue
			}
			item = model.LibraryItem{
				ItemKey: doc.ID, DocID: doc.ID, Title: doc.Title, ItemType: "document",
				AttachmentPath: doc.OriginPath,
			}
		}
		key := firstOf(item.ItemKey, item.DocID)
		if key == "" || seenItem[key] {
			continue
		}
		seenItem[key] = true
		ci := libsvc.CiteItem(item)
		n := len(out) + 1
		text := cite.Format(ci, style)
		markStr := cite.InText(ci, n, style)
		ref := reference{
			RefNo: n, DocID: h.DocID, ItemKey: item.ItemKey, Title: item.Title,
			Authors: strings.Join(item.Authors, "; "), Year: item.Year,
			Mark: markStr, Text: text, BibTeX: cite.BibTeX(ci),
		}
		line := text
		if cite.IsNumeric(style) {
			line = markStr + " " + text
		}
		if hover {
			if hasField(display.Fields, "link") {
				linkURL := fmt.Sprintf("%s/r/%s/%s", base, url.PathEscape(kb), url.PathEscape(h.DocID))
				title := paperHoverTitle(item, h, line, display.Fields, logLink(base, logID))
				ref.CiteLink = fmt.Sprintf("[%s](%s \"%s\")", markStr, linkURL, markdownTitle(title))
			} else {
				ref.CiteLink = markStr
			}
		} else {
			// 纯文本模式只输出参考文献标准规定的文内标记；
			// 完整参考文献在文末统一输出，不再把链接/文献词条/原文塞进行内。
			ref.CiteLink = markStr
		}
		if item.ItemKey != "" {
			ref.ZoteroURL = zoteroSelectURL(item)
		}
		if item.AttachmentKey != "" {
			ref.PDFURL = zoteroPDFURL(item, 0)
		}
		out = append(out, ref)
		byDoc[h.DocID] = ref
	}
	return out, byDoc, nil
}

func paperFileName(item model.LibraryItem, h model.SearchHit) string {
	name := filepath.Base(strings.TrimSpace(item.AttachmentPath))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = filepath.Base(strings.TrimSpace(h.Source))
	}
	return name
}

func paperExcerpt(h model.SearchHit) string {
	excerpt := oneLine(activity.CleanText(h.Text))
	if r := []rune(excerpt); len(r) > 160 {
		excerpt = string(r[:160]) + "…"
	}
	return excerpt
}

// paperHoverTitle 按「设置 → 引用格式」里勾选并排序的字段生成悬浮标题。
func paperHoverTitle(item model.LibraryItem, h model.SearchHit, refText string, fields []string, logURL string) string {
	if len(fields) == 0 {
		fields = []string{"reference", "file", "text"}
	}
	fileName := paperFileName(item, h)
	excerpt := paperExcerpt(h)
	var b strings.Builder
	for _, f := range fields {
		switch f {
		case "reference":
			if refText != "" {
				b.WriteString("【文献词条】")
				b.WriteString(refText)
			}
		case "file":
			if fileName != "" && fileName != "." {
				b.WriteString("【文献文件名】")
				b.WriteString(fileName)
			}
		case "doc":
			if item.Title != "" {
				b.WriteString("【文档】")
				b.WriteString(item.Title)
			}
		case "heading":
			if h.Heading != "" {
				b.WriteString("【章节】")
				b.WriteString(h.Heading)
			}
		case "text":
			if excerpt != "" {
				b.WriteString("【原文】")
				b.WriteString(excerpt)
			}
		case "log":
			if logURL != "" {
				b.WriteString("【日志】")
				b.WriteString(logURL)
			}
		}
	}
	if b.Len() == 0 {
		return refText
	}
	return b.String()
}

func formatReferenceBlock(refs []reference, style string) string {
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

func bibtexBlock(refs []reference) string {
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

func splitCSV(s string) []string {
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

func sanitizeExportName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "references.bib"
	}
	name = filepath.Base(name)
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
	out := strings.Trim(b.String(), " .")
	if out == "" || out == "." || out == ".." {
		out = "references.bib"
	}
	if !strings.HasSuffix(strings.ToLower(out), ".bib") {
		out += ".bib"
	}
	return out
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
