package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"mining_shield/internal/agent"
)

func main() {
	cfgPath := flag.String("config", "agent.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := agent.LoadConfig(*cfgPath)
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := agent.New(cfg).Run(ctx); err != nil {
		slog.Error("agent exited", "err", err)
		os.Exit(1)
	}
}
