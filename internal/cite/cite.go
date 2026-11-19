// Package cite 把文献元数据格式化为参考文献、行内引用和 BibTeX。
// 覆盖中文学术最常用的 GB/T 7714-2015，以及 APA 7、IEEE、Vancouver。
package cite

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// 支持的样式 ID。
const (
	StyleGBTNumeric    = "gb-t-7714-numeric"
	StyleGBTAuthorDate = "gb-t-7714-author-date"
	StyleAPA           = "apa"
	StyleIEEE          = "ieee"
	StyleVancouver     = "vancouver"
	StyleMLA           = "mla"
)

// Author 是格式化后的作者。
type Author struct {
	First string
	Last  string
	Name  string // 机构作者
}

// Reference 是一条已编号的参考文献。
type Reference struct {
	Number int    `json:"number"`
	Item   Item   `json:"item"`
	Text   string `json:"text"`
	Mark   string `json:"mark"`
	BibTeX string `json:"bibtex,omitempty"`
}

// BuildReferences 按输入顺序编号并格式化参考文献。
func BuildReferences(items []Item, style string) []Reference {
	out := make([]Reference, 0, len(items))
	seen := map[string]bool{}
	for _, it := range items {
		key := it.Key
		if key == "" {
			key = it.Title + "|" + strconv.Itoa(it.Year)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		n := len(out) + 1
		out = append(out, Reference{
			Number: n,
			Item:   it,
			Text:   Format(it, style),
			Mark:   InText(it, n, style),
			BibTeX: BibTeX(it),
		})
	}
	return out
}

// Item 是引用所需的文献元数据。
type Item struct {
	Key       string
	ItemType  string
	Title     string
	Authors   []string // "Last, First" 或机构名
	Year      int
	Date      string
	Journal   string
	Volume    string
	Issue     string
	Pages     string
	Publisher string
	Place     string
	DOI       string
	URL       string
	Language  string
	Abstract  string
}

// NormalizeStyle 把别名归一化为支持的样式 ID。
func NormalizeStyle(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "gb/t", "gbt", "gb-t", "gb-t-7714", "gb-t-7714-2015", "gb-t-7714-numeric", "numeric":
		return StyleGBTNumeric
	case "gb-t-7714-author-date", "author-date", "author_date":
		return StyleGBTAuthorDate
	case "apa", "apa7", "apa-7":
		return StyleAPA
	case "ieee":
		return StyleIEEE
	case "vancouver":
		return StyleVancouver
	case "mla":
		return StyleMLA
	default:
		return StyleGBTNumeric
	}
}

// MarkForStyle 返回样式规定的数字引用标记：GB/T、IEEE 用 [1]，Vancouver 用 (1)。
func MarkForStyle(style string, number int) string {
	n := strconv.Itoa(number)
	switch NormalizeStyle(style) {
	case StyleVancouver:
		return "(" + n + ")"
	default:
		return "[" + n + "]"
	}
}

// IsNumeric 报告样式是否使用数字引用。
func IsNumeric(style string) bool {
	switch NormalizeStyle(style) {
	case StyleIEEE, StyleVancouver:
		return true
	default:
		return NormalizeStyle(style) == StyleGBTNumeric
	}
}

// InText 返回样式规定的行内引用：数字样式为 [1]/(1)，著者-年份样式为 (作者, 年)。
func InText(it Item, number int, style string) string {
	if IsNumeric(style) {
		return MarkForStyle(style, number)
	}
	a := shortAuthor(it)
	if a == "" {
		a = shortTitle(it)
	}
	if it.Year > 0 {
		return "(" + a + ", " + strconv.Itoa(it.Year) + ")"
	}
	return "(" + a + ")"
}

// Format 返回一条完整的参考文献（不含编号）。
func Format(it Item, style string) string {
	switch NormalizeStyle(style) {
	case StyleAPA:
		return formatAPA(it)
	case StyleIEEE:
		return formatIEEE(it)
	case StyleVancouver:
		return formatVancouver(it)
	case StyleMLA:
		return formatMLA(it)
	default:
		return formatGBT(it)
	}
}

