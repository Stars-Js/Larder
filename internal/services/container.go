// Package services 持有当前生效的解析/向量/重排三个外部服务实例。
// 设置变更后可热重建，无需重启进程；各知识库共享同一套服务实例。
package services

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"larder/internal/config"
	"larder/internal/provider/embed"
	"larder/internal/provider/parse"
	"larder/internal/provider/rerank"
)

// Container 管理三个服务实例，支持热更新与并发快照。
type Container struct {
	mu     sync.RWMutex
	cfg    config.Config
	parse  parse.Parser
	embed  embed.Embedder
	rerank rerank.Reranker // 可能为 nil
	logger *slog.Logger
}

// New 依据配置构建服务容器。
func New(cfg config.Config, logger *slog.Logger) (*Container, error) {
	c := &Container{cfg: cfg, logger: logger}
	if err := c.rebuild(cfg); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Container) rebuild(cfg config.Config) error {
	parser, err := BuildParser(cfg)
	if err != nil {
		return err
	}
	emb := embed.NewOpenAI(
		cfg.Embed.Endpoint.BaseURL,
		cfg.Embed.Endpoint.APIKey,
		cfg.Embed.Endpoint.Model,
		cfg.Embed.Endpoint.Path,
		cfg.Embed.Dim,
		cfg.Embed.Endpoint.Timeout.Std(),
		cfg.Embed.Endpoint.MaxRetries,
		cfg.Embed.Endpoint.QPS,
	)
	var rr rerank.Reranker
	if cfg.Rerank.Enabled {
		rr = rerank.NewOpenAI(
			cfg.Rerank.Endpoint.BaseURL,
			cfg.Rerank.Endpoint.APIKey,
			cfg.Rerank.Endpoint.Model,
			cfg.Rerank.Endpoint.Path,
			cfg.Rerank.Endpoint.Timeout.Std(),
			cfg.Rerank.Endpoint.MaxRetries,
			cfg.Rerank.Endpoint.QPS,
		)
	}
	c.parse = parser
	c.embed = emb
	c.rerank = rr
	c.cfg = cfg
	return nil
}

// Update 用新配置热重建服务。
func (c *Container) Update(cfg config.Config) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rebuild(cfg)
}

// Snapshot 返回三个服务实例的快照。
func (c *Container) Snapshot() (parse.Parser, embed.Embedder, rerank.Reranker) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.parse, c.embed, c.rerank
}

// Config 返回当前生效配置。
func (c *Container) Config() config.Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg
}

// DetectParseProvider 根据 Base URL 自动识别外部解析服务协议。
// 无法识别时返回空字符串，由调用方决定是否保留原 provider 或默认 mineru。
func DetectParseProvider(baseURL string) string {
	l := strings.ToLower(strings.TrimSpace(baseURL))
	switch {
	case l == "":
		return "plain"
	case strings.Contains(l, "mineru"):
		return "mineru"
	case strings.Contains(l, "docling"):
		return "docling"
	case strings.Contains(l, "tika"):
		return "tika"
	}
	return ""
}

// BuildParser 依据配置构造解析器。
//
// plain 只用内置解析（文本 / 文字版 PDF / DOCX / PPTX / XLSX / EPUB / HTML）。
// mineru / docling / tika 则使用「本地优先 + 外部服务兜底」的混合解析：
// 普通文本和文字版 PDF 仍在本地完成，扫描件、矢量图形 PDF、复杂版式才上传
// 到外部服务——这样配置一次外部服务即可覆盖所有文件，而不会把所有文本都上传。
func BuildParser(cfg config.Config) (parse.Parser, error) {
	e := cfg.Parse.Endpoint
	local := parse.NewPlain()
	switch cfg.Parse.Provider {
	case "", "plain":
		return local, nil
	case "mineru":
		m := parse.NewMinerU(e.BaseURL, e.APIKey, e.Path, e.Timeout.Std(), e.MaxRetries, e.QPS)
		if e.Model != "" {
			m.WithModelVersion(e.Model)
		}
		return parse.NewHybrid(local, m), nil
	case "docling":
		d := parse.NewDocling(e.BaseURL, e.APIKey, e.Path, e.Timeout.Std(), e.MaxRetries, e.QPS)
		return parse.NewHybrid(local, d), nil
	case "tika":
		t := parse.NewTika(e.BaseURL, e.APIKey, e.Path, e.Timeout.Std(), e.MaxRetries, e.QPS)
		return parse.NewHybrid(local, t), nil
	default:
		return nil, fmt.Errorf("未知的 parse.provider: %q（可选 plain/mineru/docling/tika）", cfg.Parse.Provider)
	}
}

