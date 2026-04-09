package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/xlyk/triptych/internal/domain"
	triptychtmux "github.com/xlyk/triptych/internal/tmux"
)

type Runner struct {
	Config   Config
	Client   Client
	Logger   *slog.Logger
	Launch   Launcher
	Control  TmuxController
	Capture  PaneCapturer
	Receipts CommandReceiptStore
}

type PaneCapturer interface {
	CapturePane(ctx context.Context, sessionName, windowName string) (output string, lineCount int, err error)
}

type Launcher interface {
	Launch(context.Context, triptychtmux.LaunchSpec) (triptychtmux.LaunchResult, error)
}

type TmuxController interface {
	SendKeys(ctx context.Context, sessionName, windowName, text string) error
	SendInterrupt(ctx context.Context, sessionName, windowName string) error
	HasSession(ctx context.Context, sessionName string) (bool, error)
	KillSession(ctx context.Context, sessionName string) (bool, error)
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
		defaultLauncher.Mode = r.Config.LaunchMode
		defaultLauncher.Config = r.Config.Launch
		launcher = defaultLauncher
	}
	controller := r.Control
	if controller == nil {
		defaultController := triptychtmux.NewController()
		controller = &defaultController
	}
	capturer := r.Capture
	if capturer == nil {
		defaultCapturer := triptychtmux.NewCapturer()
		capturer = defaultCapturer
	}
	receipts := r.Receipts
	if receipts == nil {
		receipts = NewLocalCommandReceiptStore(filepath.Join(r.Config.StateDir, "agentd", r.Config.HostID.String(), "command-receipts"))
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
			if err := r.pollAndExecute(ctx, launcher, controller, capturer, receipts, logger); err != nil {
				return err
			}
		}
	}
}

