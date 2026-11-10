package api

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// uninstallMCPClient 取消指定客户端的 larder 注入，保留其它 MCP 配置。
func uninstallMCPClient(client, dataDir string) (mcpInstallResult, error) {
	switch client {
	case "claude-code":
		bin, err := findClaudeCLI()
		if err != nil {
			return mcpInstallResult{}, fmt.Errorf("未找到 claude 命令")
		}
		out, err := runClaudeRemove(bin)
		if err != nil {
			return mcpInstallResult{}, fmt.Errorf("claude mcp remove 失败：%v\n%s", err, strings.TrimSpace(out))
		}
		return mcpInstallResult{OK: true, Client: client, Message: "已从 Claude Code 移除 larder。"}, nil
	case "claude-desktop":
		path := claudeDesktopConfigPath()
		if err := removeJSONEntry(path, false); err != nil {
			return mcpInstallResult{}, err
		}
		return mcpInstallResult{OK: true, Client: client, Path: path, Message: "已从 Claude Desktop 配置移除 larder。"}, nil
	case "cursor":
		path := cursorConfigPath()
		if err := removeJSONEntry(path, false); err != nil {
			return mcpInstallResult{}, err
		}
		return mcpInstallResult{OK: true, Client: client, Path: path, Message: "已从 Cursor 配置移除 larder。"}, nil
	case "zcode":
		path := zcodeConfigPath()
		if err := removeJSONEntry(path, true); err != nil {
			return mcpInstallResult{}, err
		}
		return mcpInstallResult{OK: true, Client: client, Path: path, Message: "已从 ZCode 配置移除 larder；重启 ZCode 生效。"}, nil
	case "cherrystudio":
		path := cherryConfigPath(dataDir)
		if err := removeJSONEntry(path, false); err != nil {
			return mcpInstallResult{}, err
		}
		_ = os.Remove(path)
		return mcpInstallResult{OK: true, Client: client, Path: path, Message: "已删除生成的 Cherry 导入文件；已在 Cherry 中导入的条目请在 Cherry 设置 → MCP 里手动删除。"}, nil
	case "dsh":
		path := dshConfigPath()
		if err := removeDSHEntry(path); err != nil {
			return mcpInstallResult{}, err
		}
		return mcpInstallResult{OK: true, Client: client, Path: path, Message: "已从 DSH 配置移除 larder；新建会话后生效。"}, nil
	case "generic":
		return mcpInstallResult{OK: true, Client: client, Message: "通用配置没有写入本机，无需取消。"}, nil
	default:
		return mcpInstallResult{}, fmt.Errorf("不支持的客户端：%s", client)
	}
}

func runClaudeRemove(bin string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var last string
	for _, args := range [][]string{
		{"mcp", "remove", "larder", "--scope", "user"},
		{"mcp", "remove", "larder"},
	} {
		out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
		last = string(out)
		if err == nil || strings.Contains(strings.ToLower(last), "not found") {
			return last, nil
		}
	}
	return last, fmt.Errorf("remove failed")
}

func removeJSONEntry(path string, nested bool) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	obj, err := loadJSONObject(path)
	if err != nil {
		return err
	}
	if nested {
		if mcp, ok := obj["mcp"].(map[string]any); ok {
			if servers, ok := mcp["servers"].(map[string]any); ok {
				delete(servers, "larder")
			}
		}
	} else if servers, ok := obj["mcpServers"].(map[string]any); ok {
		delete(servers, "larder")
	}
	return writeJSONObject(path, obj)
}

func removeDSHEntry(path string) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	obj := map[string]any{}
	if strings.TrimSpace(string(b)) != "" {
		if err := yaml.Unmarshal(b, &obj); err != nil {
			return fmt.Errorf("%s 不是合法的 YAML，未做修改: %w", path, err)
		}
	}
	if mcp, ok := obj["mcp"].(map[string]any); ok {
		if servers, ok := mcp["servers"].(map[string]any); ok {
			delete(servers, "larder")
		}
	}
	out, err := yaml.Marshal(obj)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}
