package httpx

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"strings"
)

// joinURL 把 base 与 path 拼成完整地址，避免重复斜杠与重复版本前缀。
//
// 例如：
//
//	base=https://api.siliconflow.cn/v1  path=/v1/embeddings  -> https://api.siliconflow.cn/v1/embeddings
//	base=https://mineru.net/api/v4      path=/api/v4/file-urls/batch -> https://mineru.net/api/v4/file-urls/batch
//	base=https://api.siliconflow.cn     path=/v1/embeddings  -> https://api.siliconflow.cn/v1/embeddings
func joinURL(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if path == "" {
		return base
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	baseSegs := splitPathSegments(base)
	pathSegs := splitPathSegments(path)

	// 找出 base 末尾与 path 开头最长的重叠段并去掉，避免 /v1/v1/... 或 /api/v4/api/v4/...
	maxK := len(baseSegs)
	if len(pathSegs) < maxK {
		maxK = len(pathSegs)
	}
	for k := maxK; k >= 1; k-- {
		if segmentsEqual(baseSegs[len(baseSegs)-k:], pathSegs[:k]) {
			pathSegs = pathSegs[k:]
			break
		}
	}
	if len(pathSegs) == 0 {
		return base
	}
	return base + "/" + strings.Join(pathSegs, "/")
}

func splitPathSegments(p string) []string {
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for _, s := range parts {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func segmentsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// multipartBody 构造 multipart/form-data 请求体。
func multipartBody(fieldName, fileName string, data []byte, fields map[string]string) ([]byte, string, error) {
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)

	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, "", fmt.Errorf("写入表单字段 %s 失败: %w", k, err)
		}
	}

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, fieldName, fileName))
	h.Set("Content-Type", "application/octet-stream")

	part, err := w.CreatePart(h)
	if err != nil {
		return nil, "", fmt.Errorf("创建文件字段失败: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return nil, "", fmt.Errorf("写入文件内容失败: %w", err)
	}

	if err := w.Close(); err != nil {
		return nil, "", fmt.Errorf("关闭 multipart 失败: %w", err)
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}
