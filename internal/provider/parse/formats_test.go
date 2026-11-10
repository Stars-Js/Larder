package parse

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读取测试文件 %s 失败: %v", name, err)
	}
	return data
}

func TestPlainParsesLocalDocuments(t *testing.T) {
	cases := []struct {
		file    string
		want    []string
		mime    string
		method  string
		notWant string
	}{
		{"sample.docx", []string{"安装手册", "Windows", "install.exe"}, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "plain", ""},
		{"sample.pptx", []string{"产品介绍", "Larder 是一个本地知识库", "检索流程"}, "application/vnd.openxmlformats-officedocument.presentationml.presentation", "plain", ""},
		{"sample.xlsx", []string{"产品", "Larder", "价格", "说明"}, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "plain", ""},
		{"sample.html", []string{"帮助中心", "快速开始", "支持的格式"}, "text/html", "plain", "alert(1)"},
		{"text.pdf", []string{"Larder PDF text sample", "real text layer"}, "application/pdf", "plain", ""},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			res, err := NewPlain().Parse(context.Background(), Input{
				FileName: tc.file,
				MIME:     tc.mime,
				Data:     readFixture(t, tc.file),
			})
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if res.Method != tc.method {
				t.Errorf("Method = %q, want %q", res.Method, tc.method)
			}
			for _, want := range tc.want {
				if !strings.Contains(res.Markdown, want) {
					t.Errorf("解析结果缺少 %q:\n%s", want, truncate(res.Markdown, 500))
				}
			}
			if tc.notWant != "" && strings.Contains(res.Markdown, tc.notWant) {
				t.Errorf("解析结果不应包含 %q", tc.notWant)
			}
		})
	}
}

func TestPlainRejectsImageOnlyPDF(t *testing.T) {
	_, err := NewPlain().Parse(context.Background(), Input{
		FileName: "image.pdf",
		MIME:     "application/pdf",
		Data:     readFixture(t, "image.pdf"),
	})
	if err == nil {
		t.Fatal("无文本层 PDF 应当返回错误")
	}
	if !strings.Contains(err.Error(), "文本层") {
		t.Errorf("错误信息应说明缺少文本层，实际: %v", err)
	}
}

// fakeExternal 模拟 MinerU/Docling：仅当本地解析失败时被调用。
type fakeExternal struct {
	called bool
	err    error
	out    string
}

func (f *fakeExternal) Name() string { return "fakeocr" }
func (f *fakeExternal) Parse(_ context.Context, in Input) (Result, error) {
	f.called = true
	if f.err != nil {
		return Result{}, f.err
	}
	return Result{Markdown: f.out, Title: "OCR 结果", Method: "fakeocr"}, nil
}

func TestHybridFallsBackForScannedPDF(t *testing.T) {
	ext := &fakeExternal{out: "# 识别结果\n\n这是扫描件 OCR 后的文字。"}
	h := NewHybrid(NewPlain(), ext)
	res, err := h.Parse(context.Background(), Input{
		FileName: "image.pdf",
		MIME:     "application/pdf",
		Data:     readFixture(t, "image.pdf"),
	})
	if err != nil {
		t.Fatalf("混合解析失败: %v", err)
	}
	if !ext.called {
		t.Fatal("本地解析失败后应调用外部解析服务")
	}
	if res.Method != "fakeocr" || !strings.Contains(res.Markdown, "OCR") {
		t.Fatalf("应返回外部解析结果，实际 method=%q md=%q", res.Method, res.Markdown)
	}
}

func TestHybridKeepsLocalForTextDocuments(t *testing.T) {
	ext := &fakeExternal{out: "不该用到"}
	h := NewHybrid(NewPlain(), ext)
	res, err := h.Parse(context.Background(), Input{
		FileName: "a.md",
		Data:     []byte("# 本地\n\n普通文本"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ext.called {
		t.Fatal("文本文件不应调用外部解析服务")
	}
	if res.Method != "plain" || !strings.Contains(res.Markdown, "普通文本") {
		t.Fatalf("应使用本地解析，实际 method=%q", res.Method)
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}
