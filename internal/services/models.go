package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// FetchModels 向 OpenAI 兼容服务的 /v1/models 拉取模型列表。
// baseURL/apiKey 由前端即时提供（用于“填完服务商和 Key 后自动获取模型”）。
// kind 为 embed / rerank 时只保留对应类型的模型，避免把聊天、图像、语音模型混进下拉框。
func FetchModels(ctx context.Context, baseURL, apiKey, path, kind string) ([]string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("请先填写 Base URL")
	}
	url := modelsURL(baseURL, path)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Accept", "application/json")

	cli := &http.Client{Timeout: 15 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 %s 失败: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		hint := ""
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			hint = "（API Key 无效或无权限）"
		}
		return nil, fmt.Errorf("%s 返回 HTTP %d%s: %s", url, resp.StatusCode, hint, strings.TrimSpace(string(body)))
	}

	// 兼容 {"data":[{"id":...}]}、{"models":[{"id":...}]}、"models":["..."]。
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []json.RawMessage `json:"models"`
	}
	if err := decodeJSONBody(resp, &payload); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, d := range payload.Data {
		add(d.ID)
	}
	for _, raw := range payload.Models {
		var id string
		if err := json.Unmarshal(raw, &id); err == nil {
			add(id)
			continue
		}
		var obj struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &obj); err == nil {
			add(firstNonEmpty(obj.ID, obj.Name))
		}
	}
	out = filterModels(out, kind)
	sort.Strings(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("服务返回了空的模型列表，请检查 Base URL 是否为 OpenAI 兼容接口")
	}
	return out, nil
}

// filterModels 按用途筛选模型；无法识别时保留原列表，保证自定义服务仍可用。
func filterModels(models []string, kind string) []string {
	if kind != "embed" && kind != "rerank" {
		return models
	}
	out := make([]string, 0, len(models))
	for _, id := range models {
		if kind == "rerank" && isRerankModel(id) {
			out = append(out, id)
		} else if kind == "embed" && isEmbeddingModel(id) {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return models
	}
	return out
}

func isRerankModel(id string) bool {
	low := strings.ToLower(id)
	return strings.Contains(low, "rerank") || strings.Contains(low, "ranker")
}

func isEmbeddingModel(id string) bool {
	low := strings.ToLower(id)
	if isRerankModel(low) {
		return false
	}
	for _, token := range []string{"embedding", "embed", "bge-", "bge_", "m3e", "gte-", "e5-", "voyage", "jina", "nomic"} {
		if strings.Contains(low, token) {
			return true
		}
	}
	return false
}

// modelsURL 拼出模型列表地址。Base URL 常写成 https://host 或 https://host/v1：
// 后者直接补 /models，前者补 /v1/models。
func modelsURL(baseURL, path string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.TrimSpace(path) != "" {
		p := strings.TrimSpace(path)
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		return base + p
	}
	lower := strings.ToLower(base)
	if strings.HasSuffix(lower, "/v1") || strings.HasSuffix(lower, "/v4") ||
		strings.HasSuffix(lower, "/v1beta") {
		return base + "/models"
	}
	return base + "/v1/models"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func decodeJSONBody(resp *http.Response, v any) error {
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("解析模型列表失败: %w", err)
	}
	return nil
}
