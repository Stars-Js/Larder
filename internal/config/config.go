// Package config 负责加载配置。优先级：默认值 < TOML 文件 < 环境变量。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Duration 让配置里可以直接写 "10s" 这样的字符串。
type Duration time.Duration

// UnmarshalText 实现 encoding.TextUnmarshaler，供 TOML 解析使用。
func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// MarshalText 让 Duration 可以写回文本。
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

// Std 返回标准类型的时长。
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Endpoint 是一个外部服务端点，三个 provider 共用同一套连接参数。
type Endpoint struct {
	BaseURL    string   `toml:"base_url"`           // 例如 https://api.openai.com
	APIKey     string   `toml:"api_key"`            // 当前使用的密钥（APIKeys[0]）
	APIKeys    []string `toml:"api_keys,omitempty"` // 多密钥列表；第一个为当前使用
	Model      string   `toml:"model"`              // 模型名
	Timeout    Duration `toml:"timeout"`            // 单次请求超时
	MaxRetries int      `toml:"max_retries"`        // 失败重试次数
	QPS        float64  `toml:"qps"`                // 每秒最大请求数，0 表示不限
	Path       string   `toml:"path"`               // 覆盖默认接口路径
}

// Provider 是某一个环节的可选实现。
type Provider struct {
	Enabled  bool     `toml:"enabled"`
	Provider string   `toml:"provider"` // 具体实现名
	Endpoint Endpoint `toml:"endpoint"`
	// Dim 仅向量模型使用：为 0 时首次调用后自动探测并落库。
	Dim int `toml:"dim"`
}

// Config 是应用的全部配置。
type Config struct {
	Server   ServerConfig   `toml:"server"`
	Storage  StorageConfig  `toml:"storage"`
	Chunk    ChunkConfig    `toml:"chunk"`
	Search   SearchConfig   `toml:"search"`
	Parse    Provider       `toml:"parse"`
	Embed    Provider       `toml:"embed"`
	Rerank   Provider       `toml:"rerank"`
	Citation CitationConfig `toml:"citation"`
	Zotero   ZoteroConfig   `toml:"zotero"`
	Log      LogConfig      `toml:"log"`
}

// ZoteroConfig 描述 Zotero 文献库联动。
type ZoteroConfig struct {
	Enabled       bool     `toml:"enabled" json:"enabled"`               // 是否开启联动
	DataDir       string   `toml:"data_dir" json:"data_dir"`             // Zotero 数据目录（包含 zotero.sqlite）；留空自动读取 prefs.js
	APIBase       string   `toml:"api_base" json:"api_base"`             // 本地 API 地址，默认 http://127.0.0.1:23119/api
	LibraryID     string   `toml:"library_id" json:"library_id"`         // 个人库为 0；群组库填 groupID
	LocalAPIKey   string   `toml:"local_api_key" json:"local_api_key"`   // Zotero 10+ 本地 API 写入密钥（用于把解析 MD 挂到词条下）
	MDMode        string   `toml:"md_mode" json:"md_mode"`               // item（词条子附件）/ larder（Zotero/larder）/ off
	ParseMode     string   `toml:"parse_mode" json:"parse_mode"`         // auto（优先版面解析）/ layout（强制版面）/ zotero（只读全文缓存）
	CiteStyle     string   `toml:"cite_style" json:"cite_style"`         // 默认参考文献样式
	Watch         bool     `toml:"watch" json:"watch"`                   // 监听 Zotero 数据库变化并自动同步
	WatchInterval Duration `toml:"watch_interval" json:"watch_interval"` // 变化检测间隔
}

var zoteroDataDirRE = regexp.MustCompile(`(?m)^\s*user_pref\(\s*"extensions\.zotero\.dataDir"\s*,\s*"((?:\\.|[^"\\])*)"\s*\)\s*;`)

// parseZoteroPrefsFile 从 Zotero 的 prefs.js 读取数据目录。
func parseZoteroPrefsFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	m := zoteroDataDirRE.FindSubmatch(b)
	if len(m) != 2 {
		return ""
	}
	s := string(m[1])
	if u, err := strconv.Unquote(`"` + s + `"`); err == nil {
		s = u
	}
	return strings.TrimSpace(s)
}

