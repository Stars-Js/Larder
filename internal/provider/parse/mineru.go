package parse

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"larder/internal/httpx"
)

// MinerU 解析器支持两种后端：
//
//  1. 官方云 API（mineru.net，/api/v4）：先申请上传链接并 PUT 文件，
//     系统自动排队解析；再按 batch_id 轮询任务，完成后下载结果 zip，
//     从中取出 full.md。适用于扫描件/复杂版式，支持 ≤200MB、≤200 页。
//  2. 自建服务（如 MinerU 本地部署的 /file_parse）：直接 multipart 上传，
//     响应里返回 markdown。
//
// 通过 endpoint.base_url 自动判断：
//   - 含 "mineru.net" 或路径以 /api/v4 开头 → 云 API
//   - 其它 → 自建 /file_parse
//
// endpoint.path 可显式覆盖云 API 的根路径（默认 /api/v4）。
type MinerU struct {
	client *httpx.Client
	path   string // 自建服务的解析路径，或云 API 根路径
	cloud  bool

	// pageRanges 可选，仅云 API 生效，如 "1-3" 或 "2,4-6"。为空表示整篇。
	pageRanges string
	// modelVersion 云 API 使用的模型版本，默认 pipeline。
	modelVersion string
}

// NewMinerU 创建 MinerU 解析器。
func NewMinerU(baseURL, apiKey, path string, timeout time.Duration, maxRetries int, qps float64) *MinerU {
	cloud := isMinerUCloud(baseURL, path)
	if path == "" {
		if cloud {
			path = "/api/v4"
		} else {
			path = "/file_parse"
		}
	}
	// 云 API 的“单次请求超时”只作用于单个 HTTP 调用，真正的解析在服务端异步进行，
	// 这里给一个较宽松的默认值即可；整体等待由轮询与调用方上下文控制。
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &MinerU{
		client: httpx.New(httpx.Options{
			Name:       "parse.mineru",
			BaseURL:    baseURL,
			APIKey:     apiKey,
			Timeout:    timeout,
			MaxRetries: maxRetries,
			QPS:        qps,
		}),
		path:         path,
		cloud:        cloud,
		modelVersion: "pipeline",
	}
}

// WithPageRanges 设置云 API 的页码范围（如 "1-3"），返回自身以便链式调用。
func (m *MinerU) WithPageRanges(r string) *MinerU {
	m.pageRanges = strings.TrimSpace(r)
	return m
}

// WithModelVersion 设置云 API 的模型版本（pipeline/vlm/MinerU-HTML）。
func (m *MinerU) WithModelVersion(v string) *MinerU {
	if v = strings.TrimSpace(v); v != "" {
		m.modelVersion = v
	}
	return m
}

// Name 返回实现名。
func (m *MinerU) Name() string { return "mineru" }

// Probe 用一次轻量的“申请上传链接”请求验证地址与密钥。
// 云 API 会创建一个空的 batch（不会上传文件，稍后自动过期）；
// 自建服务没有等价的轻量接口，直接返回成功，等真正解析时再验证。
func (m *MinerU) Probe(ctx context.Context) error {
	if !m.cloud {
		return nil
	}
	req := map[string]any{
		"files":         []map[string]any{{"name": "larder-probe.pdf"}},
		"model_version": m.modelVersion,
	}
	var env mineruEnvelope
	if err := m.client.DoJSON(ctx, "POST", m.path+"/file-urls/batch", req, &env); err != nil {
		return err
	}
	if env.Code != 0 {
		return fmt.Errorf("mineru: %s", env.Msg)
	}
	return nil
}

func isMinerUCloud(baseURL, path string) bool {
	lower := strings.ToLower(baseURL)
	if strings.Contains(lower, "mineru.net") {
		return true
	}
	if strings.Contains(path, "/api/v4") {
		return true
	}
	return false
}

// Parse 解析文件为 Markdown。
func (m *MinerU) Parse(ctx context.Context, in Input) (Result, error) {
	if m.cloud {
		return m.parseCloud(ctx, in)
	}
	return m.parseSelfHosted(ctx, in)
}

