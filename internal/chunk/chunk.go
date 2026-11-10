// Package chunk 负责把 Markdown 切成检索用的块。
//
// 策略：标题感知。先按 Markdown 标题（#、##、###…）把文档切成节，
// 每个块都记住自己所属的标题路径；节内再按段落聚合成接近上限的块，
// 单块过大时按字符窗口外加重叠切分，避免切断语义边界太生硬。
package chunk

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"larder/internal/model"
)

// Options 控制切块尺寸。
type Options struct {
	MaxRunes int // 单块上限（按 rune 计）
	Overlap  int // 超大段落切分时的重叠
	MinRunes int // 过短的块尝试并入上一块
}

// Chunk 是切块中间产物（尚未落库）。
type Chunk struct {
	Ordinal int
	Heading string // 标题路径，如 "安装 > Windows"
	Text    string
}

// Split 把 Markdown 文本切成块。
func Split(markdown string, opt Options) []Chunk {
	if opt.MaxRunes <= 0 {
		opt.MaxRunes = 1200
	}
	if opt.Overlap < 0 {
		opt.Overlap = 0
	}
	if opt.Overlap >= opt.MaxRunes {
		opt.Overlap = opt.MaxRunes / 5
	}

	sections := splitSections(markdown)

	var chunks []Chunk
	ordinal := 0
	for _, sec := range sections {
		blocks := sectionChunks(sec, opt)
		for _, b := range blocks {
			text := strings.TrimSpace(b)
			if text == "" {
				continue
			}
			chunks = append(chunks, Chunk{
				Ordinal: ordinal,
				Heading: sec.heading,
				Text:    text,
			})
			ordinal++
		}
	}

	// 过短的尾块并入前一块，减少碎片。
	chunks = mergeTiny(chunks, opt.MinRunes, opt.MaxRunes)
	for i := range chunks {
		chunks[i].Ordinal = i
	}
	return chunks
}

// section 是标题切出的一节。
type section struct {
	heading string // 标题路径
	body    string
}

var headingPrefixes = []string{"######", "#####", "####", "###", "##", "#"}

// splitSections 按标题层级把文档切成节，并维护标题路径栈。
func splitSections(markdown string) []section {
	lines := strings.Split(markdown, "\n")

	stack := make([]string, 7) // 索引 1..6 对应 1..6 级标题
	var sections []section
	var current section
	var body strings.Builder

	flush := func() {
		current.body = body.String()
		if strings.TrimSpace(current.body) != "" || current.heading != "" {
			sections = append(sections, current)
		}
		body.Reset()
	}

	for _, line := range lines {
		level, title, ok := parseHeading(line)
		if !ok {
			body.WriteString(line)
			body.WriteByte('\n')
			continue
		}

		// 进入新标题前，落盘上一节。
		flush()

		stack[level] = title
		for i := level + 1; i < len(stack); i++ {
			stack[i] = ""
		}
		current.heading = joinHeading(stack)
	}

	flush()
	return sections
}

// parseHeading 识别 ATX 风格标题（#、##…），跳过代码围栏内的伪标题。
func parseHeading(line string) (level int, title string, ok bool) {
	trimmed := strings.TrimRight(line, " \t\r")
	for _, prefix := range headingPrefixes {
		if strings.HasPrefix(trimmed, prefix+" ") || trimmed == prefix {
			title = strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			// 去掉结尾的 # 收尾标记。
			title = strings.TrimSpace(strings.TrimRight(title, "#"))
			if title == "" {
				return 0, "", false
			}
			return len(prefix), title, true
		}
	}
	return 0, "", false
}

func joinHeading(stack []string) string {
	parts := make([]string, 0, len(stack))
	for i := 1; i < len(stack); i++ {
		if stack[i] != "" {
			parts = append(parts, stack[i])
		}
	}
	return strings.Join(parts, " > ")
}

