package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestInstallClaudeDesktopMergesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude_desktop_config.json")
	existing := `{"mcpServers":{"other":{"command":"node","args":["x.js"]}},"theme":"dark"}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	url := "http://127.0.0.1:8765/mcp"
	if err := installClaudeDesktop(path, url); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["theme"] != "dark" {
		t.Fatal("existing top-level keys should be preserved")
	}
	servers := obj["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatal("existing mcpServers should be preserved")
	}
	larder := servers["larder"].(map[string]any)
	if larder["command"] != "npx" {
		t.Fatalf("unexpected command: %+v", larder)
	}
	args, _ := larder["args"].([]any)
	if len(args) != 3 || args[2] != url {
		t.Fatalf("unexpected args: %+v", args)
	}
}

func TestInstallCursorMergesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"other":{"url":"http://x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	url := "http://127.0.0.1:8765/mcp"
	if err := installCursor(path, url); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		t.Fatal(err)
	}
	servers := obj["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatal("existing mcpServers should be preserved")
	}
	larder := servers["larder"].(map[string]any)
	if larder["url"] != url {
		t.Fatalf("unexpected url: %+v", larder)
	}
}

func TestInstallRejectsInvalidJSONWithoutOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	bad := []byte("{not json")
	if err := os.WriteFile(path, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installCursor(path, "http://127.0.0.1:8765/mcp"); err == nil {
		t.Fatal("invalid JSON should return an error")
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(bad) {
		t.Fatal("invalid config must not be overwritten")
	}
}

func TestInstallZCodeMergesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"theme":"dark","mcp":{"servers":{"other":{"url":"http://x"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	url := "http://127.0.0.1:8765/mcp"
	if err := installZCode(path, url); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["theme"] != "dark" {
		t.Fatal("existing top-level keys should be preserved")
	}
	mcp := obj["mcp"].(map[string]any)
	servers := mcp["servers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatal("existing mcp servers should be preserved")
	}
	larder := servers["larder"].(map[string]any)
	if larder["url"] != url || larder["enabled"] != true || larder["type"] != "http" {
		t.Fatalf("unexpected zcode entry: %+v", larder)
	}
}

func TestInstallCherryStudioUsesStreamableHTTP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp-cherrystudio.json")
	url := "http://127.0.0.1:8765/mcp"
	cfg, err := installCherryStudio(path, url)
	if err != nil {
		t.Fatal(err)
	}
	servers := cfg["mcpServers"].(map[string]any)
	larder := servers["larder"].(map[string]any)
	if larder["type"] != "streamableHttp" || larder["url"] != url {
		t.Fatalf("cherry entry must include type=streamableHttp: %+v", larder)
	}
}

func TestInstallDSHMergesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")
	existing := "llm-pi-ai:\n  providers:\n    magpie:\n      baseURL: http://127.0.0.1:3425/v1\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	url := "http://127.0.0.1:8765/mcp"
	if err := installDSH(path, url); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var obj map[string]any
	if err := yaml.Unmarshal(b, &obj); err != nil {
		t.Fatal(err)
	}
	if _, ok := obj["llm-pi-ai"]; !ok {
		t.Fatal("existing settings should be preserved")
	}
	mcp := obj["mcp"].(map[string]any)
	servers := mcp["servers"].(map[string]any)
	larder := servers["larder"].(map[string]any)
	if larder["command"] != "npx" || larder["enabled"] != true {
		t.Fatalf("unexpected dsh entry: %+v", larder)
	}
	args, _ := larder["args"].([]any)
	if len(args) != 3 || args[2] != url {
		t.Fatalf("unexpected dsh args: %+v", args)
	}
}

func TestClientConfigs(t *testing.T) {
	url := "http://127.0.0.1:8765/mcp"
	z := clientConfig("zcode", url)
	servers := z["mcp"].(map[string]any)["servers"].(map[string]any)
	larder := servers["larder"].(map[string]any)
	if larder["type"] != "http" || larder["url"] != url || larder["enabled"] != true {
		t.Fatalf("unexpected zcode config: %+v", larder)
	}
	c := clientConfig("cherrystudio", url)
	cl := c["mcpServers"].(map[string]any)["larder"].(map[string]any)
	if cl["type"] != "streamableHttp" || cl["url"] != url {
		t.Fatalf("unexpected cherry config: %+v", cl)
	}
}

func TestRemoveJSONEntryPreservesOthers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"larder":{"url":"http://x"},"other":{"url":"http://y"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeJSONEntry(path, false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		t.Fatal(err)
	}
	servers := obj["mcpServers"].(map[string]any)
	if _, ok := servers["larder"]; ok {
		t.Fatal("larder should be removed")
	}
	if _, ok := servers["other"]; !ok {
		t.Fatal("other server should be preserved")
	}
}

func TestRemoveJSONEntryNested(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"mcp":{"servers":{"larder":{"url":"http://x"},"other":{"url":"http://y"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeJSONEntry(path, true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		t.Fatal(err)
	}
	servers := obj["mcp"].(map[string]any)["servers"].(map[string]any)
	if _, ok := servers["larder"]; ok {
		t.Fatal("larder should be removed")
	}
	if _, ok := servers["other"]; !ok {
		t.Fatal("other server should be preserved")
	}
}

func TestRemoveDSHEntryPreservesOthers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")
	if err := os.WriteFile(path, []byte("llm-pi-ai:\n  providers:\n    x: {}\nmcp:\n  servers:\n    larder: {command: npx}\n    other: {command: x}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeDSHEntry(path); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var obj map[string]any
	if err := yaml.Unmarshal(b, &obj); err != nil {
		t.Fatal(err)
	}
	servers := obj["mcp"].(map[string]any)["servers"].(map[string]any)
	if _, ok := servers["larder"]; ok {
		t.Fatal("larder should be removed")
	}
	if _, ok := servers["other"]; !ok {
		t.Fatal("other server should be preserved")
	}
}