func formatGBT(it Item) string {
	authors := gbtAuthors(it.Authors, it.Year, isChinese(it.Language, it.Authors))
	title := strings.TrimSpace(it.Title)
	if title == "" {
		title = "无题名"
	}
	kind := gbtType(it.ItemType)
	var b strings.Builder
	if authors != "" {
		// initials() 末尾自带句点，避免出现 “VASWANI A..” 这样的双句点。
		b.WriteString(strings.TrimSuffix(authors, "."))
		b.WriteString(". ")
	}
	b.WriteString(title)
	b.WriteString(kind)
	b.WriteString(". ")
	switch gbtKind(it.ItemType) {
	case "book", "thesis", "report":
		if it.Place != "" {
			b.WriteString(it.Place)
			b.WriteString(": ")
		}
		if it.Publisher != "" {
			b.WriteString(it.Publisher)
		}
		if it.Publisher != "" || it.Place != "" {
			b.WriteString(", ")
		}
		if it.Year > 0 {
			b.WriteString(strconv.Itoa(it.Year))
		}
		b.WriteString(".")
	case "web":
		if it.Year > 0 {
			b.WriteString("(")
			b.WriteString(strconv.Itoa(it.Year))
			b.WriteString("). ")
		}
		if it.URL != "" {
			b.WriteString(it.URL)
			b.WriteString(".")
		}
	default:
		if it.Journal != "" {
			b.WriteString(it.Journal)
			if it.Year > 0 {
				b.WriteString(", ")
				b.WriteString(strconv.Itoa(it.Year))
			}
			if it.Volume != "" {
				b.WriteString(", ")
				b.WriteString(it.Volume)
				if it.Issue != "" {
					b.WriteString("(")
					b.WriteString(it.Issue)
					b.WriteString(")")
				}
			} else if it.Issue != "" {
				b.WriteString(", (")
				b.WriteString(it.Issue)
				b.WriteString(")")
			}
			if it.Pages != "" {
				b.WriteString(": ")
				b.WriteString(it.Pages)
			}
			b.WriteString(".")
		} else if it.Publisher != "" {
			if it.Place != "" {
				b.WriteString(it.Place)
				b.WriteString(": ")
			}
			b.WriteString(it.Publisher)
			if it.Year > 0 {
				b.WriteString(", ")
				b.WriteString(strconv.Itoa(it.Year))
			}
			b.WriteString(".")
		} else if it.Year > 0 {
			b.WriteString(strconv.Itoa(it.Year))
			b.WriteString(".")
		}
	}
	if it.DOI != "" {
		b.WriteString(" DOI: ")
		b.WriteString(it.DOI)
		b.WriteString(".")
	}
	return strings.TrimSpace(b.String())
}

func gbtKind(itemType string) string {
	switch strings.ToLower(itemType) {
	case "book":
		return "book"
	case "booksection", "book-section", "chapter":
		return "chapter"
	case "conferencepaper", "conference-paper":
		return "conference"
	case "thesis", "dissertation":
		return "thesis"
	case "report":
		return "report"
	case "webpage", "blogpost", "forumpost":
		return "web"
	case "preprint":
		return "preprint"
	default:
		return "article"
	}
}

func gbtType(itemType string) string {
	switch gbtKind(itemType) {
	case "book":
		return "[M]"
	case "chapter":
		return "[M]//"
	case "conference":
		return "[C]"
	case "thesis":
		return "[D]"
	case "report":
		return "[R]"
	case "web":
		return "[EB/OL]"
	case "preprint":
		return "[J/OL]"
	default:
		return "[J]"
	}
}

func formatAPA(it Item) string {
	authors := apaAuthors(it.Authors)
	var b strings.Builder
	if authors != "" {
		b.WriteString(authors)
		b.WriteString(" ")
	}
	if it.Year > 0 {
		b.WriteString("(")
		b.WriteString(strconv.Itoa(it.Year))
		b.WriteString("). ")
	}
	title := strings.TrimSpace(it.Title)
	if title == "" {
		title = "Untitled"
	}
	b.WriteString(title)
	b.WriteString(". ")
	switch gbtKind(it.ItemType) {
	case "book":
		if it.Publisher != "" {
			b.WriteString(it.Publisher)
			b.WriteString(". ")
		}
	case "chapter":
		if it.Journal != "" {
			b.WriteString("In ")
			b.WriteString(it.Journal)
			b.WriteString(". ")
		}
		if it.Publisher != "" {
			b.WriteString(it.Publisher)
			b.WriteString(". ")
		}
	default:
		if it.Journal != "" {
			b.WriteString(it.Journal)
			if it.Volume != "" {
				b.WriteString(", ")
				b.WriteString(it.Volume)
				if it.Issue != "" {
					b.WriteString("(")
					b.WriteString(it.Issue)
					b.WriteString(")")
				}
			}
			if it.Pages != "" {
				b.WriteString(", ")
				b.WriteString(it.Pages)
			}
			b.WriteString(". ")
		}
	}
	if it.DOI != "" {
		b.WriteString("https://doi.org/")
		b.WriteString(strings.TrimPrefix(it.DOI, "https://doi.org/"))
	} else if it.URL != "" {
		b.WriteString(it.URL)
	}
	return strings.TrimSpace(b.String())
}

