package parse

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"
)

// extractPDFText 用纯 Go 的 PDF 库抽取文本层（适用于文字版 PDF）。
// 扫描件、纯图形/矢量路径导出的 PDF 没有文本层，会返回空文本并提示改用外部解析服务。
// 返回 (文本, 页数, 错误)。
func extractPDFText(data []byte) (text string, pages int, err error) {
	// 个别损坏的 PDF 会触发第三方库 panic，这里兜住并转成可读错误。
	defer func() {
		if rec := recover(); rec != nil {
			text = ""
			err = fmt.Errorf("无法读取 PDF（内容可能损坏或加密）: %v", rec)
		}
	}()

	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", 0, fmt.Errorf("无法读取 PDF：%w", err)
	}
	var sb strings.Builder
	totalPages := r.NumPage()
	for i := 1; i <= totalPages; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		pageText, pageErr := p.GetPlainText(nil)
		if pageErr != nil {
			// 单页失败不致命，跳过该页。
			continue
		}
		pageText = strings.TrimSpace(pageText)
		if pageText == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(pageText)
	}
	out := strings.TrimSpace(sb.String())
	if out == "" {
		return "", totalPages, fmt.Errorf("该 PDF 未包含可提取的文本层（共 %d 页，可能是扫描件、图片型或矢量图形 PDF）；请在「设置 → 解析服务」中配置 MinerU / Docling 等外部解析服务后重新处理", totalPages)
	}
	return out, totalPages, nil
}
