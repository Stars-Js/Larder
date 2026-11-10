package httpx

import "testing"

func TestJoinURL(t *testing.T) {
	cases := []struct {
		base, path, want string
	}{
		{"https://api.siliconflow.cn", "/v1/embeddings", "https://api.siliconflow.cn/v1/embeddings"},
		{"https://api.siliconflow.cn/", "/v1/embeddings", "https://api.siliconflow.cn/v1/embeddings"},
		{"https://api.siliconflow.cn/v1", "/v1/embeddings", "https://api.siliconflow.cn/v1/embeddings"},
		{"https://api.siliconflow.cn/v1/", "/v1/rerank", "https://api.siliconflow.cn/v1/rerank"},
		{"https://api.siliconflow.cn/v1", "/embeddings", "https://api.siliconflow.cn/v1/embeddings"},
		{"https://mineru.net", "/api/v4/file-urls/batch", "https://mineru.net/api/v4/file-urls/batch"},
		{"https://mineru.net/api/v4", "/api/v4/file-urls/batch", "https://mineru.net/api/v4/file-urls/batch"},
		{"https://mineru.net/api/v4", "/api/v4/extract-results/batch/abc", "https://mineru.net/api/v4/extract-results/batch/abc"},
		{"http://127.0.0.1:8888", "/file_parse", "http://127.0.0.1:8888/file_parse"},
		{"http://127.0.0.1:8888/file_parse", "/file_parse", "http://127.0.0.1:8888/file_parse"},
		{"https://api.openai.com", "/v1/embeddings", "https://api.openai.com/v1/embeddings"},
	}
	for _, tc := range cases {
		if got := joinURL(tc.base, tc.path); got != tc.want {
			t.Errorf("joinURL(%q, %q) = %q, want %q", tc.base, tc.path, got, tc.want)
		}
	}
}