func formatIEEE(it Item) string {
	authors := ieeeAuthors(it.Authors)
	var b strings.Builder
	if authors != "" {
		b.WriteString(authors)
		b.WriteString(", ")
	}
	title := strings.TrimSpace(it.Title)
	if title == "" {
		title = "Untitled"
	}
	b.WriteString("\"")
	b.WriteString(title)
	b.WriteString(",\" ")
	switch gbtKind(it.ItemType) {
	case "book":
		if it.Publisher != "" {
			b.WriteString(it.Publisher)
			b.WriteString(", ")
		}
	case "conference":
		if it.Journal != "" {
			b.WriteString("in ")
			b.WriteString(it.Journal)
			b.WriteString(", ")
		}
	default:
		if it.Journal != "" {
			b.WriteString(it.Journal)
			b.WriteString(", ")
		}
	}
	if it.Volume != "" {
		b.WriteString("vol. ")
		b.WriteString(it.Volume)
		b.WriteString(", ")
	}
	if it.Issue != "" {
		b.WriteString("no. ")
		b.WriteString(it.Issue)
		b.WriteString(", ")
	}
	if it.Pages != "" {
		b.WriteString("pp. ")
		b.WriteString(it.Pages)
		b.WriteString(", ")
	}
	if it.Year > 0 {
		b.WriteString(strconv.Itoa(it.Year))
	}
	if it.DOI != "" {
		if it.Year > 0 {
			b.WriteString(", ")
		}
		b.WriteString("doi: ")
		b.WriteString(it.DOI)
	}
	b.WriteString(".")
	return strings.TrimSpace(b.String())
}

func formatVancouver(it Item) string {
	authors := vancouverAuthors(it.Authors)
	var b strings.Builder
	if authors != "" {
		b.WriteString(authors)
		b.WriteString(". ")
	}
	title := strings.TrimSpace(it.Title)
	if title == "" {
		title = "Untitled"
	}
	b.WriteString(title)
	b.WriteString(". ")
	if it.Journal != "" {
		b.WriteString(it.Journal)
		b.WriteString(". ")
	}
	if it.Year > 0 {
		b.WriteString(strconv.Itoa(it.Year))
	}
	if it.Volume != "" {
		b.WriteString(";")
		b.WriteString(it.Volume)
		if it.Issue != "" {
			b.WriteString("(")
			b.WriteString(it.Issue)
			b.WriteString(")")
		}
	}
	if it.Pages != "" {
		b.WriteString(":")
		b.WriteString(it.Pages)
	}
	b.WriteString(".")
	if it.DOI != "" {
		b.WriteString(" doi: ")
		b.WriteString(it.DOI)
		b.WriteString(".")
	}
	return strings.TrimSpace(b.String())
}

func formatMLA(it Item) string {
	authors := mlaAuthors(it.Authors)
	title := strings.TrimSpace(it.Title)
	if title == "" {
		title = "Untitled"
	}
	var b strings.Builder
	if authors != "" {
		b.WriteString(authors)
		b.WriteString(". ")
	}
	b.WriteString("\"")
	b.WriteString(title)
	b.WriteString(".\" ")
	if it.Journal != "" {
		b.WriteString(it.Journal)
		b.WriteString(", ")
	}
	if it.Volume != "" {
		b.WriteString("vol. ")
		b.WriteString(it.Volume)
		b.WriteString(", ")
	}
	if it.Issue != "" {
		b.WriteString("no. ")
		b.WriteString(it.Issue)
		b.WriteString(", ")
	}
	if it.Year > 0 {
		b.WriteString(strconv.Itoa(it.Year))
		b.WriteString(", ")
	}
	if it.Pages != "" {
		b.WriteString("pp. ")
		b.WriteString(it.Pages)
		b.WriteString(". ")
	}
	if it.DOI != "" {
		b.WriteString("doi:")
		b.WriteString(it.DOI)
		b.WriteString(".")
	} else if it.URL != "" {
		b.WriteString(it.URL)
		b.WriteString(".")
	}
	return strings.TrimSpace(b.String())
}

