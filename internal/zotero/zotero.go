// Package zotero 读取 Zotero 本地库：优先使用 Zotero 7+ 的本地 API，
// 本地 API 不可用时回退到 zotero.sqlite 的只读快照。
package zotero

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config 是 Zotero 访问配置。
type Config struct {
	DataDir   string        // Zotero 数据目录（包含 zotero.sqlite 和 storage/）
	APIBase   string        // 本地 API 地址，默认 http://127.0.0.1:23119/api
	LibraryID string        // 0=个人库；群组库填 groupID
	Timeout   time.Duration // 单次请求超时
	ForceDB   bool          // 强制使用 SQLite（测试/调试）
}

// Collection 是 Zotero 集合（文件夹）。
type Collection struct {
	Key       string        `json:"key"`
	Name      string        `json:"name"`
	ParentKey string        `json:"parent_key,omitempty"`
	LibraryID string        `json:"library_id,omitempty"`
	ItemCount int           `json:"item_count"`
	Children  []*Collection `json:"children,omitempty"`
}

// Creator 是 Zotero 作者/编者。
type Creator struct {
	FirstName   string `json:"first_name,omitempty"`
	LastName    string `json:"last_name,omitempty"`
	Name        string `json:"name,omitempty"`
	CreatorType string `json:"creator_type,omitempty"`
}

// Attachment 是 Zotero 附件。
type Attachment struct {
	Key         string
	ParentKey   string
	Title       string
	FileName    string
	ContentType string
	Path        string
	LinkMode    string
	MD5         string
}

// Item 是一篇 Zotero 文献（顶层条目）。
type Item struct {
	Key          string
	Version      int
	ItemType     string
	Title        string
	Date         string
	DOI          string
	URL          string
	Journal      string
	Volume       string
	Issue        string
	Pages        string
	Publisher    string
	Place        string
	Language     string
	Abstract     string
	Extra        string
	Creators     []Creator
	DateModified string
	Attachments  []Attachment
}

// Fulltext 是 Zotero 对附件的全文索引结果。
type Fulltext struct {
	Content      string
	IndexedPages int
	TotalPages   int
}

// Client 是统一的 Zotero 读取客户端。
type Client struct {
	cfg      Config
	http     *http.Client
	db       *sql.DB
	tempDir  string
	mode     string // api / sqlite
	lastErr  error
	openedAt time.Time
	stateMu  sync.Mutex
}

// New 创建客户端。实际使用 API 还是 SQLite 在首次请求时决定。
func New(cfg Config) *Client {
	if strings.TrimSpace(cfg.APIBase) == "" {
		cfg.APIBase = "http://127.0.0.1:23119/api"
	}
	cfg.APIBase = strings.TrimRight(strings.TrimSpace(cfg.APIBase), "/")
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if strings.TrimSpace(cfg.LibraryID) == "" {
		cfg.LibraryID = "0"
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout, CheckRedirect: nil},
		mode: "auto",
	}
}

// Mode 返回实际使用的后端：api / sqlite。
func (c *Client) Mode() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.mode
}

func (c *Client) currentMode() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.mode
}

// LastError 返回回退前的最近错误（用于 UI 提示）。
func (c *Client) LastError() error { return c.lastErr }

// Close 清理 SQLite 快照。
func (c *Client) Close() error {
	if c.db != nil {
		_ = c.db.Close()
		c.db = nil
	}
	if c.tempDir != "" {
		_ = os.RemoveAll(c.tempDir)
		c.tempDir = ""
	}
	return nil
}

// ProbeAPI 检查 Zotero 本地 API 是否可用（不触发 SQLite 回退）。
func (c *Client) ProbeAPI(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIBase+"/", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Zotero-API-Version", "3")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("本地 API 未开启（403）")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("本地 API 返回 %d", resp.StatusCode)
	}
	return nil
}

// Collections 返回全部集合（扁平列表，Children 不填）。
func (c *Client) Collections(ctx context.Context) ([]Collection, error) {
	if c.cfg.ForceDB {
		return c.collectionsSQLite(ctx)
	}
	cols, err := c.collectionsAPI(ctx)
	if err == nil {
		c.mode = "api"
		return cols, nil
	}
	c.lastErr = err
	// 本地 API 未开启 / 未运行：尝试 SQLite 快照。
	cols, dbErr := c.collectionsSQLite(ctx)
	if dbErr != nil {
		return nil, fmt.Errorf("Zotero 本地 API 不可用（%v），SQLite 读取也失败：%w", err, dbErr)
	}
	c.mode = "sqlite"
	return cols, nil
}