// zoteroPrefsCandidates 返回常见 Zotero profile 下的 prefs.js 路径。
func zoteroPrefsCandidates() []string {
	var out []string
	if cfg, err := os.UserConfigDir(); err == nil {
		out = append(out,
			filepath.Join(cfg, "Zotero", "Zotero", "Profiles"),
			filepath.Join(cfg, "Zotero", "Profiles"),
		)
	}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out,
			filepath.Join(home, ".zotero", "zotero"),
			filepath.Join(home, "Library", "Application Support", "Zotero", "Profiles"),
		)
	}
	var files []string
	for _, dir := range out {
		matches, _ := filepath.Glob(filepath.Join(dir, "*", "prefs.js"))
		files = append(files, matches...)
	}
	return files
}

// DefaultZoteroDataDir 优先从 Zotero 设置读取数据目录，读不到再用 ~/Zotero。
func DefaultZoteroDataDir() string {
	if v := strings.TrimSpace(os.Getenv("ZOTERO_DATA_DIR")); v != "" {
		return v
	}
	for _, p := range zoteroPrefsCandidates() {
		if d := parseZoteroPrefsFile(p); d != "" {
			if fi, err := os.Stat(d); err == nil && fi.IsDir() {
				return d
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// Zotero 在 Windows / macOS / Linux 的默认数据目录都是 ~/Zotero。
	fallback := filepath.Join(home, "Zotero")
	if fi, err := os.Stat(fallback); err == nil && fi.IsDir() {
		return fallback
	}
	return ""
}

// ServerConfig 描述对外 HTTP 服务。
type ServerConfig struct {
	Addr            string   `toml:"addr"`
	MCPPath         string   `toml:"mcp_path"`
	MCPEnabled      bool     `toml:"mcp_enabled"` // 是否对外提供 MCP 服务
	ReadTimeout     Duration `toml:"read_timeout"`
	WriteTimeout    Duration `toml:"write_timeout"`
	IdleTimeout     Duration `toml:"idle_timeout"`
	ShutdownTimeout Duration `toml:"shutdown_timeout"`
}

// StorageConfig 描述本地单文件存储。
type StorageConfig struct {
	Path string `toml:"path"` // 单库模式下的数据库文件
	Dir  string `toml:"dir"`  // 多知识库模式下的根目录，每个库为其中的 <id>.db
}

// ChunkConfig 描述切块策略。
type ChunkConfig struct {
	MaxRunes int `toml:"max_runes"` // 单块上限
	Overlap  int `toml:"overlap"`   // 跨块重叠字符数
	MinRunes int `toml:"min_runes"` // 过短的块并入上一块
}

// SearchConfig 描述检索参数。
type SearchConfig struct {
	TopK          int `toml:"top_k"`
	CandidateK    int `toml:"candidate_k"` // 向量召回数，供重排使用
	MaxTopK       int `toml:"max_top_k"`
	MaxCandidates int `toml:"max_candidates"`
}

// LogConfig 描述日志。
type LogConfig struct {
	Level  string `toml:"level"`  // debug/info/warn/error
	Format string `toml:"format"` // text/json
}

// CitationStyle 是某个平台的引文显示配置。
type CitationStyle struct {
	Mode           string   `toml:"mode" json:"mode"`     // markdown / plain
	Fields         []string `toml:"fields" json:"fields"` // 悬浮内容字段
	ReferenceStyle string   `toml:"style" json:"style"`   // 当前客户端的参考文献格式
}

// CitationConfig 按客户端平台保存引文格式；默认格式在 profiles.default.style。
type CitationConfig struct {
	Profiles map[string]CitationStyle `toml:"profiles" json:"profiles"`
}

// CitationProfiles 是设置页展示的平台及名称。
var CitationProfiles = []struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}{
	{"default", "默认"},
	{"zcode", "ZCode"},
	{"cherrystudio", "Cherry Studio"},
	{"claude-desktop", "Claude Desktop"},
	{"claude-code", "Claude Code"},
	{"cursor", "Cursor"},
	{"dsh", "DSH"},
}

var defaultCitationFields = []string{"link", "reference", "file", "text"}

func defaultCitationProfiles() map[string]CitationStyle {
	out := make(map[string]CitationStyle, len(CitationProfiles))
	for _, p := range CitationProfiles {
		mode := "markdown"
		if p.ID == "zcode" {
			mode = "plain"
		}
		var fields []string
		if mode == "markdown" {
			fields = defaultCitationFields
		}
		style := ""
		if p.ID == "default" {
			style = "gb-t-7714-numeric"
		}
		out[p.ID] = CitationStyle{Mode: mode, Fields: append([]string(nil), fields...), ReferenceStyle: style}
	}
	return out
}

// NormalizeZotero 补齐 Zotero 联动配置的默认值与合法取值。
func NormalizeZotero(z *ZoteroConfig) {
	if z == nil {
		return
	}
	if strings.TrimSpace(z.DataDir) == "" {
		z.DataDir = DefaultZoteroDataDir()
	}
	if strings.TrimSpace(z.APIBase) == "" {
		z.APIBase = "http://127.0.0.1:23119/api"
	}
	z.APIBase = strings.TrimRight(strings.TrimSpace(z.APIBase), "/")
	if strings.TrimSpace(z.LibraryID) == "" {
		z.LibraryID = "0"
	}
	switch strings.ToLower(strings.TrimSpace(z.MDMode)) {
	case "item":
		z.MDMode = "item"
	case "larder":
		z.MDMode = "larder"
	case "off":
		z.MDMode = "off"
	case "storage":
		// 旧版“PDF 同目录”升级为“同词条下”（Zotero 子附件）。
		z.MDMode = "item"
	default:
		z.MDMode = "item"
	}
	if strings.TrimSpace(z.CiteStyle) == "" {
		z.CiteStyle = "gb-t-7714-numeric"
	}
	switch strings.ToLower(strings.TrimSpace(z.ParseMode)) {
	case "auto", "layout", "zotero":
		z.ParseMode = strings.ToLower(strings.TrimSpace(z.ParseMode))
	default:
		z.ParseMode = "auto"
	}
	if z.WatchInterval.Std() <= 0 {
		z.WatchInterval = Duration(5 * time.Second)
	}
}

// NormalizeCitation 补齐平台、校验字段与显示模式。
func NormalizeCitation(c *CitationConfig) {
	if c == nil {
		return
	}
	if c.Profiles == nil {
		c.Profiles = defaultCitationProfiles()
	}
	delete(c.Profiles, "generic")
	defaults := defaultCitationProfiles()
	for _, p := range CitationProfiles {
		if _, ok := c.Profiles[p.ID]; !ok {
			c.Profiles[p.ID] = defaults[p.ID]
		}
	}
	if st, ok := c.Profiles["default"]; ok && strings.TrimSpace(st.ReferenceStyle) == "" {
		st.ReferenceStyle = "gb-t-7714-numeric"
		c.Profiles["default"] = st
	}
	defaultStyle := strings.TrimSpace(c.Profiles["default"].ReferenceStyle)
	if defaultStyle == "" {
		defaultStyle = "gb-t-7714-numeric"
	}
	for key, st := range c.Profiles {
		mode := strings.ToLower(strings.TrimSpace(st.Mode))
		if mode != "plain" {
			mode = "markdown"
		}
		// ZCode 会移除 Markdown 链接，固定使用纯文本引用。
		if key == "zcode" {
			mode = "plain"
		}
		rawFields := st.Fields
		if rawFields == nil {
			if mode == "markdown" {
				rawFields = append([]string(nil), defaultCitationFields...)
			} else {
				rawFields = []string{}
			}
		}
		// 可选字段：链接、文献词条、文献文件名、原文；其余旧字段丢弃。
		fields := make([]string, 0, 4)
		seen := map[string]bool{}
		for _, f := range rawFields {
			f = strings.ToLower(strings.TrimSpace(f))
			if f != "link" && f != "reference" && f != "file" && f != "text" {
				continue
			}
			if seen[f] {
				continue
			}
			seen[f] = true
			fields = append(fields, f)
		}
		// 悬浮模式链接固定第一项；允许全部取消勾选。
		if mode == "markdown" {
			ordered := make([]string, 0, len(fields))
			if seen["link"] {
				ordered = append(ordered, "link")
			}
			for _, f := range fields {
				if f != "link" {
					ordered = append(ordered, f)
				}
			}
			fields = ordered
		}
		styleID := strings.TrimSpace(st.ReferenceStyle)
		if styleID == "" {
			styleID = defaultStyle
		}
		c.Profiles[key] = CitationStyle{Mode: mode, Fields: fields, ReferenceStyle: styleID}
	}
}

// Style 返回某平台配置；未知平台回落到 default。
func (c CitationConfig) Style(profile string) CitationStyle {
	key := strings.ToLower(strings.TrimSpace(profile))
	st, ok := c.Profiles[key]
	if !ok {
		st, ok = c.Profiles["default"]
	}
	if !ok {
		st = CitationStyle{Mode: "markdown", Fields: append([]string(nil), defaultCitationFields...)}
	}
	if st.Mode == "" {
		st.Mode = "markdown"
	}
	if strings.TrimSpace(st.ReferenceStyle) == "" {
		if def, ok := c.Profiles["default"]; ok {
			st.ReferenceStyle = strings.TrimSpace(def.ReferenceStyle)
		}
	}
	if strings.TrimSpace(st.ReferenceStyle) == "" {
		st.ReferenceStyle = "gb-t-7714-numeric"
	}
	return st
}

// ErrMissingAPIKey 表示某个已启用的外部服务没有配置密钥。
var ErrMissingAPIKey = errors.New("config: 外部服务缺少 api_key")

// Default 返回内置默认配置，所有字段都有可用值。
func Default() Config {
	return Config{
		Server: ServerConfig{
			Addr:            "127.0.0.1:8765",
			MCPPath:         "/mcp",
			MCPEnabled:      true,
			ReadTimeout:     Duration(5 * time.Minute),
			WriteTimeout:    Duration(30 * time.Minute),
			IdleTimeout:     Duration(2 * time.Minute),
			ShutdownTimeout: Duration(10 * time.Second),
		},
		Storage: StorageConfig{Path: filepath.Join("data", "larder.db"), Dir: "data"},
		Chunk:   ChunkConfig{MaxRunes: 1200, Overlap: 150, MinRunes: 80},
		Search: SearchConfig{
			TopK:          8,
			CandidateK:    40,
			MaxTopK:       50,
			MaxCandidates: 200,
		},
		Parse: Provider{
			Enabled:  true,
			Provider: "plain",
			Endpoint: Endpoint{Timeout: Duration(120 * time.Second), MaxRetries: 2},
		},
		Embed: Provider{
			Enabled:  true,
			Provider: "openai",
			Endpoint: Endpoint{
				BaseURL:    "https://api.openai.com",
				Model:      "text-embedding-3-small",
				Timeout:    Duration(60 * time.Second),
				MaxRetries: 3,
				QPS:        8,
			},
			Dim: 1536,
		},
		Citation: CitationConfig{Profiles: defaultCitationProfiles()},
		Zotero: ZoteroConfig{
			Enabled:       false,
			DataDir:       DefaultZoteroDataDir(),
			APIBase:       "http://127.0.0.1:23119/api",
			LibraryID:     "0",
			MDMode:        "item",
			ParseMode:     "auto",
			CiteStyle:     "gb-t-7714-numeric",
			Watch:         true,
			WatchInterval: Duration(5 * time.Second),
		},
		Rerank: Provider{
			Enabled:  false,
			Provider: "openai",
			Endpoint: Endpoint{Timeout: Duration(30 * time.Second), MaxRetries: 2, QPS: 8},
		},
		Log: LogConfig{Level: "info", Format: "text"},
	}
}

// Load 读取配置文件并叠加环境变量。
// path 为空时依次尝试 $LARDER_CONFIG、./configs/larder.toml、数据目录下 config.toml。
func Load(path string) (Config, string, error) {
	cfg := Default()

	resolved, err := resolvePath(path)
	if err != nil {
		return cfg, "", err
	}

	if resolved != "" {
		b, err := os.ReadFile(resolved)
		if err != nil {
			return cfg, resolved, fmt.Errorf("读取配置 %s: %w", resolved, err)
		}
		// 先做环境变量替换，再解析，避免密钥写进文件。
		b = []byte(os.ExpandEnv(string(b)))
		if err := toml.Unmarshal(b, &cfg); err != nil {
			return cfg, resolved, fmt.Errorf("解析配置 %s: %w", resolved, err)
		}
	}

	applyEnv(&cfg)
	fill(&cfg)
	return cfg, resolved, nil
}

// BaseDir 返回 exe 所在目录（“本级目录”）。程序从这里读取/生成 larder.toml、
// 存放 data/ 与日志。`go run` 时 os.Executable 指向临时构建目录，此时退回当前目录。
func BaseDir() string {
	if v := strings.TrimSpace(os.Getenv("LARDER_BASE_DIR")); v != "" {
		if abs, err := filepath.Abs(v); err == nil {
			return abs
		}
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		lower := strings.ToLower(exe)
		// go run / go test 的可执行文件位于临时构建目录，不应把配置写进那里。
		if !strings.Contains(lower, "go-build") {
			return filepath.Dir(exe)
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// DataDir 返回应用数据目录，不存在时创建。默认是 exe 同级的 data/。
func DataDir() (string, error) {
	base := strings.TrimSpace(os.Getenv("LARDER_DATA_DIR"))
	if base == "" {
		base = filepath.Join(BaseDir(), "data")
	} else if !filepath.IsAbs(base) {
		base = filepath.Join(BaseDir(), base)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	return base, nil
}

func defaultDataDir() string {
	dir, err := DataDir()
	if err != nil {
		return filepath.Join(BaseDir(), "data")
	}
	return dir
}

func defaultDataFile() string {
	return filepath.Join(defaultDataDir(), "larder.db")
}

func resolvePath(path string) (string, error) {
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("配置文件不存在: %w", err)
		}
		return path, nil
	}
	if p := os.Getenv("LARDER_CONFIG"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	candidates := []string{
		filepath.Join(BaseDir(), "larder.toml"),
		"larder.toml",
		"configs/larder.toml",
	}
	if dir, err := DataDir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, "config.toml"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			abs, _ := filepath.Abs(c)
			return abs, nil
		}
	}
	return "", nil
}

func applyEnv(c *Config) {
	setStr(&c.Server.Addr, "LARDER_ADDR")
	setStr(&c.Storage.Path, "LARDER_DB")
	setStr(&c.Log.Level, "LARDER_LOG_LEVEL")
	setStr(&c.Log.Format, "LARDER_LOG_FORMAT")

	applyEndpointEnv(&c.Embed.Endpoint, "LARDER_EMBED")
	applyEndpointEnv(&c.Rerank.Endpoint, "LARDER_RERANK")
	applyEndpointEnv(&c.Parse.Endpoint, "LARDER_PARSE")

	if v := os.Getenv("LARDER_EMBED_PROVIDER"); v != "" {
		c.Embed.Provider = v
	}
	if v := os.Getenv("LARDER_PARSE_PROVIDER"); v != "" {
		c.Parse.Provider = v
	}
	if v := os.Getenv("LARDER_RERANK_ENABLED"); v != "" {
		c.Rerank.Enabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("LARDER_MCP_ENABLED"); v != "" {
		c.Server.MCPEnabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("LARDER_EMBED_DIM"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Embed.Dim = n
		}
	}
	if v := os.Getenv("LARDER_ZOTERO_ENABLED"); v != "" {
		c.Zotero.Enabled = v == "1" || strings.EqualFold(v, "true")
	}
	setStr(&c.Zotero.DataDir, "LARDER_ZOTERO_DATA_DIR")
	setStr(&c.Zotero.APIBase, "LARDER_ZOTERO_API_BASE")
	setStr(&c.Zotero.LibraryID, "LARDER_ZOTERO_LIBRARY_ID")
	setStr(&c.Zotero.MDMode, "LARDER_ZOTERO_MD_MODE")
	setStr(&c.Zotero.ParseMode, "LARDER_ZOTERO_PARSE_MODE")
	setStr(&c.Zotero.CiteStyle, "LARDER_ZOTERO_CITE_STYLE")
	if v := os.Getenv("LARDER_ZOTERO_WATCH"); v != "" {
		c.Zotero.Watch = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("LARDER_ZOTERO_WATCH_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.Zotero.WatchInterval = Duration(d)
		}
	}
}

func applyEndpointEnv(e *Endpoint, prefix string) {
	setStr(&e.BaseURL, prefix+"_BASE_URL")
	setStr(&e.APIKey, prefix+"_API_KEY")
	setStr(&e.Model, prefix+"_MODEL")
	setStr(&e.Path, prefix+"_PATH")
	if v := os.Getenv(prefix + "_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			e.Timeout = Duration(d)
		}
	}
}

func setStr(dst *string, key string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

// DefaultConfigPath 返回默认配置文件路径：exe 同级的 larder.toml。
func DefaultConfigPath() string {
	if p := os.Getenv("LARDER_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(BaseDir(), "larder.toml")
}

// Save 把配置写回 TOML 文件（会先做环境变量反向处理，保持密钥占位）。
func Save(path string, cfg Config) error {
	// 避免把明文密钥写入文件：若密钥来自环境变量则保留为 ${ENV} 引用。
	cfg = maskSecrets(cfg)
	// 数据目录位于 exe 同级时写成相对路径，整个文件夹移动后仍可用。
	cfg.Storage.Dir = relativize(cfg.Storage.Dir)
	cfg.Storage.Path = relativize(cfg.Storage.Path)
	b, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	header := []byte(`# Larder 参数文件（程序自动生成，可直接修改）。
#
# 程序启动时会读取同目录下的 larder.toml；不存在时按默认值生成。
# 数据文件默认放在同目录的 data/ 下，每个知识库一个 <id>.db。
# 修改后重启程序生效；在界面「设置」中保存的修改会立即写回这里并热生效。
#
# 密钥可明文填写，也可写成 ${ENV_NAME} 引用环境变量（推荐）。
# 环境变量优先级最高：LARDER_ADDR / LARDER_DATA_DIR / LARDER_EMBED_* /
# LARDER_PARSE_* / LARDER_RERANK_* / LARDER_MCP_ENABLED 等。

`)
	if err := os.WriteFile(path, append(header, b...), 0o644); err != nil {
		return fmt.Errorf("写入配置 %s 失败: %w", path, err)
	}
	return nil
}

// relativize 把位于 exe 目录内的绝对路径转成相对路径，便于整体搬移。
func relativize(p string) string {
	if p == "" {
		return p
	}
	rel, err := filepath.Rel(BaseDir(), p)
	if err != nil || rel == "" || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return p
	}
	return filepath.ToSlash(rel)
}

// maskSecrets 把与同名环境变量一致的密钥替换为 ${ENV} 占位，避免落盘明文。
func maskSecrets(cfg Config) Config {
	maskEndpointKeys(&cfg.Embed.Endpoint, "LARDER_EMBED_API_KEY")
	maskEndpointKeys(&cfg.Rerank.Endpoint, "LARDER_RERANK_API_KEY")
	maskEndpointKeys(&cfg.Parse.Endpoint, "LARDER_PARSE_API_KEY")
	return cfg
}

func maskEndpointKeys(e *Endpoint, envName string) {
	v := os.Getenv(envName)
	if v == "" {
		return
	}
	if e.APIKey == v {
		e.APIKey = "${" + envName + "}"
	}
	for i, k := range e.APIKeys {
		if k == v {
			e.APIKeys[i] = "${" + envName + "}"
		}
	}
}

// normalizeKeys 去重并把当前密钥放到列表首位；兼容旧配置只有 api_key 的情况。
func normalizeKeys(e *Endpoint) {
	seen := make(map[string]bool, len(e.APIKeys)+1)
	keys := make([]string, 0, len(e.APIKeys)+1)
	add := func(k string) {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		keys = append(keys, k)
	}
	add(e.APIKey)
	for _, k := range e.APIKeys {
		add(k)
	}
	e.APIKeys = keys
	if len(keys) > 0 {
		e.APIKey = keys[0]
	} else {
		e.APIKey = ""
	}
}

// fill 兜底：确保关键字段非空且取值在合理范围。
func fill(c *Config) {
	NormalizeCitation(&c.Citation)
	if c.Server.Addr == "" {
		c.Server.Addr = "127.0.0.1:8765"
	}
	if c.Server.MCPPath == "" || !strings.HasPrefix(c.Server.MCPPath, "/") {
		c.Server.MCPPath = "/mcp"
	}
	if c.Storage.Path == "" {
		c.Storage.Path = filepath.Join("data", "larder.db")
	}
	if !filepath.IsAbs(c.Storage.Path) {
		c.Storage.Path = filepath.Join(BaseDir(), c.Storage.Path)
	}
	if c.Storage.Dir == "" {
		c.Storage.Dir = defaultDataDir()
	} else if !filepath.IsAbs(c.Storage.Dir) {
		c.Storage.Dir = filepath.Join(BaseDir(), c.Storage.Dir)
	}
	if c.Chunk.MaxRunes <= 0 {
		c.Chunk.MaxRunes = 1200
	}
	if c.Chunk.Overlap < 0 {
		c.Chunk.Overlap = 0
	}
	if c.Chunk.Overlap >= c.Chunk.MaxRunes {
		c.Chunk.Overlap = c.Chunk.MaxRunes / 5
	}
	if c.Search.TopK <= 0 {
		c.Search.TopK = 8
	}
	if c.Search.CandidateK < c.Search.TopK {
		c.Search.CandidateK = c.Search.TopK * 4
	}
	if c.Search.MaxTopK <= 0 {
		c.Search.MaxTopK = 50
	}
	if c.Search.MaxCandidates < c.Search.CandidateK {
		c.Search.MaxCandidates = c.Search.CandidateK * 4
	}
	if c.Embed.Endpoint.Timeout.Std() <= 0 {
		c.Embed.Endpoint.Timeout = Duration(60 * time.Second)
	}
	if c.Parse.Endpoint.Timeout.Std() <= 0 {
		c.Parse.Endpoint.Timeout = Duration(120 * time.Second)
	}
	if c.Rerank.Endpoint.Timeout.Std() <= 0 {
		c.Rerank.Endpoint.Timeout = Duration(30 * time.Second)
	}
	normalizeKeys(&c.Parse.Endpoint)
	normalizeKeys(&c.Embed.Endpoint)
	normalizeKeys(&c.Rerank.Endpoint)
	// Zotero 联动默认值。
	NormalizeZotero(&c.Zotero)
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.Format == "" {
		c.Log.Format = "text"
	}
}

// Validate 检查会导致启动即失败的关键配置。
//
// 注意：缺少 api_key 不视为启动错误——允许无密钥的本地/自建服务运行，
// 缺失时仅通过 MissingCredentials 给出告警，服务照常启动，直到真正调用外部
// 接口时才可能返回鉴权失败。这样可避免“一启动就退出”的假死现象。
func (c Config) Validate() error {
	// 向量化是入库流程的必需环节。
	if c.Embed.Endpoint.BaseURL == "" {
		return errors.New("config: embed.base_url 不能为空")
	}
	if c.Rerank.Enabled && c.Rerank.Endpoint.BaseURL == "" {
		return errors.New("config: rerank.base_url 不能为空")
	}
	if c.Parse.Provider != "plain" && c.Parse.Endpoint.BaseURL == "" {
		return fmt.Errorf("%w: parse", ErrMissingAPIKey)
	}
	return nil
}

// MissingCredentials 返回已启用但仍缺少 api_key 的服务名列表（用于启动告警）。
func (c Config) MissingCredentials() []string {
	var missing []string
	if c.Embed.Enabled && c.Embed.Endpoint.APIKey == "" {
		missing = append(missing, "embed")
	}
	if c.Rerank.Enabled && c.Rerank.Endpoint.APIKey == "" {
		missing = append(missing, "rerank")
	}
	if c.Parse.Provider != "plain" && c.Parse.Endpoint.APIKey == "" && c.Parse.Endpoint.BaseURL != "" {
		missing = append(missing, "parse")
	}
	return missing
}
