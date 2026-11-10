package parse

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestMinerUCloudFlow 用模拟服务器完整走一遍 MinerU 官方云 API 的流程：
// 申请上传链接 → PUT 上传 → 轮询批量结果 → 下载 zip 取 full.md。
func TestMinerUCloudFlow(t *testing.T) {
	const fullMD = "# 标题\n\n这是从 MinerU 返回的正文内容。\n\n- 项目一\n- 项目二\n"

	var uploaded int32
	var pollCount int32
	var gotAuth string

	mux := http.NewServeMux()

	// 1) 申请上传链接
	mux.HandleFunc("/api/v4/file-urls/batch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("apply: want POST, got %s", r.Method)
		}
		gotAuth = r.Header.Get("Authorization")
		// 回显一个本服务器的 PUT 地址
		putURL := "http://" + r.Host + "/upload/put"
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "ok",
			"data": map[string]any{
				"batch_id":  "batch-123",
				"file_urls": []string{putURL},
			},
		})
	})

	// 2) 预签名上传（PUT，不带 Authorization）
	mux.HandleFunc("/upload/put", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("upload: want PUT, got %s", r.Method)
		}
		atomic.StoreInt32(&uploaded, 1)
		w.WriteHeader(http.StatusOK)
	})

	// 3) 轮询批量结果：前两次 running，第三次 done
	mux.HandleFunc("/api/v4/extract-results/batch/batch-123", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&pollCount, 1)
		state := "running"
		zipURL := ""
		if n >= 3 {
			state = "done"
			zipURL = "http://" + r.Host + "/result.zip"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "ok",
			"data": map[string]any{
				"batch_id": "batch-123",
				"extract_result": []map[string]any{
					{"file_name": "1.pdf", "state": state, "full_zip_url": zipURL},
				},
			},
		})
	})

	// 4) 结果 zip（含 full.md）
	mux.HandleFunc("/result.zip", func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		f, _ := zw.Create("some/full.md")
		_, _ = f.Write([]byte(fullMD))
		_ = zw.Close()
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(buf.Bytes())
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := NewMinerU(srv.URL, "test-token", "/api/v4", 10*time.Second, 1, 0)
	if !p.cloud {
		t.Fatalf("期望识别为云 API 模式")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := p.Parse(ctx, Input{FileName: "1.pdf", Data: []byte("%PDF-1.4 fake")})
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	if strings.TrimSpace(res.Markdown) != strings.TrimSpace(fullMD) {
		t.Fatalf("markdown 不匹配:\n got=%q", res.Markdown)
	}
	if atomic.LoadInt32(&uploaded) != 1 {
		t.Fatalf("文件未通过 PUT 上传")
	}
	if atomic.LoadInt32(&pollCount) < 3 {
		t.Fatalf("轮询次数不足: %d", pollCount)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("Authorization 头错误: %q", gotAuth)
	}
}

// TestMinerUSelfHosted 验证自建 /file_parse 分支仍可用。
func TestMinerUSelfHosted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file_parse" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"md_content": "# hi\n\nself-hosted"})
	}))
	defer srv.Close()

	p := NewMinerU(srv.URL, "", "", 10*time.Second, 0, 0)
	if p.cloud {
		t.Fatalf("自建地址不应被识别为云 API")
	}
	res, err := p.Parse(context.Background(), Input{FileName: "a.pdf", Data: []byte("x")})
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	if !strings.Contains(res.Markdown, "self-hosted") {
		t.Fatalf("markdown 不符: %q", res.Markdown)
	}
}
