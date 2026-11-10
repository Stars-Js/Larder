// Package server 封装 HTTP 服务的生命周期，包括启动监听与优雅退出。
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"larder/internal/config"
)

// Server 包装标准库的 http.Server。
type Server struct {
	http            *http.Server
	logger          *slog.Logger
	shutdownTimeout time.Duration
}

// New 使用给定配置与已装配好的路由创建服务器。
func New(cfg config.Config, handler http.Handler, logger *slog.Logger) *Server {
	s := cfg.Server
	return &Server{
		http: &http.Server{
			Addr:              s.Addr,
			Handler:           handler,
			ReadHeaderTimeout: s.ReadTimeout.Std(),
			ReadTimeout:       s.ReadTimeout.Std(),
			WriteTimeout:      s.WriteTimeout.Std(),
			IdleTimeout:       s.IdleTimeout.Std(),
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		},
		logger:          logger,
		shutdownTimeout: s.ShutdownTimeout.Std(),
	}
}

// Run 阻塞运行直到 ctx 被取消，然后优雅关闭。
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve 在给定的监听器上提供服务，直到 ctx 被取消，然后优雅关闭。
// 这让调用方（如桌面窗口）可以自行创建监听器并拿到实际端口。
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	errCh := make(chan error, 1)

	go func() {
		s.logger.Info("HTTP 服务启动", "addr", ln.Addr().String())
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	if s.shutdownTimeout <= 0 {
		s.shutdownTimeout = 10 * time.Second
	}
	s.logger.Info("收到退出信号，开始优雅关闭")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()

	if err := s.http.Shutdown(shutdownCtx); err != nil {
		return err
	}
	s.logger.Info("服务已关闭")
	return nil
}