// ServiceDTO 描述一个外部服务（面向前端 JSON）。
// APIKey/APIKeys 只在保存时由前端提交；读取时只返回掩码，避免密钥回显。
type ServiceDTO struct {
	Enabled       bool     `json:"enabled"`
	Provider      string   `json:"provider"`
	BaseURL       string   `json:"base_url"`
	APIKey        string   `json:"api_key,omitempty"`
	APIKeys       []string `json:"api_keys,omitempty"` // 多密钥；第一个为当前使用
	APIKeyMasked  string   `json:"api_key_masked,omitempty"`
	APIKeysMasked []string `json:"api_keys_masked,omitempty"`
	APIKeyCount   int      `json:"api_key_count"`
	HasAPIKey     bool     `json:"has_api_key"`
	ClearAPIKey   bool     `json:"clear_api_key,omitempty"`
	Model         string   `json:"model"`
	Path          string   `json:"path"`
	Dim           int      `json:"dim"`
	Timeout       string   `json:"timeout"`
	MaxRetries    int      `json:"max_retries"`
	QPS           float64  `json:"qps"`
}

// ToServiceDTO 把配置中的 Provider 转为 DTO（不包含明文密钥）。
func ToServiceDTO(p config.Provider) ServiceDTO {
	keys := p.Endpoint.APIKeys
	if len(keys) == 0 && p.Endpoint.APIKey != "" {
		keys = []string{p.Endpoint.APIKey}
	}
	masked := make([]string, len(keys))
	for i, k := range keys {
		masked[i] = maskKey(k)
	}
	firstMasked := ""
	if len(masked) > 0 {
		firstMasked = masked[0]
	}
	return ServiceDTO{
		Enabled:       p.Enabled,
		Provider:      p.Provider,
		BaseURL:       p.Endpoint.BaseURL,
		APIKeyMasked:  firstMasked,
		APIKeysMasked: masked,
		APIKeyCount:   len(keys),
		HasAPIKey:     len(keys) > 0,
		Model:         p.Endpoint.Model,
		Path:          p.Endpoint.Path,
		Dim:           p.Dim,
		Timeout:       time.Duration(p.Endpoint.Timeout).String(),
		MaxRetries:    p.Endpoint.MaxRetries,
		QPS:           p.Endpoint.QPS,
	}
}

// maskKey 只保留密钥首尾，用于界面提示。
func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return "••••••••"
	}
	return k[:2] + "••••••••" + k[len(k)-4:]
}

// ApplyService 把 DTO 写回配置中的 Provider。
// api_keys 非 nil 时整体替换密钥列表（第一个为当前使用）；
// api_key 非空时把它放到列表首位；clear_api_key=true 时清空。
func ApplyService(d ServiceDTO, p config.Provider) (config.Provider, error) {
	p.Enabled = d.Enabled
	if d.Provider != "" {
		p.Provider = d.Provider
	}
	p.Endpoint.BaseURL = strings.TrimSpace(d.BaseURL)
	if d.ClearAPIKey {
		p.Endpoint.APIKey = ""
		p.Endpoint.APIKeys = nil
	} else if d.APIKeys != nil {
		p.Endpoint.APIKeys = cleanKeys(d.APIKeys)
		if len(p.Endpoint.APIKeys) > 0 {
			p.Endpoint.APIKey = p.Endpoint.APIKeys[0]
		} else {
			p.Endpoint.APIKey = ""
		}
	} else if k := strings.TrimSpace(d.APIKey); k != "" {
		keys := []string{k}
		for _, old := range p.Endpoint.APIKeys {
			if old != k {
				keys = append(keys, old)
			}
		}
		p.Endpoint.APIKeys = keys
		p.Endpoint.APIKey = k
	}
	p.Endpoint.Model = strings.TrimSpace(d.Model)
	p.Endpoint.Path = strings.TrimSpace(d.Path)
	p.Endpoint.MaxRetries = d.MaxRetries
	p.Endpoint.QPS = d.QPS
	p.Dim = d.Dim
	if d.Timeout != "" {
		t, err := time.ParseDuration(d.Timeout)
		if err != nil {
			return p, fmt.Errorf("非法的 timeout %q: %w", d.Timeout, err)
		}
		p.Endpoint.Timeout = config.Duration(t)
	}
	return p, nil
}

func cleanKeys(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, k := range in {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}
