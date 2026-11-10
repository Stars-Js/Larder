// Package httpx 提供三个外部 provider 共用的 HTTP 客户端能力：
// 超时、指数退避重试、令牌桶限流、以及统一的错误类型。
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// APIError 是外部服务返回的非 2xx 响应。
type APIError struct {
	Provider   string
	StatusCode int
	Body       string
	RetryAfter time.Duration
}

// Error 实现 error 接口。
func (e *APIError) Error() string {
	body := e.Body
	if len(body) > 512 {
		body = body[:512] + "..."
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.StatusCode, body)
}

// Retryable 判断该错误是否值得重试。
func (e *APIError) Retryable() bool {
	switch e.StatusCode {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// IsRetryable 判断任意错误是否可重试。
func IsRetryable(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Retryable()
	}
	return false
}

// Options 描述客户端行为。
type Options struct {
	Name       string
	BaseURL    string
	APIKey     string
	Timeout    time.Duration
	MaxRetries int
	QPS        float64
	UserAgent  string
}

// Client 是带重试与限流的 JSON HTTP 客户端。
type Client struct {
	opts Options
	http *http.Client
	lim  *limiter
}

// New 创建客户端。QPS<=0 时不限流。
func New(opts Options) *Client {
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "larder/1.0"
	}
	return &Client{
		opts: opts,
		http: &http.Client{Timeout: opts.Timeout},
		lim:  newLimiter(opts.QPS, opts.QPS),
	}
}

// DoJSON 发送 JSON 请求并把响应 JSON 解析到 out。
// path 为相对 BaseURL 的路径，如 "/v1/embeddings"。
func (c *Client) DoJSON(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		body, err = json.Marshal(in)
		if err != nil {
			return fmt.Errorf("%s: 序列化请求失败: %w", c.opts.Name, err)
		}
	}

	attempts := c.opts.MaxRetries + 1
	var lastErr error

	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := sleepBackoff(ctx, attempt, lastErr); err != nil {
				return err
			}
		}
		if err := c.lim.wait(ctx); err != nil {
			return err
		}

		respBody, status, retryAfter, err := c.send(ctx, method, path, body)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}

		if status < 200 || status > 299 {
			apiErr := &APIError{
				Provider:   c.opts.Name,
				StatusCode: status,
				Body:       string(respBody),
				RetryAfter: retryAfter,
			}
			lastErr = apiErr
			if apiErr.Retryable() {
				continue
			}
			return apiErr
		}

		if out == nil || len(respBody) == 0 {
			return nil
		}
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("%s: 解析响应失败: %w", c.opts.Name, err)
		}
		return nil
	}

	return fmt.Errorf("%s: 重试 %d 次后仍失败: %w", c.opts.Name, c.opts.MaxRetries, lastErr)
}

func (c *Client) send(ctx context.Context, method, path string, body []byte) ([]byte, int, time.Duration, error) {
	url := joinURL(c.opts.BaseURL, path)
	return c.do(ctx, method, url, body)
}

// DoJSONURL 向一个绝对 URL 发送 JSON 请求并把响应解析到 out。
// 用于云服务返回的绝对地址（如对象存储预签名 URL）。
func (c *Client) DoJSONURL(ctx context.Context, method, url string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		body, err = json.Marshal(in)
		if err != nil {
			return fmt.Errorf("%s: 序列化请求失败: %w", c.opts.Name, err)
		}
	}

	attempts := c.opts.MaxRetries + 1
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := sleepBackoff(ctx, attempt, lastErr); err != nil {
				return err
			}
		}
		if err := c.lim.wait(ctx); err != nil {
			return err
		}

		respBody, status, retryAfter, err := c.do(ctx, method, url, body)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if status < 200 || status > 299 {
			apiErr := &APIError{Provider: c.opts.Name, StatusCode: status, Body: string(respBody), RetryAfter: retryAfter}
			lastErr = apiErr
			if apiErr.Retryable() {
				continue
			}
			return apiErr
		}
		if out == nil || len(respBody) == 0 {
			return nil
		}
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("%s: 解析响应失败: %w", c.opts.Name, err)
		}
		return nil
	}
	return fmt.Errorf("%s: 重试 %d 次后仍失败: %w", c.opts.Name, c.opts.MaxRetries, lastErr)
}