// BibTeX 生成一条 BibTeX 记录。
func BibTeX(it Item) string {
	key := CitationKey(it)
	entry := bibType(it.ItemType)
	fields := [][2]string{
		{"author", bibAuthors(it.Authors)},
		{"title", it.Title},
		{"year", yearString(it.Year)},
		{"journal", it.Journal},
		{"booktitle", ""},
		{"volume", it.Volume},
		{"number", it.Issue},
		{"pages", strings.ReplaceAll(it.Pages, "-", "--")},
		{"publisher", it.Publisher},
		{"address", it.Place},
		{"doi", it.DOI},
		{"url", it.URL},
	}
	if entry == "inproceedings" || entry == "incollection" {
		fields[3][1] = ""
		fields[4][1] = it.Journal
	}
	var b strings.Builder
	fmt.Fprintf(&b, "@%s{%s,\n", entry, key)
	for _, f := range fields {
		if strings.TrimSpace(f[1]) == "" {
			continue
		}
		fmt.Fprintf(&b, "  %s = {%s},\n", f[0], escapeBib(f[1]))
	}
	// 去掉最后一行逗号后的换行，形成合法 BibTeX。
	out := strings.TrimSuffix(b.String(), ",\n") + "\n}\n"
	return out
}

func bibType(itemType string) string {
	switch strings.ToLower(itemType) {
	case "book":
		return "book"
	case "booksection", "book-section", "chapter":
		return "incollection"
	case "conferencepaper", "conference-paper":
		return "inproceedings"
	case "thesis", "dissertation":
		return "phdthesis"
	case "report":
		return "techreport"
	case "webpage", "blogpost", "forumpost", "preprint":
		return "misc"
	case "manuscript":
		return "unpublished"
	default:
		return "article"
	}
}

// CitationKey 返回稳定的 BibTeX key：优先 Zotero/BBT 提供的 key。
func CitationKey(it Item) string {
	if k := sanitizeKey(it.Key); k != "" {
		return k
	}
	last := ""
	if len(it.Authors) > 0 {
		last = authorLastName(it.Authors[0])
	}
	word := firstTitleWord(it.Title)
	base := strings.ToLower(sanitizeLetters(last) + strconv.Itoa(it.Year) + sanitizeLetters(word))
	if base == "" {
		base = "ref" + strconv.Itoa(it.Year)
	}
	return base
}

func sanitizeKey(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == ':' || r == '.' {
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), ".:_-")
}

func sanitizeLetters(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func firstTitleWord(title string) string {
	for _, w := range strings.Fields(title) {
		w = strings.Trim(w, ".,:;!?()[]{}<>\"'")
		if len([]rune(w)) >= 3 {
			return w
		}
	}
	return ""
}

func yearString(y int) string {
	if y <= 0 {
		return ""
	}
	return strconv.Itoa(y)
}

func escapeBib(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\textbackslash{}`,
		`&`, `\&`,
		`%`, `\%`,
		`$`, `\$`,
		`#`, `\#`,
		`_`, `\_`,
		`{`, `\{`,
		`}`, `\}`,
		`~`, `\textasciitilde{}`,
		`^`, `\textasciicircum{}`,
	)
	return replacer.Replace(s)
}

// ---- 作者格式化 ----

func parseAuthor(s string) Author {
	s = strings.TrimSpace(s)
	if s == "" {
		return Author{}
	}
	if i := strings.Index(s, ","); i >= 0 {
		return Author{Last: strings.TrimSpace(s[:i]), First: strings.TrimSpace(s[i+1:])}
	}
	parts := strings.Fields(s)
	if len(parts) == 1 {
		return Author{Last: parts[0]}
	}
	return Author{Last: parts[len(parts)-1], First: strings.Join(parts[:len(parts)-1], " ")}
}

func authorLastName(s string) string {
	return parseAuthor(s).Last
}

func initials(first string) string {
	var b strings.Builder
	for _, w := range strings.Fields(strings.ReplaceAll(first, ".", " ")) {
		r := []rune(w)
		if len(r) > 0 {
			b.WriteRune(unicode.ToUpper(r[0]))
			b.WriteString(". ")
		}
	}
	return strings.TrimSpace(b.String())
}

func gbtAuthors(authors []string, year int, zh bool) string {
	if len(authors) == 0 {
		return ""
	}
	max := len(authors)
	if max > 3 {
		max = 3
	}
	out := make([]string, 0, max+1)
	for i := 0; i < max; i++ {
		a := parseAuthor(authors[i])
		if isCJK(a.Last) {
			out = append(out, strings.TrimSpace(a.Last+a.First))
		} else {
			last := strings.ToUpper(a.Last)
			if ini := initials(a.First); ini != "" {
				out = append(out, last+" "+ini)
			} else {
				out = append(out, last)
			}
		}
	}
	if len(authors) > 3 {
		if zh {
			out = append(out, "等")
		} else {
			out = append(out, "et al")
		}
	}
	return strings.Join(out, ", ")
}