// CollectionTree 把扁平集合构造成树。
func CollectionTree(cols []Collection) []*Collection {
	byKey := make(map[string]*Collection, len(cols))
	for i := range cols {
		c := cols[i]
		c.Children = nil
		byKey[c.Key] = &c
	}
	roots := make([]*Collection, 0)
	for i := range cols {
		c := byKey[cols[i].Key]
		if c == nil {
			continue
		}
		if parent := byKey[c.ParentKey]; parent != nil && parent.Key != c.Key {
			parent.Children = append(parent.Children, c)
		} else {
			roots = append(roots, c)
		}
	}
	var sortTree func(list []*Collection)
	sortTree = func(list []*Collection) {
		sort.SliceStable(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
		for _, c := range list {
			sortTree(c.Children)
		}
	}
	sortTree(roots)
	return roots
}

// CollectionItems 返回集合内的顶层文献。includeSub 为 true 时包含子集合。
func (c *Client) CollectionItems(ctx context.Context, collectionKey string, includeSub bool) ([]Item, error) {
	if c.mode == "sqlite" {
		return c.collectionItemsSQLite(ctx, collectionKey, includeSub)
	}
	items, err := c.collectionItemsAPI(ctx, collectionKey, includeSub)
	if err == nil {
		c.mode = "api"
		return items, nil
	}
	// API 中途失败时尝试 SQLite。
	c.lastErr = err
	if dbErr := c.ensureSQLite(); dbErr != nil {
		return nil, err
	}
	c.mode = "sqlite"
	return c.collectionItemsSQLite(ctx, collectionKey, includeSub)
}

// Fulltext 返回附件的 Zotero 全文缓存。第二个返回值表示是否存在可用全文。
func (c *Client) Fulltext(ctx context.Context, attachmentKey string) (Fulltext, bool, error) {
	if c.currentMode() != "sqlite" {
		if ft, ok, err := c.fulltextAPI(ctx, attachmentKey); err == nil {
			return ft, ok, nil
		}
	}
	// 同步 worker 可能并发调用；SQLite 快照只初始化一次。
	c.stateMu.Lock()
	err := c.ensureSQLite()
	if err == nil {
		c.mode = "sqlite"
	}
	c.stateMu.Unlock()
	if err != nil {
		return Fulltext{}, false, err
	}
	return c.fulltextSQLite(attachmentKey)
}

// AttachmentPath 解析附件的本地路径；不存在时返回空字符串。
func (c *Client) AttachmentPath(att Attachment) string {
	return resolveAttachmentPath(c.cfg.DataDir, att)
}

// ReadAttachment 读取附件字节：优先本地路径，其次本地 API 文件接口。
func (c *Client) ReadAttachment(ctx context.Context, att Attachment) ([]byte, error) {
	if p := c.AttachmentPath(att); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			return b, nil
		}
	}
	if c.currentMode() != "sqlite" {
		return c.downloadAttachmentAPI(ctx, att.Key)
	}
	return nil, fmt.Errorf("附件文件不存在：%s", att.Key)
}

// PrimaryAttachment 选择主附件：PDF 优先，其次 EPUB/HTML，同级取最大文件。
func PrimaryAttachment(atts []Attachment) (Attachment, bool) {
	if len(atts) == 0 {
		return Attachment{}, false
	}
	priority := func(a Attachment) int {
		ct := strings.ToLower(a.ContentType)
		name := strings.ToLower(a.FileName + " " + a.Path)
		switch {
		case ct == "application/pdf" || strings.HasSuffix(name, ".pdf"):
			return 0
		case ct == "application/epub+zip" || strings.HasSuffix(name, ".epub"):
			return 1
		case strings.Contains(ct, "html"):
			return 2
		case strings.Contains(ct, "text") || strings.Contains(ct, "word") || strings.HasSuffix(name, ".docx"):
			return 3
		default:
			return 4
		}
	}
	sort.SliceStable(atts, func(i, j int) bool {
		pi, pj := priority(atts[i]), priority(atts[j])
		if pi != pj {
			return pi < pj
		}
		return false
	})
	return atts[0], true
}