func (r Runner) pollAndExecute(ctx context.Context, launcher Launcher, controller TmuxController, capturer PaneCapturer, receipts CommandReceiptStore, logger *slog.Logger) error {
	work, err := r.Client.GetWork(ctx, r.Config.HostID)
	if err != nil {
		return fmt.Errorf("get work: %w", err)
	}

	// Launch new runs.
	for _, job := range work.LaunchableJobs {
		result, err := launcher.Launch(ctx, triptychtmux.LaunchSpec{
			RunID:   job.RunID,
			JobID:   job.JobID,
			Agent:   job.Agent,
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

	// Build a lookup from run_id -> active run for resolving tmux targets.
	activeByRun := make(map[domain.RunID]ActiveRun, len(work.ActiveRuns))
	for _, ar := range work.ActiveRuns {
		activeByRun[ar.RunID] = ar
	}

	// Reconcile active runs whose tmux sessions have disappeared.
	for _, ar := range work.ActiveRuns {
		if err := r.reconcileRun(ctx, ar, controller, logger); err != nil {
			logger.Error("reconcile run failed", "run_id", ar.RunID, "error", err)
		}
	}

	// Capture output snapshots for live runs with tmux sessions.
	for _, ar := range work.ActiveRuns {
		r.captureSnapshot(ctx, ar, capturer, logger)
	}

	// Execute pending commands.
	for _, cmd := range work.PendingCommands {
		if err := r.executeCommand(ctx, cmd, activeByRun, controller, receipts, logger); err != nil {
			logger.Error("command execution failed",
				"command_id", cmd.CommandID,
				"command_type", cmd.CommandType,
				"error", err,
			)
		}
	}

	return nil
}

func (r Runner) executeCommand(ctx context.Context, cmd PendingCommand, activeByRun map[domain.RunID]ActiveRun, controller TmuxController, receipts CommandReceiptStore, logger *slog.Logger) error {
	applied, err := receipts.HasApplied(cmd.CommandID)
	if err != nil {
		return err
	}
	if applied {
		return r.finalizeAppliedCommand(ctx, cmd, receipts)
	}

	// Ack first so the server can keep surfacing the command while the daemon is still finishing it.
	if err := r.Client.AckCommand(ctx, cmd.CommandID); err != nil {
		return fmt.Errorf("ack command %s: %w", cmd.CommandID, err)
	}

	ar, ok := activeByRun[cmd.RunID]
	if !ok {
		logger.Warn("command references unknown active run, observing without action",
			"command_id", cmd.CommandID,
			"run_id", cmd.RunID,
		)
		return r.observeAndClearCommand(ctx, cmd.CommandID, receipts)
	}

	sessionName := ar.SessionName()
	windowName := ar.WindowName()
	if windowName == "" {
		windowName = triptychtmux.DefaultWindowName
	}

	var execErr error
	switch cmd.CommandType {
	case domain.CommandTypeSend:
		text := ""
		if cmd.Payload != nil {
			text = cmd.Payload.Text
		}
		execErr = controller.SendKeys(ctx, sessionName, windowName, text)
		if execErr == nil {
			logger.Info("sent keys", "command_id", cmd.CommandID, "run_id", cmd.RunID)
		}

	case domain.CommandTypeInterrupt:
		execErr = controller.SendInterrupt(ctx, sessionName, windowName)
		if execErr == nil {
			logger.Info("sent interrupt", "command_id", cmd.CommandID, "run_id", cmd.RunID)
		}

	case domain.CommandTypeStop:
		execErr = r.executeStop(ctx, cmd, ar, controller, logger)

	default:
		logger.Warn("unknown command type", "command_type", cmd.CommandType, "command_id", cmd.CommandID)
	}

	if execErr != nil {
		logger.Error("command action failed", "command_id", cmd.CommandID, "error", execErr)
		return nil
	}
	if err := receipts.MarkApplied(cmd); err != nil {
		return fmt.Errorf("mark command %s applied: %w", cmd.CommandID, err)
	}
	return r.observeAndClearCommand(ctx, cmd.CommandID, receipts)
}

func (r Runner) finalizeAppliedCommand(ctx context.Context, cmd PendingCommand, receipts CommandReceiptStore) error {
	if err := r.Client.AckCommand(ctx, cmd.CommandID); err != nil {
		return fmt.Errorf("re-ack applied command %s: %w", cmd.CommandID, err)
	}
	return r.observeAndClearCommand(ctx, cmd.CommandID, receipts)
}

func (r Runner) observeAndClearCommand(ctx context.Context, commandID domain.CommandID, receipts CommandReceiptStore) error {
	if err := r.Client.ObserveCommand(ctx, commandID); err != nil {
		return fmt.Errorf("observe command %s: %w", commandID, err)
	}
	if err := receipts.Clear(commandID); err != nil {
		return fmt.Errorf("clear command receipt %s: %w", commandID, err)
	}
	return nil
}

func (r Runner) executeStop(ctx context.Context, cmd PendingCommand, ar ActiveRun, controller TmuxController, logger *slog.Logger) error {
	// 1. Transition run to stopping.
	if err := r.Client.UpdateRunState(ctx, ar.RunID, RunStateUpdate{
		Status: domain.RunStatusStopping,
	}); err != nil {
		return fmt.Errorf("set run %s stopping: %w", ar.RunID, err)
	}

	// 2. Kill the tmux session.
	killed, err := controller.KillSession(ctx, ar.SessionName())
	if err != nil {
		return fmt.Errorf("kill session for run %s: %w", ar.RunID, err)
	}
	logger.Info("stopped run", "run_id", ar.RunID, "session_killed", killed)

	// 3. Mark the run as exited with cancelled disposition.
	finishedAt := time.Now().UTC()
	disposition := domain.TerminalDispositionCancelled
	if err := r.Client.UpdateRunState(ctx, ar.RunID, RunStateUpdate{
		Status:              domain.RunStatusExited,
		FinishedAt:          &finishedAt,
		TerminalDisposition: &disposition,
	}); err != nil {
		return fmt.Errorf("set run %s exited: %w", ar.RunID, err)
	}

	return nil
}

// reconcileRun checks whether the tmux session for an active run still exists.
// If the session is gone, it repairs the run state on the server:
//   - stop_requested or already stopping → exited + cancelled
//   - otherwise → crashed + failed
func (r Runner) reconcileRun(ctx context.Context, ar ActiveRun, controller TmuxController, logger *slog.Logger) error {
	sessionName := ar.SessionName()
	if sessionName == "" {
		return nil
	}

	// Only reconcile runs the server considers live.
	switch ar.RunStatus {
	case domain.RunStatusActive, domain.RunStatusWaiting, domain.RunStatusStopping:
		// eligible for reconciliation
	default:
		return nil
	}

	exists, err := controller.HasSession(ctx, sessionName)
	if err != nil {
		return fmt.Errorf("check session %q: %w", sessionName, err)
	}
	if exists {
		return nil
	}

	// Session is gone — decide terminal state.
	var (
		status      domain.RunStatus
		disposition domain.TerminalDisposition
	)
	if ar.StopRequested || ar.RunStatus == domain.RunStatusStopping {
		status = domain.RunStatusExited
		disposition = domain.TerminalDispositionCancelled
	} else {
		status = domain.RunStatusCrashed
		disposition = domain.TerminalDispositionFailed
	}

	finishedAt := time.Now().UTC()
	if err := r.Client.UpdateRunState(ctx, ar.RunID, RunStateUpdate{
		Status:              status,
		FinishedAt:          &finishedAt,
		TerminalDisposition: &disposition,
	}); err != nil {
		return fmt.Errorf("reconcile run %s to %s: %w", ar.RunID, status, err)
	}

	logger.Info("reconciled missing tmux session",
		"run_id", ar.RunID,
		"session", sessionName,
		"status", status,
		"disposition", disposition,
	)
	return nil
}

func (r Runner) captureSnapshot(ctx context.Context, ar ActiveRun, capturer PaneCapturer, logger *slog.Logger) {
	sessionName := ar.SessionName()
	if sessionName == "" {
		return
	}

	switch ar.RunStatus {
	case domain.RunStatusActive, domain.RunStatusWaiting, domain.RunStatusStopping:
		// eligible for capture
	default:
		return
	}

	windowName := ar.WindowName()
	if windowName == "" {
		windowName = triptychtmux.DefaultWindowName
	}

	output, lineCount, err := capturer.CapturePane(ctx, sessionName, windowName)
	if err != nil {
		logger.Debug("snapshot capture failed", "run_id", ar.RunID, "error", err)
		return
	}

	now := time.Now().UTC()
	if err := r.Client.UploadSnapshot(ctx, ar.RunID, SnapshotUpload{
		HostID:     r.Config.HostID,
		CapturedAt: now,
		LineCount:  lineCount,
		Stale:      false,
		Output:     output,
	}); err != nil {
		logger.Debug("snapshot upload failed", "run_id", ar.RunID, "error", err)
	}
}

func stringPtr(value string) *string {
	return &value
}
