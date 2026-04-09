package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xlyk/triptych/internal/daemon"
	triptychtmux "github.com/xlyk/triptych/internal/tmux"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, err := daemon.LoadConfigFromEnv(os.Getenv, os.Hostname)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "agentd: %v\n", err)
		os.Exit(1)
	}

	runner := daemon.Runner{
		Config: cfg,
		Client: daemon.NewHTTPClient(cfg.ServerURL, &http.Client{
			Timeout: 10 * time.Second,
		}),
		Logger: logger,
		Launch: triptychtmux.NewLauncher(),
	}

	logger.Info("starting agentd", "host_id", cfg.HostID, "server_url", cfg.ServerURL, "heartbeat_interval", cfg.HeartbeatInterval)

	if err := runner.Run(ctx); err != nil {
		logger.Error("agentd exited with error", "err", err)
		os.Exit(1)
	}

	logger.Info("agentd stopped", "host_id", cfg.HostID)
}
