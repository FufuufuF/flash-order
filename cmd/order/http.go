package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/FufuufuF/flash-order/internal/httpapi"
	"github.com/FufuufuF/flash-order/internal/order"
)

type HTTPConfig struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// runHTTP 负责组装对象和管理 HTTP 生命周期，不在这里编写下单业务。
func runHTTP(signalContext context.Context, db *sql.DB, cfg HTTPConfig) error {
	orderStore := order.NewStore(db)
	orderService := order.NewService(orderStore)
	orderHandler := httpapi.NewOrderHandler(orderService)

	router := httpapi.NewRouter(orderHandler)

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           router,
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}

	return runHTTPServer(signalContext, server)
}

// runHTTPServer 等待 HTTP 退出；返回后，run 才会关闭数据库连接池。
func runHTTPServer(signalContext context.Context, server *http.Server) error {
	// 1. 请求使用独立的 context。退出信号到达时，先给在途请求完成的机会。
	serviceContext, cancelService := context.WithCancel(context.Background())
	defer cancelService()
	server.BaseContext = func(_ net.Listener) context.Context {
		return serviceContext
	}

	// 2. 新 goroutine 负责监听；通道传回唯一一次监听结果。
	// 容量为 1：即使当前正在等待 Shutdown，监听任务也能发出结果并结束。
	listenErr := make(chan error, 1)
	go func() {
		listenErr <- server.ListenAndServe()
	}()

	// 3. 同时等待监听结束或退出信号。
	select {
	case err := <-listenErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		// 意外监听失败也要清理连接和请求，不能只留下后台任务。
		cancelService()
		return errors.Join(fmt.Errorf("HTTP 监听失败: %w", err), server.Close())
	case <-signalContext.Done():
		slog.Info("shutdown_requested")
	}

	// 4. 信号 context 已取消，所以关闭过程使用独立的 5 秒预算。
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()

	// Shutdown 会先停止监听，再等待在途请求；必须等待它返回。
	var runErr error
	if err := server.Shutdown(shutdownContext); err != nil {
		runErr = fmt.Errorf("HTTP 优雅关闭失败: %w", err)
		slog.Warn("http_shutdown_forced", "error", err)
		// 取消在途 SQL 等操作，再强制关闭连接。
		cancelService()
		if err := server.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("HTTP 强制关闭失败: %w", err))
		}
	}

	// 5. 取回监听结果。ErrServerClosed 是主动关闭的正常结果。
	if err := <-listenErr; !errors.Is(err, http.ErrServerClosed) {
		runErr = errors.Join(runErr, fmt.Errorf("HTTP 监听失败: %w", err))
	}
	if runErr != nil {
		return runErr
	}

	slog.Info("http_stopped")
	return nil
}