func apaAuthors(authors []string) string {
	if len(authors) == 0 {
		return ""
	}
	format := func(s string) string {
		a := parseAuthor(s)
		if isCJK(a.Last) {
			return strings.TrimSpace(a.Last + a.First)
		}
		if ini := initials(a.First); ini != "" {
			return a.Last + ", " + ini
		}
		return a.Last
	}
	if len(authors) == 1 {
		return format(authors[0])
	}
	if len(authors) <= 20 {
		parts := make([]string, len(authors))
		for i, a := range authors {
			parts[i] = format(a)
		}
		return strings.Join(parts[:len(parts)-1], ", ") + ", & " + parts[len(parts)-1]
	}
	parts := make([]string, 19)
	for i := 0; i < 19; i++ {
		parts[i] = format(authors[i])
	}
	return strings.Join(parts, ", ") + ", ... " + format(authors[len(authors)-1])
}

func ieeeAuthors(authors []string) string {
	if len(authors) == 0 {
		return ""
	}
	max := len(authors)
	if max > 6 {
		max = 6
	}
	out := make([]string, 0, max+1)
	for i := 0; i < max; i++ {
		a := parseAuthor(authors[i])
		if isCJK(a.Last) {
			out = append(out, strings.TrimSpace(a.Last+a.First))
		} else {
			ini := initials(a.First)
			if ini != "" {
				out = append(out, strings.ReplaceAll(ini, ". ", ". ")+" "+a.Last)
			} else {
				out = append(out, a.Last)
			}
		}
	}
	if len(authors) > 6 {
		out = append(out, "et al.")
	}
	if len(out) == 1 {
		return out[0]
	}
	return strings.Join(out[:len(out)-1], ", ") + " and " + out[len(out)-1]
}

func vancouverAuthors(authors []string) string {
	if len(authors) == 0 {
		return ""
	}
	max := len(authors)
	if max > 6 {
		max = 6
	}
	out := make([]string, 0, max+1)
	for i := 0; i < max; i++ {
		a := parseAuthor(authors[i])
		if isCJK(a.Last) {
			out = append(out, strings.TrimSpace(a.Last+a.First))
		} else {
			ini := strings.ReplaceAll(initials(a.First), ". ", "")
			out = append(out, a.Last+" "+ini)
		}
	}
	if len(authors) > 6 {
		out = append(out, "et al")
	}
	return strings.Join(out, ", ")
}

func mlaAuthors(authors []string) string {
	if len(authors) == 0 {
		return ""
	}
	if len(authors) == 1 {
		return authors[0]
	}
	if len(authors) == 2 {
		return authors[0] + ", and " + authors[1]
	}
	return authors[0] + ", et al"
}

func bibAuthors(authors []string) string {
	out := make([]string, 0, len(authors))
	for _, a := range authors {
		pa := parseAuthor(a)
		if pa.Name != "" {
			out = append(out, "{"+pa.Name+"}")
		} else if pa.First != "" {
			out = append(out, pa.Last+", "+pa.First)
		} else {
			out = append(out, pa.Last)
		}
	}
	return strings.Join(out, " and ")
}

func shortAuthor(it Item) string {
	if len(it.Authors) == 0 {
		return ""
	}
	last := authorLastName(it.Authors[0])
	if len(it.Authors) > 1 {
		return last + " et al."
	}
	return last
}

func shortTitle(it Item) string {
	title := strings.TrimSpace(it.Title)
	if title == "" {
		return "Untitled"
	}
	r := []rune(title)
	if len(r) > 24 {
		return string(r[:24]) + "…"
	}
	return title
}

func isChinese(lang string, authors []string) bool {
	l := strings.ToLower(lang)
	if strings.Contains(l, "zh") || strings.Contains(l, "chinese") || strings.Contains(lang, "中文") {
		return true
	}
	for _, a := range authors {
		if isCJK(a) {
			return true
		}
	}
	return false
}

func isCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
}

// SortedKeys 返回稳定的字符序，便于测试与去重。
func SortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// NormalizeTitle 清理标题中的换行与多余空白。
func NormalizeTitle(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

var multiSpace = regexp.MustCompile(`\s+`)

// CleanText 清理 OCR/PDF 文本中的连续空白。
func CleanText(s string) string {
	return multiSpace.ReplaceAllString(strings.TrimSpace(s), " ")
}
