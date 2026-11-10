package parse

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ---------- DOCX ----------

type docxState struct {
	lines  []string
	para   strings.Builder
	style  string
	inText bool
	inCell bool
	cell   strings.Builder
	row    []string
	table  [][]string
	inTbl  bool
}

// extractDOCX 从 Word 文档提取段落、标题与表格，输出 Markdown。
func extractDOCX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("不是有效的 DOCX 文件: %w", err)
	}
	var body []byte
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			body, err = readZipFile(f, 256<<20)
			if err != nil {
				return "", err
			}
			break
		}
	}
	if len(body) == 0 {
		return "", fmt.Errorf("DOCX 中缺少 word/document.xml")
	}

	var st docxState
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("解析 document.xml 失败: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				st.para.Reset()
				st.style = ""
			case "pStyle":
				st.style = attrValue(t, "val")
			case "numPr":
				if st.style == "" {
					st.style = "ListParagraph"
				}
			case "t":
				st.inText = true
			case "tab":
				st.para.WriteByte('\t')
			case "br", "cr":
				st.para.WriteByte('\n')
			case "tbl":
				st.inTbl = true
				st.lines = append(st.lines, "")
			case "tr":
				st.row = nil
			case "tc":
				st.cell.Reset()
				st.inCell = true
			}
		case xml.CharData:
			if st.inText {
				if st.inCell {
					st.cell.Write([]byte(t))
				}
				st.para.Write([]byte(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				st.inText = false
			case "tc":
				st.inCell = false
				st.row = append(st.row, strings.TrimSpace(st.cell.String()))
			case "tr":
				if len(st.row) > 0 {
					st.table = append(st.table, st.row)
				}
				st.row = nil
			case "tbl":
				st.inTbl = false
				if len(st.table) > 0 {
					st.lines = append(st.lines, "", renderTable(st.table), "")
					st.table = nil
				}
			case "p":
				text := strings.TrimSpace(st.para.String())
				if st.inCell {
					// 单元格内段落已进入 cell，避免重复加入正文。
					st.para.Reset()
					continue
				}
				if text == "" {
					continue
				}
				prefix := ""
				style := strings.ToLower(strings.ReplaceAll(st.style, " ", ""))
				switch {
				case style == "title":
					prefix = "# "
				case strings.HasPrefix(style, "heading"):
					level := parseTrailingInt(style, "heading")
					prefix = strings.Repeat("#", clampInt(level, 1, 6)) + " "
				case strings.Contains(st.style, "标题"):
					level := parseTrailingInt(style, "标题")
					prefix = strings.Repeat("#", clampInt(level, 1, 6)) + " "
				case style == "listparagraph":
					prefix = "- "
				}
				st.lines = append(st.lines, prefix+text)
			}
		}
	}
	out := cleanMarkdownLines(st.lines)
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("文档中没有可提取的文字")
	}
	return out, nil
}

func attrValue(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func parseTrailingInt(s, prefix string) int {
	s = strings.TrimPrefix(s, prefix)
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 {
		return 1
	}
	return n
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func renderTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	maxCols := 0
	for _, r := range rows {
		if len(r) > maxCols {
			maxCols = len(r)
		}
	}
	if maxCols == 0 {
		return ""
	}
	var b strings.Builder
	writeRow := func(r []string) {
		b.WriteString("|")
		for i := 0; i < maxCols; i++ {
			v := ""
			if i < len(r) {
				v = strings.ReplaceAll(strings.ReplaceAll(r[i], "|", "\\|"), "\n", " ")
			}
			b.WriteString(" " + v + " |")
		}
		b.WriteByte('\n')
	}
	writeRow(rows[0])
	b.WriteString("|")
	for i := 0; i < maxCols; i++ {
		b.WriteString(" --- |")
	}
	b.WriteByte('\n')
	for _, r := range rows[1:] {
		writeRow(r)
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---------- PPTX ----------

// extractPPTX 按页码提取每页文本框中的文字，输出 Markdown。
func extractPPTX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("不是有效的 PPTX 文件: %w", err)
	}
	type slide struct {
		num  int
		name string
	}
	var slides []slide
	for _, f := range zr.File {
		name := strings.ToLower(f.Name)
		if strings.HasPrefix(name, "ppt/slides/slide") && strings.HasSuffix(name, ".xml") {
			n := atoiSafe(strings.TrimSuffix(strings.TrimPrefix(name, "ppt/slides/slide"), ".xml"))
			slides = append(slides, slide{num: n, name: f.Name})
		}
	}
	if len(slides) == 0 {
		return "", fmt.Errorf("PPTX 中没有幻灯片")
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].num < slides[j].num })

	var b strings.Builder
	for _, s := range slides {
		f, err := zr.Open(s.name)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(f, 64<<20))
		f.Close()
		if err != nil {
			continue
		}
		paras := pptxParagraphs(body)
		if len(paras) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## 幻灯片 %d\n\n", s.num)
		for _, p := range paras {
			b.WriteString(strings.TrimSpace(p))
			b.WriteString("\n\n")
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", fmt.Errorf("幻灯片中没有可提取的文字")
	}
	return out, nil
}

