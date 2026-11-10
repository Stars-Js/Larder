// Command larder 启动本地知识库服务：REST + MCP 一体化进程。
//
// Windows 下用 -H windowsgui 构建（见 scripts/build-windows.ps1）时不显示控制台；
// 启动参数与运行日志会写到 exe 同级的 larder.toml / data/larder.log。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"larder/internal/activity"
	"larder/internal/api"
	"larder/internal/app"
	"larder/internal/config"
	"larder/internal/desktop"
	"larder/internal/kb"
	"larder/internal/route"
	"larder/internal/server"
	"larder/internal/services"
)

func main() {
	// DPI 感知必须在任何窗口创建前设置，否则界面会被系统拉伸而发虚。
	desktop.Init()
	if err := run(); err != nil {
		// 无控制台时错误不能只写 stderr，用原生提示框告知用户。
		desktop.MessageBox("Larder 启动失败", err.Error())
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		cfgPath  = flag.String("config", "", "配置文件路径（TOML）")
		addr     = flag.String("addr", "", "监听地址，覆盖配置，如 127.0.0.1:8765")
		logLevel = flag.String("log", "", "日志级别：debug/info/warn/error")
		showCfg  = flag.Bool("print-config", false, "打印最终生效的配置后退出")
		noOpen   = flag.Bool("no-open", false, "不打开原生窗口（仅后台运行 HTTP 服务）")
		webMode  = flag.Bool("web", false, "以纯 Web 模式运行，并用系统浏览器打开（默认用内置原生窗口）")
	)
	flag.Parse()

	cfg, resolved, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *addr != "" {
		cfg.Server.Addr = *addr
	}
	if *logLevel != "" {
		cfg.Log.Level = *logLevel
	}

	// 首次运行在“本级目录”生成参数文件（larder.toml），方便查看和修改。
	savePath := resolved
	if savePath == "" {
		savePath = config.DefaultConfigPath()
		if _, statErr := os.Stat(savePath); os.IsNotExist(statErr) {
			if err := config.Save(savePath, cfg); err != nil {
				fmt.Fprintln(os.Stderr, "生成参数文件失败:", err)
			}
		}
	}
	if abs, err := filepath.Abs(savePath); err == nil {
		savePath = abs
	}
	// 数据目录固定到 exe 同级 data/（可用 LARDER_DATA_DIR 覆盖）。
	if err := os.MkdirAll(cfg.Storage.Dir, 0o755); err != nil {
		return fmt.Errorf("创建数据目录 %s 失败: %w", cfg.Storage.Dir, err)
	}

	logger, logFile := newLogger(cfg.Log.Level, cfg.Log.Format, cfg.Storage.Dir)
	if logFile != nil {
		defer logFile.Close()
	}

	if resolved != "" {
		logger.Info("已加载配置文件", "path", resolved)
	} else {
		logger.Info("未找到配置文件，已使用默认值并生成参数文件", "path", savePath)
	}

	if *showCfg {
		fmt.Printf("数据目录: %s\n监听地址: %s\nMCP 路径: %s（启用: %v）\n解析器: %s\n向量模型: %s\n重排: %v\n配置文件: %s\n",
			cfg.Storage.Dir, cfg.Server.Addr, cfg.Server.MCPPath, cfg.Server.MCPEnabled,
			cfg.Parse.Provider, cfg.Embed.Endpoint.Model, cfg.Rerank.Enabled, savePath)
		return nil
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("配置校验失败: %w", err)
	}
	// 缺密钥不阻塞启动，仅告警，避免一启动就退出（“闪退”）。
	for _, name := range cfg.MissingCredentials() {
		logger.Warn("外部服务未配置 api_key，相关功能在调用时可能失败", "service", name)
	}

	// 共享的外部服务容器（解析/向量/重排），设置变更可热更新。
	svc, err := services.New(cfg, logger)
	if err != nil {
		return err
	}

	// 多知识库管理器。
	manager, err := kb.NewManager(cfg, svc, logger)
	if err != nil {
		return err
	}
	defer manager.Close()

	// 配置持久化到启动时确定的参数文件（exe 同级 larder.toml）。
	saveConfig := func(c config.Config) error { return config.Save(savePath, c) }

	// 先监听端口，拿到真实地址后再装配 REST/MCP，这样“一键注入 Agent”
	// 使用的 MCP URL 在 addr = 127.0.0.1:0 时也是正确的。
	ln, err := net.Listen("tcp", cfg.Server.Addr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", cfg.Server.Addr, err)
	}
	actualAddr := ln.Addr().String()
	uiURL := "http://" + actualAddr + "/"
	mcpURL := "http://" + actualAddr + cfg.Server.MCPPath
	// 旧版注入没有带 ?client=；启动时补齐，平台级引用格式无需手动重注入。
	api.MigrateInjectedClients(mcpURL, cfg.Storage.Dir)

	act := activity.New(filepath.Join(cfg.Storage.Dir, "activity.log"))
	handler := route.Handler(route.Deps{
		Manager:  manager,
		MCPPath:  cfg.Server.MCPPath,
		Logger:   logger,
		Activity: act,
		API: api.Deps{
			Manager:    manager,
			Services:   svc,
			SaveConfig: saveConfig,
			ConfigPath: savePath,
			MCPURL:     mcpURL,
			Version:    app.Version,
			Logger:     logger,
			Activity:   act,
		},
	})
	srv := server.New(cfg, handler, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("Larder 就绪",
		"addr", actualAddr,
		"mcp", cfg.Server.MCPPath,
		"data_dir", cfg.Storage.Dir,
		"parser", cfg.Parse.Provider,
	)

	// 在后台 goroutine 中跑 HTTP 服务。
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	if *noOpen {
		// 仅后台运行：等待服务结束或信号。
		select {
		case err := <-serveErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		case <-ctx.Done():
		}
		return nil
	}

	// 默认：在 exe 自带的原生窗口内打开界面。
	if *webMode {
		openBrowser(uiURL)
		select {
		case err := <-serveErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		case <-ctx.Done():
		}
		return nil
	}

	// 原生窗口模式：窗口关闭后停止服务。
	winErr := desktop.Run("Larder · 本地知识库", uiURL, 1200, 820)
	if errors.Is(winErr, desktop.ErrNoWebView) {
		// 无法创建原生窗口（如缺少 WebView2 运行库）时回退到系统浏览器。
		logger.Warn("无法创建原生窗口，回退到系统浏览器", "hint", "安装 WebView2 运行库后可内嵌窗口")
		openBrowser(uiURL)
		select {
		case err := <-serveErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		case <-ctx.Done():
		}
		return nil
	}

	// 窗口已关闭：触发优雅退出。
	stop()
	if err := <-serveErr; err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// openBrowser 尝试用系统默认浏览器打开 URL。
func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "windows":
		cmd, args = "cmd", []string{"/c", "start", "", url}
	case "darwin":
		cmd, args = "open", []string{url}
	default:
		cmd, args = "xdg-open", []string{url}
	}
	_ = exec.Command(cmd, args...).Start()
}