// sectionChunks 把一节正文按段落聚合成不超过 MaxRunes 的块。
func sectionChunks(sec section, opt Options) []string {
	paragraphs := splitParagraphs(sec.body)

	var blocks []string
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			blocks = append(blocks, buf.String())
			buf.Reset()
		}
	}

	for _, p := range paragraphs {
		p = strings.TrimRight(p, " \t\r\n")
		if p == "" {
			continue
		}
		plen := utf8.RuneCountInString(p)

		// 单段就超限，先落盘当前累积，再把该段切块。
		if plen > opt.MaxRunes {
			flush()
			blocks = append(blocks, cutByRunes(p, opt.MaxRunes, opt.Overlap)...)
			continue
		}

		if buf.Len() > 0 && utf8.RuneCountInString(buf.String())+1+plen > opt.MaxRunes {
			flush()
		}
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(p)
	}
	flush()

	return blocks
}

// splitParagraphs 以空行为界拆段，同时把代码围栏作为整体保留。
func splitParagraphs(text string) []string {
	lines := strings.Split(text, "\n")
	var paras []string
	var cur strings.Builder
	inFence := false

	flush := func() {
		if cur.Len() > 0 {
			paras = append(paras, cur.String())
			cur.Reset()
		}
	}

	for _, line := range lines {
		fence := strings.HasPrefix(strings.TrimSpace(line), "```")
		if fence {
			inFence = !inFence
			cur.WriteString(line)
			cur.WriteByte('\n')
			continue
		}
		if !inFence && strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
	}
	flush()
	return paras
}

// cutByRunes 把超长文本按窗口 + 重叠切开，优先在空白处断开。
func cutByRunes(text string, max, overlap int) []string {
	runes := []rune(text)
	if len(runes) <= max {
		return []string{text}
	}

	var out []string
	start := 0
	for start < len(runes) {
		end := start + max
		if end > len(runes) {
			end = len(runes)
		}
		// 尽量在断点前的空白处收尾，避免切词。
		if end < len(runes) {
			if cut := lastBreak(runes, start, end); cut > start+max/2 {
				end = cut
			}
		}
		out = append(out, string(runes[start:end]))
		if end >= len(runes) {
			break
		}
		next := end - overlap
		if next <= start {
			next = end
		}
		start = next
	}
	return out
}

// lastBreak 返回 [start,end) 内最后一个合适的断点位置。
func lastBreak(runes []rune, start, end int) int {
	for i := end - 1; i > start; i-- {
		switch runes[i] {
		case '\n', '。', '！', '？', '.', '!', '?', ' ', '\t', '；', ';':
			return i + 1
		}
	}
	return end
}

// mergeTiny 把过短的块并入前一块（若合并后不超上限）。
func mergeTiny(chunks []Chunk, minRunes, maxRunes int) []Chunk {
	if minRunes <= 0 || len(chunks) < 2 {
		return chunks
	}
	out := make([]Chunk, 0, len(chunks))
	for _, c := range chunks {
		if len(out) > 0 && utf8.RuneCountInString(c.Text) < minRunes {
			prev := &out[len(out)-1]
			merged := prev.Text + "\n\n" + c.Text
			// 只有同一标题路径下才合并，避免污染引用。
			if prev.Heading == c.Heading && utf8.RuneCountInString(merged) <= maxRunes {
				prev.Text = merged
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// ToModelChunks 把中间块转成带 docID 的领域对象，便于落库。
func ToModelChunks(docID string, chunks []Chunk) []model.Chunk {
	out := make([]model.Chunk, len(chunks))
	for i, c := range chunks {
		out[i] = model.Chunk{
			DocID:   docID,
			Ordinal: c.Ordinal,
			Heading: c.Heading,
			Text:    c.Text,
		}
	}
	return out
}

// isSpace 判断 rune 是否空白。
func isSpace(r rune) bool { return unicode.IsSpace(r) }