func pptxParagraphs(data []byte) []string {
	var out []string
	var cur strings.Builder
	inText := false
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				cur.Reset()
			case "t":
				inText = true
			}
		case xml.CharData:
			if inText {
				cur.Write([]byte(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				if text := strings.TrimSpace(cur.String()); text != "" {
					out = append(out, text)
				}
			}
		}
	}
	return out
}

// ---------- XLSX ----------

// extractXLSX 提取每个工作表的单元格，输出 Markdown 表格。
func extractXLSX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("不是有效的 XLSX 文件: %w", err)
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[strings.ToLower(f.Name)] = f
	}

	shared := parseSharedStrings(files)
	sheets := parseWorkbookSheets(zr, files)

	var b strings.Builder
	for _, s := range sheets {
		f := files[strings.ToLower(s.target)]
		if f == nil {
			continue
		}
		rows, truncated, err := parseSheetRows(f, shared)
		if err != nil || len(rows) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n", s.name)
		b.WriteString(renderTable(rows))
		b.WriteString("\n\n")
		if truncated {
			b.WriteString("> 表格行数过多，仅保留前 2000 行。\n\n")
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", fmt.Errorf("工作簿中没有可提取的单元格文字")
	}
	return out, nil
}

func parseSharedStrings(files map[string]*zip.File) []string {
	f := files["xl/sharedstrings.xml"]
	if f == nil {
		return nil
	}
	body, err := readZipFile(f, 256<<20)
	if err != nil {
		return nil
	}
	var out []string
	var cur strings.Builder
	inText := false
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				cur.Reset()
			case "t":
				inText = true
			}
		case xml.CharData:
			if inText {
				cur.Write([]byte(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "si":
				out = append(out, cur.String())
			}
		}
	}
	return out
}

type sheetRef struct {
	name   string
	target string
}

func parseWorkbookSheets(zr *zip.Reader, files map[string]*zip.File) []sheetRef {
	rels := map[string]string{}
	if f := files["xl/_rels/workbook.xml.rels"]; f != nil {
		if body, err := readZipFile(f, 8<<20); err == nil {
			dec := xml.NewDecoder(bytes.NewReader(body))
			for {
				tok, err := dec.Token()
				if err != nil {
					break
				}
				if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
					id := attrValue(se, "Id")
					target := attrValue(se, "Target")
					if id != "" && target != "" {
						if strings.HasPrefix(target, "/") {
							target = strings.TrimPrefix(target, "/")
						} else {
							target = path.Clean(path.Join("xl", target))
						}
						rels[id] = strings.TrimPrefix(target, "/")
					}
				}
			}
		}
	}

	var sheets []sheetRef
	if f := files["xl/workbook.xml"]; f != nil {
		if body, err := readZipFile(f, 8<<20); err == nil {
			dec := xml.NewDecoder(bytes.NewReader(body))
			idx := 0
			for {
				tok, err := dec.Token()
				if err != nil {
					break
				}
				if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "sheet" {
					idx++
					name := attrValue(se, "name")
					rid := ""
					for _, a := range se.Attr {
						if a.Name.Local == "id" {
							rid = a.Value
						}
					}
					target := rels[rid]
					if target == "" {
						target = fmt.Sprintf("xl/worksheets/sheet%d.xml", idx)
					}
					sheets = append(sheets, sheetRef{name: name, target: target})
				}
			}
		}
	}
	if len(sheets) == 0 {
		// 兜底：按文件名排序。
		var names []string
		for name := range files {
			if strings.HasPrefix(name, "xl/worksheets/sheet") && strings.HasSuffix(name, ".xml") {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for i, n := range names {
			sheets = append(sheets, sheetRef{name: fmt.Sprintf("Sheet %d", i+1), target: n})
		}
	}
	return sheets
}

func parseSheetRows(f *zip.File, shared []string) ([][]string, bool, error) {
	body, err := readZipFile(f, 256<<20)
	if err != nil {
		return nil, false, err
	}
	var rows [][]string
	var row []string
	var cell strings.Builder
	cellType := ""
	inCell := false
	inValue := false
	inInline := false
	inText := false
	truncated := false
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return rows, truncated, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				row = nil
			case "c":
				cell.Reset()
				cellType = attrValue(t, "t")
				inCell = true
			case "v":
				inValue = true
			case "is":
				inInline = true
			case "t":
				if inInline {
					inText = true
				}
			}
		case xml.CharData:
			if (inValue || inText) && inCell {
				cell.Write([]byte(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inValue = false
			case "t":
				inText = false
			case "is":
				inInline = false
			case "c":
				inCell = false
				val := cell.String()
				if cellType == "s" {
					if idx := atoiSafe(val); idx >= 0 && idx < len(shared) {
						val = shared[idx]
					}
				} else if cellType == "b" {
					if strings.TrimSpace(val) == "1" {
						val = "TRUE"
					} else {
						val = "FALSE"
					}
				}
				row = append(row, strings.TrimSpace(val))
			case "row":
				if len(row) > 0 {
					if len(rows) >= 2000 {
						truncated = true
						return rows, truncated, nil
					}
					rows = append(rows, row)
				}
				row = nil
			}
		}
	}
	return rows, truncated, nil
}

