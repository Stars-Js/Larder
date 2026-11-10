//go:build !windows

package desktop

import (
	"fmt"
	"os"
)

// Init 在非 Windows 平台无需处理 DPI。
func Init() {}

// Scale 在非 Windows 平台保持逻辑尺寸。
func Scale(n int) int { return n }

// MessageBox 在非 Windows 平台输出到标准错误。
func MessageBox(title, text string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", title, text)
}
