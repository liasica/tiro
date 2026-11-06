// tiro 把飞书妙记自动整理成会议纪要
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/liasica/tiro/internal/feishu"
	"github.com/liasica/tiro/internal/server"
	"github.com/liasica/tiro/internal/store"
	"github.com/liasica/tiro/internal/web"
	"github.com/liasica/tiro/internal/worker"
)

// version 在构建时通过 -ldflags 注入
var version = "dev"

func main() {
	address := flag.String("addr", envOr("TIRO_ADDR", ":8476"), "后台的监听地址")
	dataDir := flag.String("data", envOr("TIRO_DATA_DIR", "data"), "数据目录，存放 SQLite 数据库")

	flag.Parse()

	if err := run(*address, *dataDir); err != nil {
		slog.Error("tiro 退出", "error", err)
		os.Exit(1)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}

	return fallback
}

func run(address, dataDir string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	database, err := store.Open(ctx, dataDir)
	if err != nil {
		return err
	}

	defer func() { _ = database.Close() }()

	client := feishu.New()
	background := worker.New(database, client)

	var handler *server.Server

	handler, err = server.New(ctx, server.Options{
		Store:   database,
		Feishu:  client,
		Worker:  background,
		Assets:  web.Assets(),
		Version: version,
	})
	if err != nil {
		return err
	}

	var listener net.Listener

	listener, err = net.Listen("tcp", address)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Handler:           handler.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	announce(ctx, database, listener.Addr())

	workerDone := make(chan struct{})

	go func() {
		defer close(workerDone)

		background.Run(ctx)
	}()

	serveErr := make(chan error, 1)

	go func() { serveErr <- httpServer.Serve(listener) }()

	select {
	case err = <-serveErr:
		stop()
	case <-ctx.Done():
		err = nil
	}

	// 给进行中的请求与正在生成的纪要一点收尾时间
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = httpServer.Shutdown(shutdownCtx)

	select {
	case <-workerDone:
	case <-shutdownCtx.Done():
	}

	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return err
}

// announce 在启动日志里给出后台地址，尚未初始化时提示去后台扫码
func announce(ctx context.Context, database *store.Store, address net.Addr) {
	port := "8476"

	if tcp, ok := address.(*net.TCPAddr); ok {
		port = strconv.Itoa(tcp.Port)
	}

	adminURL := "http://localhost:" + port

	config, err := database.Config(ctx)
	if err != nil {
		slog.Warn("读取配置失败", "error", err)

		return
	}

	if config.AppConfigured() {
		slog.Info("tiro 已启动", "version", version, "admin", adminURL)

		return
	}

	slog.Info("tiro 已启动，尚未初始化，打开后台用飞书扫码完成配置", "version", version, "admin", adminURL)
}
