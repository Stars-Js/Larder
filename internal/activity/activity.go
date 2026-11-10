// Package activity 记录 Larder 的关键交互（检索、入库、设置、MCP 调用），
// 供界面“日志”页查看。事件同时写入 data/activity.log，重启后仍可回看。
package activity

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxEvents = 500

// Snippet 是检索命中的原文片段，随日志保存，便于点开查看。
type Snippet struct {
	KBID    string  `json:"kb_id,omitempty"`
	DocID   string  `json:"doc_id,omitempty"`
	Doc     string  `json:"doc,omitempty"`
	Source  string  `json:"source,omitempty"`
	Heading string  `json:"heading,omitempty"`
	Text    string  `json:"text,omitempty"`
	Score   float64 `json:"score,omitempty"`
}

// Event 是一条交互记录。
type Event struct {
	ID         string    `json:"id,omitempty"`
	Time       time.Time `json:"time"`
	Kind       string    `json:"kind"`
	KB         string    `json:"kb,omitempty"`
	Title      string    `json:"title,omitempty"`
	Status     string    `json:"status,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Snippets   []Snippet `json:"snippets,omitempty"`
}

// Logger 是带内存环形缓冲的 JSONL 事件记录器。
type Logger struct {
	mu     sync.RWMutex
	path   string
	events []Event
}

// New 创建记录器，并从已有 activity.log 恢复最近事件。
func New(path string) *Logger {
	l := &Logger{path: path}
	if path == "" {
		return l
	}
	f, err := os.Open(path)
	if err != nil {
		return l
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Kind != "" {
			l.events = append(l.events, e)
		}
	}
	if len(l.events) > maxEvents {
		l.events = l.events[len(l.events)-maxEvents:]
	}
	return l
}

// CleanText 去掉 PDF 抽取残留的控制字符，保留换行/制表符。
func CleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 0x20 {
			return r
		}
		return ' '
	}, s)
}

// Add 追加一条事件并返回事件 ID；写文件失败不影响主流程。
func (l *Logger) Add(e Event) string {
	if l == nil {
		return ""
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if e.ID == "" {
		e.ID = strconv.FormatInt(e.Time.UnixNano(), 36)
	}
	if e.Status == "" {
		e.Status = "ok"
	}
	l.mu.Lock()
	l.events = append(l.events, e)
	if len(l.events) > maxEvents {
		l.events = l.events[len(l.events)-maxEvents:]
	}
	l.mu.Unlock()

	if l.path == "" {
		return e.ID
	}
	b, err := json.Marshal(e)
	if err != nil {
		return e.ID
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return e.ID
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return e.ID
	}
	_, _ = f.Write(append(b, byte(10)))
	_ = f.Close()
	return e.ID
}

// Recent 返回最近 limit 条事件，新的在前。
func (l *Logger) Recent(limit int) []Event {
	if l == nil {
		return []Event{}
	}
	if limit <= 0 || limit > maxEvents {
		limit = 200
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	n := len(l.events)
	if n == 0 {
		return []Event{}
	}
	out := make([]Event, 0, min(limit, n))
	for i := n - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, l.events[i])
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
