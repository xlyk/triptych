package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type Runner struct {
	Config Config
	Client Client
	Logger *slog.Logger
}

func (r Runner) Run(ctx context.Context) error {
	if r.Client == nil {
		return fmt.Errorf("client is required")
	}

	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}

	registration := HostRegistration{
		HostID:           r.Config.HostID,
		Hostname:         r.Config.Hostname,
		Capabilities:     r.Config.Capabilities,
		AllowedRepoRoots: r.Config.AllowedRepoRoots,
		Labels:           r.Config.Labels,
	}

	if err := r.Client.RegisterHost(ctx, registration); err != nil {
		return fmt.Errorf("register host: %w", err)
	}
	logger.Info("registered host", "host_id", r.Config.HostID, "hostname", r.Config.Hostname)

	ticker := time.NewTicker(r.Config.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.Client.Heartbeat(ctx, r.Config.HostID); err != nil {
				return fmt.Errorf("heartbeat: %w", err)
			}
			logger.Debug("heartbeat sent", "host_id", r.Config.HostID)
		}
	}
}