func readZipFile(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, limit))
}

func atoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// ---------- EPUB ----------

// extractEPUB 按 spine 顺序拼接 EPUB 中的 XHTML，输出 Markdown。
func extractEPUB(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("不是有效的 EPUB 文件: %w", err)
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[strings.ToLower(f.Name)] = f
	}

	opfPath := ""
	if f := files["meta-inf/container.xml"]; f != nil {
		if body, err := readZipFile(f, 4<<20); err == nil {
			re := regexp.MustCompile(`(?i)full-path="([^"]+)"`)
			if m := re.FindSubmatch(body); len(m) == 2 {
				opfPath = string(m[1])
			}
		}
	}
	if opfPath == "" {
		for name := range files {
			if strings.HasSuffix(name, ".opf") {
				opfPath = name
				break
			}
		}
	}

	var htmlFiles []string
	if opfPath != "" {
		if f := files[strings.ToLower(opfPath)]; f != nil {
			body, _ := readZipFile(f, 16<<20)
			htmlFiles = epubSpine(body, opfPath)
		}
	}
	if len(htmlFiles) == 0 {
		for name := range files {
			if strings.HasSuffix(name, ".xhtml") || strings.HasSuffix(name, ".html") || strings.HasSuffix(name, ".htm") {
				htmlFiles = append(htmlFiles, name)
			}
		}
		sort.Strings(htmlFiles)
	}

	var b strings.Builder
	for _, name := range htmlFiles {
		f := files[strings.ToLower(name)]
		if f == nil {
			continue
		}
		body, err := readZipFile(f, 32<<20)
		if err != nil {
			continue
		}
		md := htmlToMarkdown(string(body))
		if strings.TrimSpace(md) == "" {
			continue
		}
		b.WriteString(md)
		b.WriteString("\n\n")
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", fmt.Errorf("EPUB 中没有可提取的正文")
	}
	return out, nil
}

func epubSpine(opf []byte, opfPath string) []string {
	type item struct{ href string }
	items := map[string]item{}
	var order []string
	dec := xml.NewDecoder(bytes.NewReader(opf))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "item":
				id := attrValue(t, "id")
				href := attrValue(t, "href")
				if id != "" && href != "" {
					items[id] = item{href: href}
				}
			case "itemref":
				idref := attrValue(t, "idref")
				if idref != "" {
					order = append(order, idref)
				}
			}
		case xml.EndElement:
			_ = t
		}
	}
	base := path.Dir(opfPath)
	var out []string
	for _, id := range order {
		it, ok := items[id]
		if !ok {
			continue
		}
		href := it.href
		if i := strings.IndexByte(href, '#'); i >= 0 {
			href = href[:i]
		}
		out = append(out, path.Clean(path.Join(base, href)))
	}
	return out
}

// ---------- HTML ----------

