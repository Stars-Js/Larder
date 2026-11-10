package parse

import (
	"context"
	"fmt"
	"strings"
)

// Hybrid 先尝试内置解析器（文本、文字版 PDF、Office、HTML 等），
// 本地无法提取时再调用外部解析服务（MinerU / Docling / Tika）。
// 这正是扫描件/矢量图形 PDF（如无文本层的 1.pdf）的兜底路径。
type Hybrid struct {
	local    Parser
	external Parser
}

// NewHybrid 组合内置解析器与外部解析服务；external 可为 nil。
func NewHybrid(local, external Parser) *Hybrid {
	return &Hybrid{local: local, external: external}
}

// Name 返回实现名。
func (h *Hybrid) Name() string {
	if h.external != nil {
		return "auto+" + h.external.Name()
	}
	return "auto"
}

// Parse 本地优先，失败后自动回退到外部解析服务。
func (h *Hybrid) Parse(ctx context.Context, in Input) (Result, error) {
	localRes, localErr := h.local.Parse(ctx, in)
	if localErr == nil && strings.TrimSpace(localRes.Markdown) != "" {
		return localRes, nil
	}
	if h.external == nil {
		if localErr != nil {
			return Result{}, localErr
		}
		return Result{}, fmt.Errorf("plain: 未提取到文本内容，请配置外部解析服务（如 MinerU / Docling）")
	}

	extRes, extErr := h.external.Parse(ctx, in)
	if extErr == nil && strings.TrimSpace(extRes.Markdown) != "" {
		extRes.Method = h.external.Name()
		if extRes.Note == "" {
			extRes.Note = "外部解析服务"
		} else {
			extRes.Note = "外部解析服务：" + extRes.Note
		}
		return extRes, nil
	}
	if extErr == nil {
		extErr = fmt.Errorf("外部解析服务返回空内容")
	}
	if localErr != nil {
		return Result{}, fmt.Errorf("本地解析失败：%v；外部解析（%s）也失败：%w", localErr, h.external.Name(), extErr)
	}
	return Result{}, fmt.Errorf("本地未提取到文本；外部解析（%s）失败：%w", h.external.Name(), extErr)
}
