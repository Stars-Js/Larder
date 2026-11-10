// Package api — MCP 一键接入 Agent。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// mcpClientInfo 是前端可选的 Agent 客户端。
type mcpClientInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Detail    string `json:"detail"`
	Path      string `json:"path,omitempty"`
	Config    any    `json:"config,omitempty"`
	Installed bool   `json:"installed"`
}

type mcpInstallResult struct {
	OK      bool   `json:"ok"`
	Client  string `json:"client"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Config  any    `json:"config,omitempty"`
}

func (d Deps) mcpEndpoint() string {
	if strings.TrimSpace(d.MCPURL) != "" {
		return d.MCPURL
	}
	cfg := d.Services.Config()
	return "http://" + cfg.Server.Addr + cfg.Server.MCPPath
}

func listMCPClients(dataDir, url string) []mcpClientInfo {
	claudeBin, _ := findClaudeCLI()
	claudeDetail := "未找到 claude 命令；请先安装 Claude Code CLI"
	if claudeBin != "" {
		claudeDetail = "已找到：" + claudeBin + "；将执行 claude mcp add"
	}
	npx, _ := exec.LookPath("npx")
	npxNote := "未找到 Node.js/npx，请先安装"
	if npx != "" {
		npxNote = "已找到 npx"
	}
	claudeInstalled := false
	if claudeBin != "" {
		claudeInstalled = claudeHasLarder(claudeBin)
	}
	return []mcpClientInfo{
		{ID: "claude-code", Name: "Claude Code", Available: claudeBin != "", Detail: claudeDetail, Config: clientConfig("claude-code", withClient(url, "claude-code")), Installed: claudeInstalled},
		{ID: "claude-desktop", Name: "Claude Desktop", Available: npx != "", Detail: "写入 claude_desktop_config.json；通过 mcp-remote 桥接（" + npxNote + "）", Path: claudeDesktopConfigPath(), Config: clientConfig("claude-desktop", withClient(url, "claude-desktop")), Installed: jsonHasLarderURL(claudeDesktopConfigPath(), false, "client=claude-desktop")},
		{ID: "cursor", Name: "Cursor", Available: true, Detail: "写入 ~/.cursor/mcp.json（远程 HTTP MCP）", Path: cursorConfigPath(), Config: clientConfig("cursor", withClient(url, "cursor")), Installed: jsonHasLarderURL(cursorConfigPath(), false, "client=cursor")},
		{ID: "zcode", Name: "ZCode", Available: true, Detail: "写入 ~/.zcode/cli/config.json 的 mcp.servers（HTTP）", Path: zcodeConfigPath(), Config: clientConfig("zcode", withClient(url, "zcode")), Installed: jsonHasLarderURL(zcodeConfigPath(), true, "client=zcode")},
		{ID: "cherrystudio", Name: "Cherry Studio", Available: true, Detail: "生成导入 JSON（type=streamableHttp）；在 Cherry Studio → 设置 → MCP → 从 JSON 导入", Path: cherryConfigPath(dataDir), Config: clientConfig("cherrystudio", withClient(url, "cherrystudio")), Installed: false},
		{ID: "dsh", Name: "DSH", Available: true, Detail: "写入 ~/.dsh/settings.yaml 的 mcp.servers（通过 npx mcp-remote）", Path: dshConfigPath(), Config: clientConfig("dsh", withClient(url, "dsh")), Installed: dshHasLarderURL(dshConfigPath(), "client=dsh")},
		{ID: "generic", Name: "通用配置", Available: true, Detail: "生成 JSON；可复制到任意支持 MCP 的客户端", Config: genericMCPConfig(url)},
	}
}

func installMCPClient(client, url, dataDir string) (mcpInstallResult, error) {
	clientURL := withClient(url, client)
	if client == "generic" {
		clientURL = url
	}
	switch client {
	case "claude-code":
		bin, err := findClaudeCLI()
		if err != nil {
			return mcpInstallResult{}, fmt.Errorf("未找到 claude 命令：请先安装 Claude Code CLI，或使用通用配置手动接入")
		}
		out, err := runClaudeAdd(bin, clientURL)
		if err != nil {
			return mcpInstallResult{}, fmt.Errorf("claude mcp add 失败：%v\n%s", err, strings.TrimSpace(out))
		}
		return mcpInstallResult{
			OK: true, Client: client,
			Message: "已写入 Claude Code 用户级 MCP 配置；重启 Claude Code 后生效。",
		}, nil
	case "claude-desktop":
		if _, err := exec.LookPath("npx"); err != nil {
			return mcpInstallResult{}, fmt.Errorf("未找到 npx，请先安装 Node.js；Claude Desktop 需要通过 mcp-remote 桥接远程 MCP")
		}
		path := claudeDesktopConfigPath()
		if err := installClaudeDesktop(path, clientURL); err != nil {
			return mcpInstallResult{}, err
		}
		return mcpInstallResult{
			OK: true, Client: client, Path: path,
			Message: "已写入 Claude Desktop 配置；重启 Claude Desktop 后生效（需要 Node.js/npx）。",
		}, nil
	case "cursor":
		path := cursorConfigPath()
		if err := installCursor(path, clientURL); err != nil {
			return mcpInstallResult{}, err
		}
		return mcpInstallResult{
			OK: true, Client: client, Path: path,
			Message: "已写入 Cursor 配置；重启 Cursor 后生效。",
		}, nil
	case "zcode":
		path := zcodeConfigPath()
		if err := installZCode(path, clientURL); err != nil {
			return mcpInstallResult{}, err
		}
		return mcpInstallResult{
			OK: true, Client: client, Path: path, Config: clientConfig(client, clientURL),
			Message: "已写入 ZCode 的 mcp.servers；重启 ZCode 后生效。",
		}, nil
	case "cherrystudio":
		path := cherryConfigPath(dataDir)
		cfg, err := installCherryStudio(path, clientURL)
		if err != nil {
			return mcpInstallResult{}, err
		}
		return mcpInstallResult{
			OK: true, Client: client, Path: path, Config: cfg,
			Message: "已生成 Cherry Studio 导入文件（关键字段 type=streamableHttp）。请在 Cherry Studio → 设置 → MCP → 从 JSON 导入该文件，再启用服务。",
		}, nil
	case "dsh":
		path := dshConfigPath()
		if err := installDSH(path, clientURL); err != nil {
			return mcpInstallResult{}, err
		}
		return mcpInstallResult{
			OK: true, Client: client, Path: path,
			Message: "已写入 DSH 的 mcp.servers；新建 DSH 会话后生效（通过 npx mcp-remote）。",
		}, nil
	case "generic":
		cfg := genericMCPConfig(clientURL)
		return mcpInstallResult{
			OK: true, Client: client,
			Message: "复制下面的 JSON，粘贴到你的 MCP 客户端配置中。",
			Config:  cfg,
		}, nil
	default:
		return mcpInstallResult{}, fmt.Errorf("不支持的客户端：%s", client)
	}
}

// MigrateInjectedClients 给已注入的客户端 URL 补上 ?client=<平台>，
// 让平台级引用格式对旧配置也生效；不新增/删除任何客户端条目。
func MigrateInjectedClients(url, dataDir string) {
	migrateJSONClient(zcodeConfigPath(), true, "zcode", url)
	migrateJSONClient(cursorConfigPath(), false, "cursor", url)
	migrateMCPRemoteJSON(claudeDesktopConfigPath(), "claude-desktop", url)
	migrateMCPRemoteYAML(dshConfigPath(), "dsh", url)
	migrateJSONClient(cherryConfigPath(dataDir), false, "cherrystudio", url)
}

func migrateJSONClient(path string, nested bool, client, url string) {
	if !jsonHasLarder(path, nested) {
		return
	}
	obj, err := loadJSONObject(path)
	if err != nil {
		return
	}
	var entry map[string]any
	if nested {
		mcp, _ := obj["mcp"].(map[string]any)
		servers, _ := mcp["servers"].(map[string]any)
		entry, _ = servers["larder"].(map[string]any)
	} else {
		servers, _ := obj["mcpServers"].(map[string]any)
		entry, _ = servers["larder"].(map[string]any)
	}
	if entry == nil {
		return
	}
	raw, _ := entry["url"].(string)
	if strings.Contains(raw, "client="+client) {
		return
	}
	entry["url"] = withClient(url, client)
	_ = writeJSONObject(path, obj)
}

func migrateMCPRemoteJSON(path, client, url string) {
	if !jsonHasLarder(path, false) {
		return
	}
	obj, err := loadJSONObject(path)
	if err != nil {
		return
	}
	servers, _ := obj["mcpServers"].(map[string]any)
	entry, _ := servers["larder"].(map[string]any)
	if entry == nil {
		return
	}
	args, _ := entry["args"].([]any)
	if len(args) < 3 {
		return
	}
	if raw, _ := args[2].(string); strings.Contains(raw, "client="+client) {
		return
	}
	args[2] = withClient(url, client)
	entry["args"] = args
	_ = writeJSONObject(path, obj)
}

func migrateMCPRemoteYAML(path, client, url string) {
	if !dshHasLarder(path) {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	obj := map[string]any{}
	if yaml.Unmarshal(b, &obj) != nil {
		return
	}
	mcp, _ := obj["mcp"].(map[string]any)
	servers, _ := mcp["servers"].(map[string]any)
	entry, _ := servers["larder"].(map[string]any)
	if entry == nil {
		return
	}
	args, _ := entry["args"].([]any)
	if len(args) < 3 {
		return
	}
	if raw, _ := args[2].(string); strings.Contains(raw, "client="+client) {
		return
	}
	args[2] = withClient(url, client)
	entry["args"] = args
	out, err := yaml.Marshal(obj)
	if err == nil {
		_ = os.WriteFile(path, out, 0o644)
	}
}

func jsonHasLarder(path string, nested bool) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var obj map[string]any
	if json.Unmarshal(b, &obj) != nil {
		return false
	}
	if nested {
		mcp, _ := obj["mcp"].(map[string]any)
		servers, _ := mcp["servers"].(map[string]any)
		_, ok := servers["larder"]
		return ok
	}
	servers, _ := obj["mcpServers"].(map[string]any)
	_, ok := servers["larder"]
	return ok
}

func jsonHasLarderURL(path string, nested bool, needle string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var obj map[string]any
	if json.Unmarshal(b, &obj) != nil {
		return false
	}
	var entry map[string]any
	if nested {
		mcp, _ := obj["mcp"].(map[string]any)
		servers, _ := mcp["servers"].(map[string]any)
		entry, _ = servers["larder"].(map[string]any)
	} else {
		servers, _ := obj["mcpServers"].(map[string]any)
		entry, _ = servers["larder"].(map[string]any)
	}
	if entry == nil {
		return false
	}
	raw, _ := entry["url"].(string)
	return strings.Contains(raw, needle)
}

func dshHasLarderURL(path, needle string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var obj map[string]any
	if yaml.Unmarshal(b, &obj) != nil {
		return false
	}
	mcp, _ := obj["mcp"].(map[string]any)
	servers, _ := mcp["servers"].(map[string]any)
	entry, _ := servers["larder"].(map[string]any)
	if entry == nil {
		return false
	}
	raw, _ := json.Marshal(entry)
	return strings.Contains(string(raw), needle)
}

func dshHasLarder(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var obj map[string]any
	if yaml.Unmarshal(b, &obj) != nil {
		return false
	}
	mcp, _ := obj["mcp"].(map[string]any)
	servers, _ := mcp["servers"].(map[string]any)
	_, ok := servers["larder"]
	return ok
}

func claudeHasLarder(bin string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "mcp", "list").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), "larder")
}

func withClient(raw, client string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" {
		return raw
	}
	q := u.Query()
	q.Set("client", client)
	u.RawQuery = q.Encode()
	return u.String()
}

func clientConfig(client, url string) map[string]any {
	switch client {
	case "claude-desktop":
		return map[string]any{"mcpServers": map[string]any{"larder": map[string]any{
			"command": "npx", "args": []any{"-y", "mcp-remote", url},
		}}}
	case "dsh":
		return map[string]any{"mcp": map[string]any{"servers": map[string]any{"larder": map[string]any{
			"command": "npx", "args": []any{"-y", "mcp-remote", url}, "env": map[string]any{}, "enabled": true,
		}}}}
	case "zcode":
		return map[string]any{"mcp": map[string]any{"servers": map[string]any{"larder": map[string]any{
			"type": "http", "url": url, "enabled": true,
		}}}}
	case "cherrystudio":
		return map[string]any{"mcpServers": map[string]any{"larder": map[string]any{
			"type": "streamableHttp", "url": url, "longRunning": true, "timeout": 120,
		}}}
	default:
		return genericMCPConfig(url)
	}
}

func findClaudeCLI() (string, error) {
	for _, name := range []string{"claude", "claude.cmd", "claude.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}

func runClaudeAdd(bin, url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// 先尝试用户级作用域；旧版 CLI 不支持 --scope 时再退回默认作用域。
	args := []string{"mcp", "add", "--transport", "http", "larder", url, "--scope", "user"}
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err == nil {
		return string(out), nil
	}
	args = []string{"mcp", "add", "--transport", "http", "larder", url}
	out2, err2 := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err2 == nil {
		return string(out2), nil
	}
	return string(out) + "\n" + string(out2), err2
}

func claudeDesktopConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		home, _ := os.UserHomeDir()
		dir = home
	}
	return filepath.Join(dir, "Claude", "claude_desktop_config.json")
}

func cursorConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cursor", "mcp.json")
}

func zcodeConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".zcode", "cli", "config.json")
}

func dshConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".dsh", "settings.yaml")
}

func cherryConfigPath(dataDir string) string {
	if strings.TrimSpace(dataDir) == "" {
		dataDir = "data"
	}
	return filepath.Join(dataDir, "mcp-cherrystudio.json")
}

func genericMCPConfig(url string) map[string]any {
	return map[string]any{
		"mcpServers": map[string]any{
			"larder": map[string]any{"url": url},
		},
	}
}

func installClaudeDesktop(path, url string) error {
	obj, err := loadJSONObject(path)
	if err != nil {
		return err
	}
	servers := mcpServersMap(obj)
	servers["larder"] = map[string]any{
		"command": "npx",
		"args":    []any{"-y", "mcp-remote", url},
	}
	return writeJSONObject(path, obj)
}

func installCursor(path, url string) error {
	obj, err := loadJSONObject(path)
	if err != nil {
		return err
	}
	servers := mcpServersMap(obj)
	servers["larder"] = map[string]any{"url": url}
	return writeJSONObject(path, obj)
}

func installZCode(path, url string) error {
	obj, err := loadJSONObject(path)
	if err != nil {
		return err
	}
	mcp, _ := obj["mcp"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
		obj["mcp"] = mcp
	}
	servers, _ := mcp["servers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
		mcp["servers"] = servers
	}
	servers["larder"] = map[string]any{"type": "http", "url": url, "enabled": true}
	return writeJSONObject(path, obj)
}

func installCherryStudio(path, url string) (map[string]any, error) {
	obj, err := loadJSONObject(path)
	if err != nil {
		return nil, err
	}
	servers := mcpServersMap(obj)
	servers["larder"] = map[string]any{
		"type":        "streamableHttp",
		"url":         url,
		"longRunning": true,
		"timeout":     120,
	}
	if err := writeJSONObject(path, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func installDSH(path, url string) error {
	obj := map[string]any{}
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	if err == nil && strings.TrimSpace(string(b)) != "" {
		if err := yaml.Unmarshal(b, &obj); err != nil {
			return fmt.Errorf("%s 不是合法的 YAML，未做修改: %w", path, err)
		}
	}
	if obj == nil {
		obj = map[string]any{}
	}
	mcp, _ := obj["mcp"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
		obj["mcp"] = mcp
	}
	servers, _ := mcp["servers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
		mcp["servers"] = servers
	}
	servers["larder"] = map[string]any{
		"command": "npx",
		"args":    []any{"-y", "mcp-remote", url},
		"env":     map[string]any{},
		"enabled": true,
	}
	out, err := yaml.Marshal(obj)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	return nil
}

func loadJSONObject(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	if strings.TrimSpace(string(b)) == "" {
		return map[string]any{}, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, fmt.Errorf("%s 不是合法的 JSON 对象，未做修改: %w", path, err)
	}
	if obj == nil {
		obj = map[string]any{}
	}
	return obj, nil
}

func writeJSONObject(path string, obj any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	b, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	return nil
}

func mcpServersMap(obj map[string]any) map[string]any {
	if v, ok := obj["mcpServers"].(map[string]any); ok {
		return v
	}
	m := map[string]any{}
	obj["mcpServers"] = m
	return m
}
