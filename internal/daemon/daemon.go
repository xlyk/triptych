package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/xlyk/triptych/internal/domain"
	triptychtmux "github.com/xlyk/triptych/internal/tmux"
)

type Runner struct {
	Config Config
	Client Client
	Logger *slog.Logger
	Launch Launcher
}

type Launcher interface {
	Launch(context.Context, triptychtmux.LaunchSpec) (triptychtmux.LaunchResult, error)
}

func (r Runner) Run(ctx context.Context) error {
	if r.Client == nil {
		return fmt.Errorf("client is required")
	}

	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}
	launcher := r.Launch
	if launcher == nil {
		defaultLauncher := triptychtmux.NewLauncher()
		launcher = defaultLauncher
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
			if err := r.pollAndLaunch(ctx, launcher, logger); err != nil {
				return err
			}
		}
	}
}

func (r Runner) pollAndLaunch(ctx context.Context, launcher Launcher, logger *slog.Logger) error {
	work, err := r.Client.GetWork(ctx, r.Config.HostID)
	if err != nil {
		return fmt.Errorf("get work: %w", err)
	}
	for _, job := range work.LaunchableJobs {
		result, err := launcher.Launch(ctx, triptychtmux.LaunchSpec{
			RunID:   job.RunID,
			JobID:   job.JobID,
			Workdir: job.Workdir,
			Goal:    job.Goal,
		})
		if err != nil {
			return fmt.Errorf("launch run %s: %w", job.RunID, err)
		}

		startedAt := time.Now().UTC()
		if err := r.Client.UpdateRunState(ctx, job.RunID, RunStateUpdate{
			Status:          domain.RunStatusActive,
			TmuxSessionName: stringPtr(result.SessionName),
			TmuxWindowName:  stringPtr(result.WindowName),
			StartedAt:       &startedAt,
		}); err != nil {
			return fmt.Errorf("update run state %s: %w", job.RunID, err)
		}

		logger.Info("launched run",
			"job_id", job.JobID,
			"run_id", job.RunID,
			"tmux_session", result.SessionName,
			"tmux_window", result.WindowName,
			"created", result.Created,
		)
	}
	return nil
}

func stringPtr(value string) *string {
	return &value
}
