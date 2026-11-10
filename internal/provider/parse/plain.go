package parse

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Plain 是内置的纯文本/轻量文档解析器，不需要任何外部服务。
// 支持：Markdown/纯文本/代码/CSV/HTML、文字版 PDF、DOCX、PPTX、XLSX、EPUB、RTF。
// 扫描件与复杂版式仍交给外部解析服务。
type Plain struct{}

// NewPlain 创建内置解析器。
func NewPlain() *Plain { return &Plain{} }

// Name 返回实现名。
func (p *Plain) Name() string { return "plain" }

var plainExts = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".mdx": true,
	".rst": true, ".adoc": true, ".org": true,
	".go": true, ".py": true, ".js": true, ".ts": true, ".jsx": true, ".tsx": true,
	".java": true, ".c": true, ".h": true, ".cc": true, ".cpp": true, ".hpp": true,
	".cs": true, ".rs": true, ".rb": true, ".php": true, ".swift": true, ".kt": true,
	".sh": true, ".ps1": true, ".bat": true, ".sql": true, ".yaml": true, ".yml": true,
	".toml": true, ".ini": true, ".json": true, ".csv": true, ".tsv": true,
	".html": true, ".htm": true, ".xhtml": true, ".xml": true, ".log": true,
	".tex": true, ".proto": true, ".vue": true, ".svelte": true, ".dart": true,
}

var plainMIMEs = map[string]bool{
	"text/plain": true, "text/markdown": true, "text/html": true,
	"application/json": true, "application/xml": true, "text/xml": true,
}

// Parse 直接读取文本，必要时按扩展名包一层代码围栏以保留结构。
func (p *Plain) Parse(ctx context.Context, in Input) (Result, error) {
	if len(in.Data) == 0 {
		return Result{}, errors.New("plain: 文件内容为空")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	ext := strings.ToLower(filepath.Ext(in.FileName))
	mime := strings.ToLower(in.MIME)

	// PDF：内置纯 Go 文本层抽取（文字版 PDF 可直接入库）。
	if ext == ".pdf" || mime == "application/pdf" {
		text, pages, err := extractPDFText(in.Data)
		if err != nil {
			return Result{}, fmt.Errorf("plain: %w", err)
		}
		return Result{
			Markdown: text,
			Title:    inferTitle(text, in.FileName),
			MIME:     "text/markdown",
			Method:   p.Name(),
			Note:     fmt.Sprintf("内置 PDF 文本层抽取，%d 页，%d 字符", pages, len([]rune(text))),
		}, nil
	}

	// Office / EPUB / HTML 等常见格式在本地直接抽取。
	if out, ok, err := p.parseLocalDocument(ext, mime, in); ok {
		if err != nil {
			return Result{}, err
		}
		out.Method = p.Name()
		out.MIME = "text/markdown"
		if out.Title == "" {
			out.Title = inferTitle(out.Markdown, in.FileName)
		}
		return out, nil
	}

	if !utf8.Valid(in.Data) {
		return Result{}, errors.New("plain: 非 UTF-8 文本，请配置外部解析服务（如 MinerU / Docling）")
	}

	// 即使扩展名不认识，只要 MIME 是文本也放行。
	if !plainExts[ext] && !plainMIMEs[mime] && !strings.HasPrefix(mime, "text/") {
		return Result{}, fmt.Errorf("plain: 不支持的文件类型 %q，请配置外部解析服务", extOrMime(ext, mime))
	}

	text := string(in.Data)
	title := inferTitle(text, in.FileName)

	// 代码类文件包进围栏，让后续切块能识别边界。
	if isCodeExt(ext) {
		lang := strings.TrimPrefix(ext, ".")
		text = fmt.Sprintf("```%s\n%s\n```\n", lang, text)
	}

	return Result{
		Markdown: text,
		Title:    title,
		MIME:     pickMIME(mime, ext),
		Method:   p.Name(),
		Note:     fmt.Sprintf("%d 字符", len([]rune(text))),
	}, nil
}

func (p *Plain) parseLocalDocument(ext, mime string, in Input) (Result, bool, error) {
	switch {
	case ext == ".docx" || strings.Contains(mime, "wordprocessingml"):
		md, err := extractDOCX(in.Data)
		if err != nil {
			return Result{}, true, fmt.Errorf("plain: Word 文档解析失败: %w", err)
		}
		return Result{Markdown: md, Note: "本地 DOCX 抽取"}, true, nil
	case ext == ".pptx" || strings.Contains(mime, "presentationml"):
		md, err := extractPPTX(in.Data)
		if err != nil {
			return Result{}, true, fmt.Errorf("plain: PPT 文档解析失败: %w", err)
		}
		return Result{Markdown: md, Note: "本地 PPTX 抽取"}, true, nil
	case ext == ".xlsx" || ext == ".xlsm" || strings.Contains(mime, "spreadsheetml"):
		md, err := extractXLSX(in.Data)
		if err != nil {
			return Result{}, true, fmt.Errorf("plain: Excel 文档解析失败: %w", err)
		}
		return Result{Markdown: md, Note: "本地 XLSX 抽取"}, true, nil
	case ext == ".epub" || mime == "application/epub+zip":
		md, err := extractEPUB(in.Data)
		if err != nil {
			return Result{}, true, fmt.Errorf("plain: EPUB 解析失败: %w", err)
		}
		return Result{Markdown: md, Note: "本地 EPUB 抽取"}, true, nil
	case ext == ".html" || ext == ".htm" || ext == ".xhtml":
		md := htmlToMarkdown(string(in.Data))
		if strings.TrimSpace(md) == "" {
			return Result{}, true, errors.New("plain: HTML 中没有可提取的正文")
		}
		return Result{Markdown: md, Note: "本地 HTML 抽取"}, true, nil
	case ext == ".rtf":
		md := extractRTF(string(in.Data))
		if strings.TrimSpace(md) == "" {
			return Result{}, true, errors.New("plain: RTF 中没有可提取的正文")
		}
		return Result{Markdown: md, Note: "本地 RTF 抽取"}, true, nil
	}
	return Result{}, false, nil
}

func extOrMime(ext, mime string) string {
	if ext != "" {
		return ext
	}
	if mime != "" {
		return mime
	}
	return "unknown"
}

func pickMIME(mime, ext string) string {
	if mime != "" {
		return mime
	}
	if ext == ".md" || ext == ".markdown" {
		return "text/markdown"
	}
	return "text/plain"
}

func isCodeExt(ext string) bool {
	switch ext {
	case ".txt", ".md", ".markdown", ".mdx", ".rst", ".adoc", ".org", ".csv", ".tsv", ".log":
		return false
	}
	return plainExts[ext]
}

// inferTitle 依次尝试：Markdown 一级标题、首个非空短行、文件名。
func inferTitle(text, fileName string) string {
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(t, "# "))
		}
	}
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if utf8.RuneCountInString(t) <= 80 {
			return t
		}
		break
	}
	base := filepath.Base(fileName)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
