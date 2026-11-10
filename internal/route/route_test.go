package route_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"larder/internal/api"
	"larder/internal/config"
	"larder/internal/kb"
	"larder/internal/route"
	"larder/internal/services"
)

func fakeEmbed(t *testing.T, dim int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		data := make([]item, len(req.Input))
		for i, s := range req.Input {
			v := make([]float32, dim)
			if strings.Contains(s, "Windows") {
				v[0] = 1
			}
			if strings.Contains(s, "macOS") {
				v[1] = 1
			}
			data[i] = item{Index: i, Embedding: v}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
}

func TestHTTPEndToEnd(t *testing.T) {
	dir := t.TempDir()
	dim := 8
	emb := fakeEmbed(t, dim)
	defer emb.Close()

	cfg := config.Default()
	cfg.Storage.Dir = dir
	cfg.Embed.Endpoint.BaseURL = emb.URL
	cfg.Embed.Endpoint.APIKey = "test"
	cfg.Embed.Dim = dim
	cfg.Embed.Endpoint.Timeout = config.Duration(10 * time.Second)
	cfg.Server.MCPPath = "/mcp"
	cfg.Chunk.MaxRunes = 400
	cfg.Chunk.MinRunes = 10

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := services.New(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := kb.NewManager(cfg, svc, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	savePath := filepath.Join(dir, "larder.toml")
	h := route.Handler(route.Deps{
		Manager: mgr,
		MCPPath: "/mcp",
		Logger:  logger,
		API: api.Deps{
			Manager: mgr, Services: svc,
			SaveConfig: func(c config.Config) error { return config.Save(savePath, c) },
			Version:    "test", Logger: logger,
		},
	})
	ts := httptest.NewServer(h)
	defer ts.Close()

	get := func(path string) *http.Response {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return resp
	}

	// healthz
	if r := get("/healthz"); r.StatusCode != 200 {
		t.Fatalf("healthz=%d", r.StatusCode)
	} else {
		r.Body.Close()
	}

	// index.html（静态资源）
	if r := get("/"); r.StatusCode != 200 {
		t.Fatalf("index=%d", r.StatusCode)
	} else {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if !bytes.Contains(b, []byte("Larder")) {
			t.Fatal("index.html 未包含 Larder")
		}
	}

	// SPA 回退
	if r := get("/settings"); r.StatusCode != 200 {
		t.Fatalf("spa fallback=%d", r.StatusCode)
	} else {
		r.Body.Close()
	}

	// 建库
	body, _ := json.Marshal(map[string]string{"name": "HTTP 测试库"})
	resp, err := http.Post(ts.URL+"/api/kbs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var kbInfo struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&kbInfo)
	resp.Body.Close()
	if kbInfo.ID == "" {
		t.Fatal("创建库未返回 id")
	}

	// 上传文件（multipart）
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "install.md")
	_, _ = fw.Write([]byte("# 安装\n\n## Windows\n\n在 Windows 上运行安装程序。\n\n## macOS\n\n用 Homebrew。\n"))
	mw.WriteField("title", "安装指南")
	mw.Close()
	upReq, _ := http.NewRequest("POST", ts.URL+"/api/kbs/"+kbInfo.ID+"/ingest/upload", &buf)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	upResp, err := http.DefaultClient.Do(upReq)
	if err != nil {
		t.Fatal(err)
	}
	if upResp.StatusCode != 200 {
		b, _ := io.ReadAll(upResp.Body)
		t.Fatalf("上传失败 status=%d body=%s", upResp.StatusCode, b)
	}
	upResp.Body.Close()

	// 检索
	sbody, _ := json.Marshal(map[string]any{"query": "Windows"})
	sresp, err := http.Post(ts.URL+"/api/kbs/"+kbInfo.ID+"/search", "application/json", bytes.NewReader(sbody))
	if err != nil {
		t.Fatal(err)
	}
	var sr struct {
		Results []struct {
			Text string `json:"text"`
		} `json:"results"`
	}
	json.NewDecoder(sresp.Body).Decode(&sr)
	sresp.Body.Close()
	if len(sr.Results) == 0 || !strings.Contains(sr.Results[0].Text, "Windows") {
		t.Fatalf("检索未命中 Windows: %+v", sr)
	}

	// 设置：修改并持久化
	setBody, _ := json.Marshal(map[string]any{
		"embed":  map[string]any{"enabled": true, "provider": "openai", "base_url": emb.URL, "model": "fake", "dim": dim, "timeout": "10s"},
		"parse":  map[string]any{"enabled": true, "provider": "plain"},
		"rerank": map[string]any{"enabled": false, "provider": "openai"},
	})
	req, _ := http.NewRequest("PUT", ts.URL+"/api/settings", bytes.NewReader(setBody))
	req.Header.Set("Content-Type", "application/json")
	presp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if presp.StatusCode != 200 {
		b, _ := io.ReadAll(presp.Body)
		t.Fatalf("保存设置失败 status=%d body=%s", presp.StatusCode, b)
	}
	presp.Body.Close()
	if _, err := os.Stat(savePath); err != nil {
		t.Fatalf("配置文件未写入: %v", err)
	}
}