// ---------- 自建 /file_parse ----------

// mineruResponse 兼容 MinerU 自建服务与 mineru.net 的两种返回结构。
type mineruResponse struct {
	MDContent string `json:"md_content"`
	Markdown  string `json:"markdown"`
	Title     string `json:"title"`
	Data      struct {
		MDContent string `json:"md_content"`
		Markdown  string `json:"markdown"`
		Title     string `json:"title"`
	} `json:"data"`
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (m *MinerU) parseSelfHosted(ctx context.Context, in Input) (Result, error) {
	fields := map[string]string{
		"return_md":           "true",
		"return_content_list": "false",
		"return_images":       "false",
	}

	var resp mineruResponse
	if err := m.client.DoMultipart(ctx, m.path, "file", in.FileName, in.Data, fields, &resp); err != nil {
		return Result{}, err
	}
	if resp.Code != 0 && resp.Msg != "" {
		return Result{}, fmt.Errorf("mineru: 解析失败 code=%d: %s", resp.Code, resp.Msg)
	}

	md := firstNonEmpty(resp.MDContent, resp.Markdown, resp.Data.MDContent, resp.Data.Markdown)
	if strings.TrimSpace(md) == "" {
		return Result{}, fmt.Errorf("mineru: 响应中未找到 markdown 字段（请检查接口返回结构）")
	}
	title := firstNonEmpty(resp.Title, resp.Data.Title)
	if title == "" {
		title = inferTitle(md, in.FileName)
	}
	return Result{Markdown: md, Title: title, MIME: "text/markdown", Method: m.Name(), Note: "MinerU 自建服务"}, nil
}

// ---------- 官方云 API /api/v4 ----------

type mineruEnvelope struct {
	Code    int             `json:"code"`
	Msg     string          `json:"msg"`
	TraceID string          `json:"trace_id"`
	Data    json.RawMessage `json:"data"`
}

func (m *MinerU) parseCloud(ctx context.Context, in Input) (Result, error) {
	if len(in.Data) == 0 {
		return Result{}, fmt.Errorf("mineru: 文件内容为空")
	}

	// 1) 申请上传链接（本地文件批量上传接口）。
	name := in.FileName
	if name == "" {
		name = "document.pdf"
	}
	fileObj := map[string]any{"name": name}
	if m.pageRanges != "" {
		fileObj["page_ranges"] = m.pageRanges
	}
	reqBody := map[string]any{
		"files":         []map[string]any{fileObj},
		"model_version": m.modelVersion,
	}
	var apply mineruEnvelope
	if err := m.client.DoJSON(ctx, "POST", m.path+"/file-urls/batch", reqBody, &apply); err != nil {
		return Result{}, err
	}
	if apply.Code != 0 {
		return Result{}, fmt.Errorf("mineru: 申请上传链接失败 code=%d: %s", apply.Code, apply.Msg)
	}
	var applyData struct {
		BatchID  string   `json:"batch_id"`
		FileURLs []string `json:"file_urls"`
	}
	if err := json.Unmarshal(apply.Data, &applyData); err != nil {
		return Result{}, fmt.Errorf("mineru: 解析申请响应失败: %w", err)
	}
	if applyData.BatchID == "" || len(applyData.FileURLs) == 0 {
		return Result{}, fmt.Errorf("mineru: 申请响应缺少 batch_id 或 file_urls")
	}

	// 2) PUT 上传文件到预签名 URL。上传完成后系统会自动提交解析任务。
	if err := m.client.PutBytes(ctx, applyData.FileURLs[0], in.Data); err != nil {
		return Result{}, fmt.Errorf("mineru: 上传文件失败: %w", err)
	}

	// 3) 轮询批量结果，找到本文件对应的 full_zip_url。
	zipURL, err := m.pollBatch(ctx, applyData.BatchID, name)
	if err != nil {
		return Result{}, err
	}

	// 4) 下载结果 zip，取出 full.md。
	md, err := m.fetchMarkdown(ctx, zipURL)
	if err != nil {
		return Result{}, err
	}
	return Result{Markdown: md, Title: inferTitle(md, name), MIME: "text/markdown", Method: m.Name(), Note: "MinerU 云 API"}, nil
}

type mineruBatchResult struct {
	FileName string `json:"file_name"`
	State    string `json:"state"`
	ErrMsg   string `json:"err_msg"`
	FullZip  string `json:"full_zip_url"`
}

// pollBatch 轮询批量任务直到本文件解析完成并返回结果 zip 地址。
// 间隔从 3s 起逐步增大到 15s，整体最长等待约 20 分钟（受 ctx 控制）。
func (m *MinerU) pollBatch(ctx context.Context, batchID, fileName string) (string, error) {
	const maxWait = 20 * time.Minute
	deadline := time.Now().Add(maxWait)
	interval := 3 * time.Second
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		var env mineruEnvelope
		if err := m.client.DoJSON(ctx, "GET", m.path+"/extract-results/batch/"+batchID, nil, &env); err != nil {
			return "", err
		}
		if env.Code != 0 {
			return "", fmt.Errorf("mineru: 查询任务失败 code=%d: %s", env.Code, env.Msg)
		}
		var data struct {
			BatchID       string              `json:"batch_id"`
			ExtractResult []mineruBatchResult `json:"extract_result"`
		}
		if err := json.Unmarshal(env.Data, &data); err != nil {
			return "", fmt.Errorf("mineru: 解析查询响应失败: %w", err)
		}

		var target *mineruBatchResult
		for i := range data.ExtractResult {
			if data.ExtractResult[i].FileName == fileName {
				target = &data.ExtractResult[i]
				break
			}
		}
		if target == nil && len(data.ExtractResult) > 0 {
			// 文件名可能被规范化，退化为取第一个结果。
			target = &data.ExtractResult[0]
		}

		if target != nil {
			switch target.State {
			case "done":
				if target.FullZip == "" {
					return "", fmt.Errorf("mineru: 任务完成但缺少 full_zip_url")
				}
				return target.FullZip, nil
			case "failed":
				return "", fmt.Errorf("mineru: 解析失败: %s", strings.TrimSpace(target.ErrMsg))
			}
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("mineru: 轮询超时（超过 %s）, batch_id=%s", maxWait, batchID)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
		if interval < 15*time.Second {
			interval += 2 * time.Second
		}
	}
}

// fetchMarkdown 下载结果 zip 并取出其中的 full.md。
func (m *MinerU) fetchMarkdown(ctx context.Context, zipURL string) (string, error) {
	// 结果 zip 在对象存储（OSS/COS）上，刚完成轮询时立即下载容易触发 QPS 限流，
	// 稍等片刻再取；真正的限流由 httpx.GetBytes 的退避重试兜底。
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(time.Second):
	}
	data, err := m.client.GetBytes(ctx, zipURL, 512<<20)
	if err != nil {
		return "", fmt.Errorf("mineru: 下载结果失败: %w", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("mineru: 结果压缩包无法读取: %w", err)
	}

	// 优先 full.md，其次任意 .md。
	var mdFile *zip.File
	for _, f := range zr.File {
		base := strings.ToLower(f.Name)
		if strings.HasSuffix(base, "full.md") {
			mdFile = f
			break
		}
		if mdFile == nil && strings.HasSuffix(base, ".md") {
			mdFile = f
		}
	}
	if mdFile == nil {
		return "", fmt.Errorf("mineru: 结果压缩包中未找到 markdown 文件")
	}
	rc, err := mdFile.Open()
	if err != nil {
		return "", fmt.Errorf("mineru: 读取 markdown 失败: %w", err)
	}
	defer rc.Close()
	md, err := io.ReadAll(io.LimitReader(rc, 64<<20))
	if err != nil {
		return "", fmt.Errorf("mineru: 读取 markdown 失败: %w", err)
	}
	out := strings.TrimSpace(string(md))
	if out == "" {
		return "", fmt.Errorf("mineru: markdown 内容为空")
	}
	return out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
