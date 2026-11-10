// Package parse 定义"文档解析"这一环节的抽象。
//
// 解析器把原始文件转成 Markdown 文本。Larder 自身不做 OCR/版面分析，
// 全部交给外部服务（MinerU / Docling / Tika）或内置的纯文本兜底解析器。
package parse

import "context"

// Input 是一次解析请求。
type Input struct {
	FileName string // 文件名，用于判断类型
	MIME     string
	Data     []byte
}

// Result 是解析结果。
type Result struct {
	Markdown string // 统一输出 Markdown
	Title    string // 从内容推断的标题，可能为空
	MIME     string
	Method   string // 实际使用的解析器（plain / mineru / docling / tika）
	Note     string // 解析补充说明（页数、字符数、是否 OCR 等）
}

// Parser 是解析器接口。
type Parser interface {
	// Name 返回实现名，用于日志与 /healthz 展示。
	Name() string
	// Parse 将文件解析为 Markdown。
	Parse(ctx context.Context, in Input) (Result, error)
}
