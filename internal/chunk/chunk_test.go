package chunk_test

import (
	"strings"
	"testing"

	"larder/internal/chunk"
)

const sampleMD = `# 安装指南

这是引言段落，介绍整篇文档的目的。

## Windows

在 Windows 上，请先安装 Go 与 Git，然后运行安装脚本。
安装完成后重启终端。

### 常见问题

如果遇到权限错误，请以管理员身份运行。

## macOS

使用 Homebrew 安装：brew install larder。
`

func TestSplitHeadings(t *testing.T) {
	chunks := chunk.Split(sampleMD, chunk.Options{MaxRunes: 1200, Overlap: 100, MinRunes: 5})
	if len(chunks) == 0 {
		t.Fatal("应产生至少一个切块")
	}

	// 每个块的标题路径应正确。
	want := map[string]bool{
		"安装指南":                  true,
		"安装指南 > Windows":        true,
		"安装指南 > Windows > 常见问题": true,
		"安装指南 > macOS":          true,
	}
	got := map[string]bool{}
	for _, c := range chunks {
		got[c.Heading] = true
	}
	for h := range want {
		if !got[h] {
			t.Errorf("缺少标题路径 %q，实际有 %v", h, got)
		}
	}

	// ordinal 应连续从 0 开始。
	for i, c := range chunks {
		if c.Ordinal != i {
			t.Errorf("块 %d 的 ordinal 应为 %d，实为 %d", i, i, c.Ordinal)
		}
	}
}

func TestSplitMaxRunes(t *testing.T) {
	// 构造一个超长段落。
	long := strings.Repeat("甲乙丙丁戊己庚辛壬癸", 200) // 2000 runes
	md := "# 标题\n\n" + long

	chunks := chunk.Split(md, chunk.Options{MaxRunes: 300, Overlap: 30})
	if len(chunks) < 2 {
		t.Fatalf("超长段落应被切成多块，实际 %d", len(chunks))
	}
	for _, c := range chunks {
		// 允许轻微超出（断句点），但不应显著超过上限。
		if n := len([]rune(c.Text)); n > 400 {
			t.Errorf("块长度 %d 超过上限过多", n)
		}
	}
}

func TestSplitCodeFencePreserved(t *testing.T) {
	md := "# 代码\n\n介绍\n\n```go\nfunc main() {\n    println(\"hi\")\n}\n```\n\n结尾"
	chunks := chunk.Split(md, chunk.Options{MaxRunes: 1200, MinRunes: 5})
	var found bool
	for _, c := range chunks {
		if strings.Contains(c.Text, "```go") && strings.Contains(c.Text, "func main") {
			found = true
		}
	}
	if !found {
		t.Error("代码围栏应被整体保留在某个块中")
	}
}