// GetBytes 下载一个绝对 URL 的二进制内容（用于拉取解析结果压缩包/文件）。
// 不附加 Authorization，也不参与 JSON 解析；最多读取 maxBytes 字节（<=0 表示 1GB）。
// 对象存储（如 MinerU 的结果 zip）会限流，这里按 MaxRetries 做退避重试。
func (c *Client) GetBytes(ctx context.Context, url string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = 1 << 30
	}
	attempts := c.opts.MaxRetries + 1
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := sleepBackoff(ctx, attempt, lastErr); err != nil {
				return nil, err
			}
		}
		if err := c.lim.wait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("%s: 构造请求失败: %w", c.opts.Name, err)
		}
		req.Header.Set("User-Agent", c.opts.UserAgent)
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s: 请求失败: %w", c.opts.Name, err)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
		retryAfter := retryAfterFrom(resp)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("%s: 读取响应失败: %w", c.opts.Name, readErr)
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			return data, nil
		}
		apiErr := &APIError{
			Provider:   c.opts.Name,
			StatusCode: resp.StatusCode,
			Body:       string(data),
			RetryAfter: retryAfter,
		}
		lastErr = apiErr
		if apiErr.Retryable() {
			continue
		}
		return nil, apiErr
	}
	return nil, fmt.Errorf("%s: 下载重试 %d 次后仍失败: %w", c.opts.Name, c.opts.MaxRetries, lastErr)
}

// PutBytes 把原始字节 PUT 到一个绝对 URL（用于对象存储预签名上传，不带 Authorization）。
// 与 GetBytes 一样对 429/5xx 与网络错误做退避重试。
func (c *Client) PutBytes(ctx context.Context, url string, data []byte) error {
	attempts := c.opts.MaxRetries + 1
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := sleepBackoff(ctx, attempt, lastErr); err != nil {
				return err
			}
		}
		if err := c.lim.wait(ctx); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("%s: 构造请求失败: %w", c.opts.Name, err)
		}
		req.Header.Set("User-Agent", c.opts.UserAgent)
		// 预签名 URL 上传时不设置 Content-Type（MinerU 明确要求）。
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s: 上传失败: %w", c.opts.Name, err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		retryAfter := retryAfterFrom(resp)
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			return nil
		}
		apiErr := &APIError{
			Provider:   c.opts.Name,
			StatusCode: resp.StatusCode,
			Body:       string(body),
			RetryAfter: retryAfter,
		}
		lastErr = apiErr
		if apiErr.Retryable() {
			continue
		}
		return apiErr
	}
	return fmt.Errorf("%s: 上传重试 %d 次后仍失败: %w", c.opts.Name, c.opts.MaxRetries, lastErr)
}

// retryAfterFrom 读取 Retry-After（秒）响应头。
func retryAfterFrom(resp *http.Response) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

// do 是底层请求发送：设置通用请求头并读取响应（限制 32MB）。
func (c *Client) do(ctx context.Context, method, url string, body []byte) ([]byte, int, time.Duration, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("%s: 构造请求失败: %w", c.opts.Name, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", c.opts.UserAgent)
	if c.opts.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.opts.APIKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("%s: 请求失败: %w", c.opts.Name, err)
	}
	defer resp.Body.Close()

	// 限制读取上限，避免异常响应撑爆内存。
	limited := io.LimitReader(resp.Body, 32<<20)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("%s: 读取响应失败: %w", c.opts.Name, err)
	}

	var retryAfter time.Duration
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			retryAfter = time.Duration(secs) * time.Second
		}
	}
	return data, resp.StatusCode, retryAfter, nil
}

