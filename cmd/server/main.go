package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"mining_shield/internal/server"
)

func main() {
	cfgPath := flag.String("config", "server.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := server.LoadConfig(*cfgPath)
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := server.New(cfg).Run(ctx); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}