// PrimaryPDF 只选择 PDF 附件；用于需要精确页面的场景。
func PrimaryPDF(atts []Attachment) (Attachment, bool) {
	for _, a := range atts {
		ct := strings.ToLower(a.ContentType)
		name := strings.ToLower(a.FileName + " " + a.Path)
		if ct == "application/pdf" || strings.HasSuffix(name, ".pdf") {
			return a, true
		}
	}
	return Attachment{}, false
}

// Authors 把 Zotero 作者格式化为 "Last, First" 列表。
func (it Item) Authors() []string {
	out := make([]string, 0, len(it.Creators))
	for _, c := range it.Creators {
		if strings.TrimSpace(c.CreatorType) != "" && c.CreatorType != "author" {
			continue
		}
		if strings.TrimSpace(c.LastName) != "" {
			if strings.TrimSpace(c.FirstName) != "" {
				out = append(out, strings.TrimSpace(c.LastName)+", "+strings.TrimSpace(c.FirstName))
			} else {
				out = append(out, strings.TrimSpace(c.LastName))
			}
		} else if strings.TrimSpace(c.Name) != "" {
			out = append(out, strings.TrimSpace(c.Name))
		}
	}
	if len(out) == 0 {
		// 没有 author 类型时退回全部创作者（编者、译者等），避免引用为空。
		for _, c := range it.Creators {
			if strings.TrimSpace(c.LastName) != "" {
				out = append(out, strings.TrimSpace(c.LastName))
			} else if strings.TrimSpace(c.Name) != "" {
				out = append(out, strings.TrimSpace(c.Name))
			}
		}
	}
	return out
}

// Year 从 Zotero 的 date 字段提取四位年份。
func (it Item) Year() int {
	return extractYear(it.Date)
}

var citationKeyRE = regexp.MustCompile(`(?im)^\s*citation\s*key\s*:\s*([^\s]+)\s*$`)

// CitationKey 从 Extra 字段提取 Better BibTeX 的 citation key。
func (it Item) CitationKey() string {
	if m := citationKeyRE.FindStringSubmatch(it.Extra); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func extractYear(date string) int {
	for i := 0; i+4 <= len(date); i++ {
		s := date[i : i+4]
		if n, err := strconv.Atoi(s); err == nil && n >= 1000 && n <= 2999 {
			return n
		}
	}
	return 0
}

func resolveAttachmentPath(dataDir string, att Attachment) string {
	if strings.TrimSpace(dataDir) == "" {
		return ""
	}
	p := strings.TrimSpace(att.Path)
	name := strings.TrimSpace(att.FileName)
	if name == "" {
		name = filepath.Base(p)
	}
	var candidates []string
	switch {
	case strings.HasPrefix(p, "storage:"):
		rel := strings.TrimPrefix(p, "storage:")
		if rel == "" {
			rel = name
		}
		candidates = append(candidates, filepath.Join(dataDir, "storage", att.Key, filepath.FromSlash(rel)))
	case strings.HasPrefix(p, "attachments:"):
		rel := strings.TrimPrefix(p, "attachments:")
		candidates = append(candidates,
			filepath.Join(dataDir, "attachments", filepath.FromSlash(rel)),
			filepath.Join(dataDir, filepath.FromSlash(rel)))
	case strings.HasPrefix(strings.ToLower(p), "file://"):
		if u, err := url.Parse(p); err == nil {
			h := u.Path
			if len(h) >= 3 && h[0] == '/' && h[2] == ':' {
				h = h[1:] // /C:/... -> C:/...
			}
			candidates = append(candidates, filepath.FromSlash(h))
		}
	case p != "":
		candidates = append(candidates, filepath.FromSlash(p))
	}
	if name != "" {
		candidates = append(candidates, filepath.Join(dataDir, "storage", att.Key, filepath.FromSlash(name)))
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	// 路径漂移：扫描 storage/<key>/ 找同类型文件。
	if dir := filepath.Join(dataDir, "storage", att.Key); dir != "" {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
					continue
				}
				ext := strings.ToLower(filepath.Ext(e.Name()))
				if ext == ".pdf" || ext == ".epub" || ext == ".html" || ext == ".htm" || ext == ".docx" || ext == ".txt" || ext == ".md" {
					return filepath.Join(dir, e.Name())
				}
			}
		}
	}
	return ""
}

func (c *Client) libraryPrefix() string {
	id := strings.TrimSpace(c.cfg.LibraryID)
	if id == "" {
		id = "0"
	}
	if id == "0" {
		return "/users/0"
	}
	return "/groups/" + url.PathEscape(id)
}
