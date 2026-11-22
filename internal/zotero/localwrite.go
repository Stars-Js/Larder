package zotero

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LocalWriteAuthorization 是 Zotero 本地写入授权的返回值。
type LocalWriteAuthorization struct {
	Key      string `json:"key"`
	Remember bool   `json:"remember"`
}

// APIItem 是本地 API 返回的条目（data 内是 Zotero 字段）。
type APIItem struct {
	Key  string         `json:"key"`
	Data map[string]any `json:"data"`
}

// ServerID 读取 Zotero 实例的 Zotero-Server-ID（本地写入必需）。
func (c *Client) ServerID(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIBase+"/", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Zotero-API-Version", "3")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	id := strings.TrimSpace(resp.Header.Get("Zotero-Server-ID"))
	if id == "" {
		return "", fmt.Errorf("Zotero 未返回 Zotero-Server-ID（需要 Zotero 10+）")
	}
	return id, nil
}

// AuthorizeLocalWrite 触发 Zotero 原生授权对话框，取得本地写入密钥。
// 建议用户在对话框中选择“始终允许”，否则密钥只能使用一次。
func (c *Client) AuthorizeLocalWrite(ctx context.Context, appName string) (LocalWriteAuthorization, error) {
	serverID, err := c.ServerID(ctx)
	if err != nil {
		return LocalWriteAuthorization{}, err
	}
	if strings.TrimSpace(appName) == "" {
		appName = "Larder"
	}
	body, _ := json.Marshal(map[string]string{"appName": appName})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.APIBase+"/local/authorize", bytes.NewReader(body))
	if err != nil {
		return LocalWriteAuthorization{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Zotero-API-Version", "3")
	req.Header.Set("Zotero-Server-ID", serverID)
	resp, err := c.http.Do(req)
	if err != nil {
		return LocalWriteAuthorization{}, err
	}
	defer resp.Body.Close()
	var out LocalWriteAuthorization
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode == http.StatusForbidden {
		return out, fmt.Errorf("Zotero 拒绝了写入授权")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return out, fmt.Errorf("Zotero 授权失败：HTTP %d", resp.StatusCode)
	}
	if strings.TrimSpace(out.Key) == "" {
		return out, fmt.Errorf("Zotero 授权未返回密钥")
	}
	c.cfg.APIKey = out.Key
	return out, nil
}

// Children 返回某个 Zotero 父条目的子条目。
func (c *Client) Children(ctx context.Context, parentKey string) ([]APIItem, error) {
	parentKey = strings.TrimSpace(parentKey)
	if parentKey == "" {
		return nil, fmt.Errorf("父条目 key 为空")
	}
	endpoint := fmt.Sprintf("%s/users/%s/items/%s/children?limit=100", c.cfg.APIBase, url.PathEscape(c.cfg.LibraryID), url.PathEscape(parentKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Zotero-API-Version", "3")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("读取 Zotero 子条目失败：HTTP %d", resp.StatusCode)
	}
	var out []APIItem
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// EnsureMarkdownLink 在 Zotero 词条下创建/更新指向解析 Markdown 的附件。
// 优先 linked_file（本地文件，不复制）；Zotero 不支持时回退 linked_url(file://)。
func (c *Client) EnsureMarkdownLink(ctx context.Context, parentKey, title, mdPath string) (string, bool, error) {
	parentKey = strings.TrimSpace(parentKey)
	mdPath = strings.TrimSpace(mdPath)
	if parentKey == "" || mdPath == "" {
		return "", false, fmt.Errorf("父条目或 Markdown 路径为空")
	}
	abs, err := filepath.Abs(mdPath)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", false, err
	}
	fileURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Larder Markdown"
	}
	children, err := c.Children(ctx, parentKey)
	if err != nil {
		return "", false, err
	}
	for _, child := range children {
		if child.Data == nil || fmt.Sprint(child.Data["itemType"]) != "attachment" {
			continue
		}
		if fmt.Sprint(child.Data["title"]) != title {
			continue
		}
		same := fmt.Sprint(child.Data["url"]) == fileURL ||
			fmt.Sprint(child.Data["path"]) == abs ||
			filepath.Clean(fmt.Sprint(child.Data["path"])) == filepath.Clean(abs)
		if same {
			return child.Key, false, nil
		}
		// 路径变了（例如标题变化）：删除旧附件，重新创建。
		if err := c.deleteItem(ctx, child.Key); err != nil {
			return "", false, err
		}
	}
	key, err := c.createAttachment(ctx, parentKey, title, abs, fileURL)
	if err != nil {
		return "", false, err
	}
	return key, true, nil
}

func (c *Client) createAttachment(ctx context.Context, parentKey, title, abs, fileURL string) (string, error) {
	// linked_file 不复制文件，Markdown 同目录的 image/ 可被 Zotero 打开。
	payload := map[string]any{
		"itemType":    "attachment",
		"parentItem":  parentKey,
		"linkMode":    "linked_file",
		"title":       title,
		"path":        abs,
		"contentType": "text/markdown",
	}
	key, status, err := c.postAttachment(ctx, payload)
	if err == nil {
		return key, nil
	}
	if status != http.StatusBadRequest {
		return "", err
	}
	// 某些 Zotero 版本只接受 linked_url；用 file:// 链接兜底。
	payload = map[string]any{
		"itemType":    "attachment",
		"parentItem":  parentKey,
		"linkMode":    "linked_url",
		"title":       title,
		"url":         fileURL,
		"contentType": "text/markdown",
	}
	key, _, err = c.postAttachment(ctx, payload)
	return key, err
}

func (c *Client) postAttachment(ctx context.Context, payload map[string]any) (string, int, error) {
	serverID, err := c.ServerID(ctx)
	if err != nil {
		return "", 0, err
	}
	if strings.TrimSpace(c.cfg.APIKey) == "" {
		return "", 0, fmt.Errorf("未授权 Zotero 本地写入；请先在「文献库」页点击“授权写入 Zotero”")
	}
	body, _ := json.Marshal([]map[string]any{payload})
	endpoint := fmt.Sprintf("%s/users/%s/items", c.cfg.APIBase, url.PathEscape(c.cfg.LibraryID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Zotero-API-Version", "3")
	req.Header.Set("Zotero-Server-ID", serverID)
	req.Header.Set("Zotero-API-Key", c.cfg.APIKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	rawBytes, _ := io.ReadAll(resp.Body)
	raw := strings.TrimSpace(string(rawBytes))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", resp.StatusCode, fmt.Errorf("创建 Zotero 附件失败：HTTP %d %s", resp.StatusCode, raw)
	}
	var result struct {
		Successful json.RawMessage `json:"successful"`
		Failed     json.RawMessage `json:"failed"`
	}
	if err := json.Unmarshal(rawBytes, &result); err != nil {
		return "", resp.StatusCode, fmt.Errorf("解析 Zotero 附件响应失败：%w", err)
	}
	if key := firstAttachmentKey(result.Successful); key != "" {
		return key, resp.StatusCode, nil
	}
	if msg := strings.TrimSpace(string(result.Failed)); msg != "" && msg != "{}" && msg != "[]" && msg != "null" {
		return "", resp.StatusCode, fmt.Errorf("Zotero 创建附件失败：%s", msg)
	}
	return "", resp.StatusCode, fmt.Errorf("Zotero 未创建附件：%s", raw)
}

// firstAttachmentKey 兼容本地 API 的两种 successful 形状：
// {"0":{"key":"..."}} 和 [{"key":"..."}]，以及直接 {"key":"..."}。
func firstAttachmentKey(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" || string(raw) == "[]" {
		return ""
	}
	var one struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &one); err == nil && strings.TrimSpace(one.Key) != "" {
		return one.Key
	}
	var arr []struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil {
		for _, it := range arr {
			if strings.TrimSpace(it.Key) != "" {
				return it.Key
			}
		}
	}
	var obj map[string]struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		for _, it := range obj {
			if strings.TrimSpace(it.Key) != "" {
				return it.Key
			}
		}
	}
	return ""
}

func (c *Client) deleteItem(ctx context.Context, key string) error {
	serverID, err := c.ServerID(ctx)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/users/%s/items/%s", c.cfg.APIBase, url.PathEscape(c.cfg.LibraryID), url.PathEscape(key))
	// 先读取条目版本，Zotero 删除要求 If-Unmodified-Since-Version。
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Zotero-API-Version", "3")
	req.Header.Set("Zotero-Server-ID", serverID)
	req.Header.Set("Zotero-API-Key", c.cfg.APIKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	rawBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("读取 Zotero 附件版本失败：HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(rawBytes)))
	}
	var item struct {
		Version int `json:"version"`
		Data    struct {
			Version int `json:"version"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rawBytes, &item)
	version := item.Version
	if version == 0 {
		version = item.Data.Version
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Zotero-API-Version", "3")
	req.Header.Set("Zotero-Server-ID", serverID)
	req.Header.Set("Zotero-API-Key", c.cfg.APIKey)
	if version > 0 {
		req.Header.Set("If-Unmodified-Since-Version", strconv.Itoa(version))
	}
	resp, err = c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("删除旧 Zotero 附件失败：HTTP %d", resp.StatusCode)
	}
	return nil
}