// DoMultipart 发送 multipart/form-data，供解析服务上传文件使用。
func (c *Client) DoMultipart(ctx context.Context, path, fieldName, fileName string, data []byte, fields map[string]string, out any) error {
	var lastErr error
	attempts := c.opts.MaxRetries + 1

	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := sleepBackoff(ctx, attempt, lastErr); err != nil {
				return err
			}
		}
		if err := c.lim.wait(ctx); err != nil {
			return err
		}

		body, contentType, err := multipartBody(fieldName, fileName, data, fields)
		if err != nil {
			return err
		}

		url := joinURL(c.opts.BaseURL, path)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("%s: 构造请求失败: %w", c.opts.Name, err)
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.opts.UserAgent)
		if c.opts.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.opts.APIKey)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s: 请求失败: %w", c.opts.Name, err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}

		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("%s: 读取响应失败: %w", c.opts.Name, readErr)
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			apiErr := &APIError{
				Provider:   c.opts.Name,
				StatusCode: resp.StatusCode,
				Body:       string(respBody),
			}
			lastErr = apiErr
			if apiErr.Retryable() {
				continue
			}
			return apiErr
		}

		if out != nil {
			if err := json.Unmarshal(respBody, out); err != nil {
				return fmt.Errorf("%s: 解析响应失败: %w", c.opts.Name, err)
			}
		}
		return nil
	}
	return fmt.Errorf("%s: 重试 %d 次后仍失败: %w", c.opts.Name, c.opts.MaxRetries, lastErr)
}

// DoRaw 发送原始字节请求并返回原始响应体，供返回非 JSON 的服务（如 Tika）使用。
func (c *Client) DoRaw(ctx context.Context, method, path, contentType string, in []byte, accept string) ([]byte, error) {
	var lastErr error
	attempts := c.opts.MaxRetries + 1

	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := sleepBackoff(ctx, attempt, lastErr); err != nil {
				return nil, err
			}
		}
		if err := c.lim.wait(ctx); err != nil {
			return nil, err
		}

		url := joinURL(c.opts.BaseURL, path)
		var rdr io.Reader
		if in != nil {
			rdr = bytes.NewReader(in)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rdr)
		if err != nil {
			return nil, fmt.Errorf("%s: 构造请求失败: %w", c.opts.Name, err)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		req.Header.Set("User-Agent", c.opts.UserAgent)
		if c.opts.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.opts.APIKey)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s: 请求失败: %w", c.opts.Name, err)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}

		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("%s: 读取响应失败: %w", c.opts.Name, readErr)
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			apiErr := &APIError{
				Provider:   c.opts.Name,
				StatusCode: resp.StatusCode,
				Body:       string(data),
			}
			lastErr = apiErr
			if apiErr.Retryable() {
				continue
			}
			return nil, apiErr
		}
		return data, nil
	}
	return nil, fmt.Errorf("%s: 重试 %d 次后仍失败: %w", c.opts.Name, c.opts.MaxRetries, lastErr)
}

// sleepBackoff 指数退避，并优先采用服务端 Retry-After。
func sleepBackoff(ctx context.Context, attempt int, lastErr error) error {
	var wait time.Duration
	var ae *APIError
	if errors.As(lastErr, &ae) && ae.RetryAfter > 0 {
		wait = ae.RetryAfter
	} else {
		base := time.Duration(1<<uint(attempt-1)) * 300 * time.Millisecond
		if base > 8*time.Second {
			base = 8 * time.Second
		}
		jitter := time.Duration(rand.Int63n(int64(base/2 + 1)))
		wait = base + jitter
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// limiter 是简单的令牌桶，按固定间隔匀速放行。
type limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newLimiter(qps, burst float64) *limiter {
	if qps <= 0 {
		return &limiter{}
	}
	return &limiter{interval: time.Duration(float64(time.Second) / qps)}
}

func (l *limiter) wait(ctx context.Context) error {
	if l.interval <= 0 {
		return nil
	}

	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	wait := l.next.Sub(now)
	l.next = l.next.Add(l.interval)
	l.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