var (
	reScripts = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<script[^>]*>.*?</script\s*>`),
		regexp.MustCompile(`(?is)<style[^>]*>.*?</style\s*>`),
		regexp.MustCompile(`(?is)<noscript[^>]*>.*?</noscript\s*>`),
		regexp.MustCompile(`(?is)<svg[^>]*>.*?</svg\s*>`),
		regexp.MustCompile(`(?is)<iframe[^>]*>.*?</iframe\s*>`),
	}
	reTitle    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title\s*>`)
	reHeading  = regexp.MustCompile(`(?i)<h([1-6])[^>]*>`)
	reBlockEnd = regexp.MustCompile(`(?i)</(p|div|section|article|header|footer|blockquote|pre|tr|ul|ol|table|figure|figcaption)\s*>`)
	reLi       = regexp.MustCompile(`(?i)<li[^>]*>`)
	reBr       = regexp.MustCompile(`(?i)<br\s*/?>`)
	reCell     = regexp.MustCompile(`(?i)</t[dh]\s*>`)
	reAnyTag   = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpaces   = regexp.MustCompile(`[ \t\f\v]+`)
	reBlank    = regexp.MustCompile(`\n{3,}`)
)

// htmlToMarkdown 做轻量的 HTML→文本/标题转换，足够用于网页与 EPUB。
func htmlToMarkdown(src string) string {
	pageTitle := ""
	if m := reTitle.FindStringSubmatch(src); len(m) == 2 {
		pageTitle = strings.TrimSpace(html.UnescapeString(reAnyTag.ReplaceAllString(m[1], "")))
	}
	for _, re := range reScripts {
		src = re.ReplaceAllString(src, "")
	}
	src = reHeading.ReplaceAllStringFunc(src, func(tag string) string {
		m := reHeading.FindStringSubmatch(tag)
		if len(m) != 2 {
			return "\n\n"
		}
		n := atoiSafe(m[1])
		if n < 1 || n > 6 {
			n = 2
		}
		return "\n\n" + strings.Repeat("#", n) + " "
	})
	src = reLi.ReplaceAllString(src, "\n- ")
	src = reBr.ReplaceAllString(src, "\n")
	src = reCell.ReplaceAllString(src, " | ")
	src = reBlockEnd.ReplaceAllString(src, "\n\n")
	src = reAnyTag.ReplaceAllString(src, "")
	src = html.UnescapeString(src)
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.ReplaceAll(src, "\r", "\n")

	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(reSpaces.ReplaceAllString(line, " "))
		line = strings.TrimPrefix(line, "| ")
		line = strings.TrimSuffix(line, " |")
		if line == "" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		out = append(out, line)
	}
	text := strings.TrimSpace(reBlank.ReplaceAllString(strings.Join(out, "\n"), "\n\n"))
	if pageTitle != "" && text != "" && !strings.HasPrefix(text, "# "+pageTitle) {
		text = "# " + pageTitle + "\n\n" + text
	}
	return text
}

// ---------- RTF ----------

var (
	reRTFCtrl = regexp.MustCompile(`\\[a-zA-Z]+-?\d* ?`)
	reRTFHex  = regexp.MustCompile(`\\'([0-9a-fA-F]{2})`)
	reRTFUni  = regexp.MustCompile(`\\u(-?\d+)\??`)
)

// extractRTF 做基础 RTF 文本抽取（支持 \par、\uN 与 \'hh 转义）。
func extractRTF(src string) string {
	s := reRTFHex.ReplaceAllStringFunc(src, func(m string) string {
		sub := reRTFHex.FindStringSubmatch(m)
		if len(sub) != 2 {
			return ""
		}
		b, _ := strconv.ParseUint(sub[1], 16, 8)
		return string([]byte{byte(b)})
	})
	s = reRTFUni.ReplaceAllStringFunc(s, func(m string) string {
		sub := reRTFUni.FindStringSubmatch(m)
		if len(sub) != 2 {
			return ""
		}
		n, _ := strconv.Atoi(sub[1])
		return string(rune(n))
	})
	s = strings.ReplaceAll(s, `\par`, "\n")
	s = strings.ReplaceAll(s, `\line`, "\n")
	s = strings.ReplaceAll(s, `\tab`, "\t")
	s = strings.ReplaceAll(s, `\{`, "{")
	s = strings.ReplaceAll(s, `\}`, "}")
	s = strings.ReplaceAll(s, `\\`, `\`)
	s = reRTFCtrl.ReplaceAllString(s, "")
	s = strings.NewReplacer("{", "", "}", "").Replace(s)
	lines := strings.Split(s, "\n")
	var out []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n\n"))
}

// cleanMarkdownLines 去掉空行堆叠，保留段落间距。
func cleanMarkdownLines(lines []string) string {
	var out []string
	for _, line := range lines {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