// ignoreErrWriter 忽略底层 writer 的错误，保证多路日志不会互相影响。
type ignoreErrWriter struct{ w io.Writer }

func (w ignoreErrWriter) Write(p []byte) (int, error) {
	_, _ = w.w.Write(p)
	return len(p), nil
}

// newLogger 依据配置构造 slog.Logger。同时写标准输出与 data/larder.log，
// 这样用 -H windowsgui 构建成无控制台 exe 后仍能排查问题。
func newLogger(level, format, dataDir string) (*slog.Logger, *os.File) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}

	var writers []io.Writer
	// windowsgui 模式下 stdout 可能不可写；包一层忽略错误的 writer，
	// 避免 io.MultiWriter 因第一个 writer 失败而跳过日志文件。
	writers = append(writers, ignoreErrWriter{os.Stdout})
	var logFile *os.File
	if dataDir != "" {
		if err := os.MkdirAll(dataDir, 0o755); err == nil {
			p := filepath.Join(dataDir, "larder.log")
			if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
				logFile = f
				_, _ = fmt.Fprintf(f, "\n===== %s 启动 =====\n", time.Now().Format(time.RFC3339))
				writers = append(writers, f)
			}
		}
	}
	w := io.MultiWriter(writers...)

	var h slog.Handler
	if strings.EqualFold(format, "json") {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h), logFile
}
