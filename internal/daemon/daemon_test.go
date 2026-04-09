package daemon

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/xlyk/triptych/internal/domain"
	triptychtmux "github.com/xlyk/triptych/internal/tmux"
)

func TestRunnerRunRegistersThenHeartbeatsUntilShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &stubClient{
		onHeartbeat: func(call int) {
			if call == 2 {
				cancel()
			}
		},
	}

	runner := Runner{
		Config: Config{
			HostID:            "host-1",
			Hostname:          "mbp.local",
			Capabilities:      []string{"codex"},
			HeartbeatInterval: 5 * time.Millisecond,
		},
		Client: client,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	if !slices.Equal(client.calls, []string{"register", "heartbeat", "heartbeat"}) {
		t.Fatalf("call order = %#v", client.calls)
	}
	if client.registration.HostID != "host-1" {
		t.Fatalf("registered host_id = %q", client.registration.HostID)
	}
}

func TestRunnerRunPollsWorkAndLaunchesRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &stubClient{
		workByHeartbeat: map[int]*Work{
			1: {
				HostID: "host-1",
				LaunchableJobs: []LaunchableJob{{
					JobID:   "job-1",
					RunID:   "run-1",
					Workdir: "/tmp/repo",
					Goal:    "Fix the flaky test",
				}},
			},
			2: {
				HostID: "host-1",
			},
		},
		onHeartbeat: func(call int) {
			if call == 2 {
				cancel()
			}
		},
	}
	launcher := &stubLauncher{
		result: triptychtmux.LaunchResult{
			SessionName: "triptych-run-1",
			WindowName:  triptychtmux.DefaultWindowName,
			Created:     true,
		},
	}

	runner := Runner{
		Config: Config{
			HostID:            "host-1",
			Hostname:          "mbp.local",
			Capabilities:      []string{"codex", "tmux"},
			HeartbeatInterval: 5 * time.Millisecond,
		},
		Client: client,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Launch: launcher,
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(launcher.specs) != 1 {
		t.Fatalf("launch calls = %d, want 1", len(launcher.specs))
	}
	if launcher.specs[0].RunID != "run-1" {
		t.Fatalf("launched run_id = %q", launcher.specs[0].RunID)
	}
	if launcher.specs[0].Goal != "Fix the flaky test" {
		t.Fatalf("launched goal = %q", launcher.specs[0].Goal)
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	if got := slices.Clone(client.getWorkHosts); !slices.Equal(got, []domain.HostID{"host-1", "host-1"}) {
		t.Fatalf("GetWork hosts = %#v", got)
	}
	if len(client.runUpdates) != 1 {
		t.Fatalf("run updates = %d, want 1", len(client.runUpdates))
	}
	update := client.runUpdates[0]
	if update.runID != "run-1" {
		t.Fatalf("updated run_id = %q", update.runID)
	}
	if update.state.Status != domain.RunStatusActive {
		t.Fatalf("updated status = %q", update.state.Status)
	}
	if update.state.TmuxSessionName == nil || *update.state.TmuxSessionName != "triptych-run-1" {
		t.Fatalf("tmux_session_name = %#v", update.state.TmuxSessionName)
	}
	if update.state.TmuxWindowName == nil || *update.state.TmuxWindowName != triptychtmux.DefaultWindowName {
		t.Fatalf("tmux_window_name = %#v", update.state.TmuxWindowName)
	}
	if update.state.StartedAt == nil {
		t.Fatal("expected started_at to be set")
	}
}

func TestRunnerExecutesSendCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &stubClient{
		workByHeartbeat: map[int]*Work{
			1: {
				HostID: "host-1",
				ActiveRuns: []ActiveRun{{
					RunID:     "run-1",
					JobID:     "job-1",
					RunStatus: domain.RunStatusActive,
					Tmux:      TmuxRef{SessionName: "triptych-run-1", WindowName: "main"},
				}},
				PendingCommands: []PendingCommand{{
					CommandID:   "cmd-1",
					RunID:       "run-1",
					CommandType: domain.CommandTypeSend,
					Payload:     &CommandPayload{Text: "hello world"},
				}},
			},
		},
		onHeartbeat: func(call int) {
			if call == 1 {
				cancel()
			}
		},
	}
	ctrl := &stubController{}

	runner := Runner{
		Config: Config{
			HostID:            "host-1",
			Hostname:          "mbp.local",
			HeartbeatInterval: 5 * time.Millisecond,
		},
		Client:  client,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Launch:  &stubLauncher{},
		Control: ctrl,
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()

	if len(client.ackedCommands) != 1 || client.ackedCommands[0] != "cmd-1" {
		t.Fatalf("acked = %v, want [cmd-1]", client.ackedCommands)
	}
	if len(client.observedCmds) != 1 || client.observedCmds[0] != "cmd-1" {
		t.Fatalf("observed = %v, want [cmd-1]", client.observedCmds)
	}
	if len(ctrl.sendCalls) != 1 {
		t.Fatalf("send calls = %d, want 1", len(ctrl.sendCalls))
	}
	if ctrl.sendCalls[0].text != "hello world" {
		t.Fatalf("sent text = %q", ctrl.sendCalls[0].text)
	}
	if ctrl.sendCalls[0].session != "triptych-run-1" {
		t.Fatalf("session = %q", ctrl.sendCalls[0].session)
	}
}

func TestRunnerExecutesInterruptCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &stubClient{
		workByHeartbeat: map[int]*Work{
			1: {
				HostID: "host-1",
				ActiveRuns: []ActiveRun{{
					RunID:     "run-1",
					JobID:     "job-1",
					RunStatus: domain.RunStatusActive,
					Tmux:      TmuxRef{SessionName: "triptych-run-1", WindowName: "main"},
				}},
				PendingCommands: []PendingCommand{{
					CommandID:   "cmd-2",
					RunID:       "run-1",
					CommandType: domain.CommandTypeInterrupt,
				}},
			},
		},
		onHeartbeat: func(call int) {
			if call == 1 {
				cancel()
			}
		},
	}
	ctrl := &stubController{}

	runner := Runner{
		Config: Config{
			HostID:            "host-1",
			Hostname:          "mbp.local",
			HeartbeatInterval: 5 * time.Millisecond,
		},
		Client:  client,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Launch:  &stubLauncher{},
		Control: ctrl,
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()

	if len(ctrl.interrupts) != 1 {
		t.Fatalf("interrupt calls = %d, want 1", len(ctrl.interrupts))
	}
	if ctrl.interrupts[0].session != "triptych-run-1" {
		t.Fatalf("session = %q", ctrl.interrupts[0].session)
	}
}

func TestRunnerExecutesStopCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &stubClient{
		workByHeartbeat: map[int]*Work{
			1: {
				HostID: "host-1",
				ActiveRuns: []ActiveRun{{
					RunID:     "run-1",
					JobID:     "job-1",
					RunStatus: domain.RunStatusActive,
					Tmux:      TmuxRef{SessionName: "triptych-run-1", WindowName: "main"},
				}},
				PendingCommands: []PendingCommand{{
					CommandID:   "cmd-3",
					RunID:       "run-1",
					CommandType: domain.CommandTypeStop,
				}},
			},
		},
		onHeartbeat: func(call int) {
			if call == 1 {
				cancel()
			}
		},
	}
	ctrl := &stubController{killResult: true}

	runner := Runner{
		Config: Config{
			HostID:            "host-1",
			Hostname:          "mbp.local",
			HeartbeatInterval: 5 * time.Millisecond,
		},
		Client:  client,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Launch:  &stubLauncher{},
		Control: ctrl,
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()

	// Should ack, then observe.
	if len(client.ackedCommands) != 1 || client.ackedCommands[0] != "cmd-3" {
		t.Fatalf("acked = %v", client.ackedCommands)
	}
	if len(client.observedCmds) != 1 || client.observedCmds[0] != "cmd-3" {
		t.Fatalf("observed = %v", client.observedCmds)
	}

	// Should kill the tmux session.
	if len(ctrl.kills) != 1 || ctrl.kills[0] != "triptych-run-1" {
		t.Fatalf("kills = %v", ctrl.kills)
	}

	// Should have two run state updates: stopping, then exited.
	if len(client.runUpdates) != 2 {
		t.Fatalf("run updates = %d, want 2", len(client.runUpdates))
	}
	if client.runUpdates[0].state.Status != domain.RunStatusStopping {
		t.Fatalf("first update status = %q, want stopping", client.runUpdates[0].state.Status)
	}
	if client.runUpdates[1].state.Status != domain.RunStatusExited {
		t.Fatalf("second update status = %q, want exited", client.runUpdates[1].state.Status)
	}
	if client.runUpdates[1].state.TerminalDisposition == nil || *client.runUpdates[1].state.TerminalDisposition != domain.TerminalDispositionCancelled {
		t.Fatalf("terminal disposition = %v, want cancelled", client.runUpdates[1].state.TerminalDisposition)
	}
	if client.runUpdates[1].state.FinishedAt == nil {
		t.Fatal("expected finished_at to be set")
	}
}

func TestRunnerCommandForUnknownRunObservesWithoutAction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &stubClient{
		workByHeartbeat: map[int]*Work{
			1: {
				HostID:     "host-1",
				ActiveRuns: []ActiveRun{}, // no active runs
				PendingCommands: []PendingCommand{{
					CommandID:   "cmd-orphan",
					RunID:       "run-gone",
					CommandType: domain.CommandTypeSend,
					Payload:     &CommandPayload{Text: "hi"},
				}},
			},
		},
		onHeartbeat: func(call int) {
			if call == 1 {
				cancel()
			}
		},
	}
	ctrl := &stubController{}

	runner := Runner{
		Config: Config{
			HostID:            "host-1",
			Hostname:          "mbp.local",
			HeartbeatInterval: 5 * time.Millisecond,
		},
		Client:  client,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Launch:  &stubLauncher{},
		Control: ctrl,
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()

	// Should ack + observe but NOT send keys.
	if len(client.ackedCommands) != 1 {
		t.Fatalf("acked = %v", client.ackedCommands)
	}
	if len(client.observedCmds) != 1 {
		t.Fatalf("observed = %v", client.observedCmds)
	}
	if len(ctrl.sendCalls) != 0 {
		t.Fatalf("send calls = %d, want 0", len(ctrl.sendCalls))
	}
}

type stubClient struct {
	mu              sync.Mutex
	calls           []string
	registration    HostRegistration
	heartbeats      int
	onHeartbeat     func(int)
	getWorkHosts    []domain.HostID
	workByHeartbeat map[int]*Work
	runUpdates      []stubRunUpdate
	ackedCommands   []domain.CommandID
	observedCmds    []domain.CommandID
}

func (s *stubClient) RegisterHost(_ context.Context, host HostRegistration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "register")
	s.registration = host
	return nil
}

func (s *stubClient) Heartbeat(_ context.Context, _ domain.HostID) error {
	s.mu.Lock()
	s.calls = append(s.calls, "heartbeat")
	s.heartbeats++
	call := s.heartbeats
	onHeartbeat := s.onHeartbeat
	s.mu.Unlock()

	if onHeartbeat != nil {
		onHeartbeat(call)
	}
	return nil
}

func (s *stubClient) GetWork(_ context.Context, hostID domain.HostID) (*Work, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getWorkHosts = append(s.getWorkHosts, hostID)
	if work := s.workByHeartbeat[s.heartbeats]; work != nil {
		cp := *work
		cp.LaunchableJobs = append([]LaunchableJob(nil), work.LaunchableJobs...)
		cp.ActiveRuns = append([]ActiveRun(nil), work.ActiveRuns...)
		cp.PendingCommands = append([]PendingCommand(nil), work.PendingCommands...)
		return &cp, nil
	}
	return &Work{HostID: hostID}, nil
}

func (s *stubClient) UpdateRunState(_ context.Context, runID domain.RunID, state RunStateUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runUpdates = append(s.runUpdates, stubRunUpdate{runID: runID, state: state})
	return nil
}

func (s *stubClient) AckCommand(_ context.Context, commandID domain.CommandID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ackedCommands = append(s.ackedCommands, commandID)
	return nil
}

func (s *stubClient) ObserveCommand(_ context.Context, commandID domain.CommandID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observedCmds = append(s.observedCmds, commandID)
	return nil
}

type stubRunUpdate struct {
	runID domain.RunID
	state RunStateUpdate
}

type stubLauncher struct {
	specs  []triptychtmux.LaunchSpec
	result triptychtmux.LaunchResult
}

func (s *stubLauncher) Launch(_ context.Context, spec triptychtmux.LaunchSpec) (triptychtmux.LaunchResult, error) {
	s.specs = append(s.specs, spec)
	return s.result, nil
}

type stubController struct {
	mu              sync.Mutex
	sendCalls       []stubSendCall
	interrupts      []stubInterruptCall
	kills           []string
	killResult      bool
	hasSessionCalls []string
	// sessionExists controls what HasSession returns per session name.
	// If a name is absent, HasSession returns true (session exists).
	sessionExists map[string]bool
}

type stubSendCall struct {
	session string
	window  string
	text    string
}

type stubInterruptCall struct {
	session string
	window  string
}

func (s *stubController) HasSession(_ context.Context, session string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hasSessionCalls = append(s.hasSessionCalls, session)
	if s.sessionExists != nil {
		return s.sessionExists[session], nil
	}
	return true, nil
}

func (s *stubController) SendKeys(_ context.Context, session, window, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sendCalls = append(s.sendCalls, stubSendCall{session: session, window: window, text: text})
	return nil
}

func (s *stubController) SendInterrupt(_ context.Context, session, window string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interrupts = append(s.interrupts, stubInterruptCall{session: session, window: window})
	return nil
}

func (s *stubController) KillSession(_ context.Context, session string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kills = append(s.kills, session)
	return s.killResult, nil
}

func TestRunnerReconcilesMissingSessionToCrashed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &stubClient{
		workByHeartbeat: map[int]*Work{
			1: {
				HostID: "host-1",
				ActiveRuns: []ActiveRun{{
					RunID:     "run-1",
					JobID:     "job-1",
					RunStatus: domain.RunStatusActive,
					Tmux:      TmuxRef{SessionName: "triptych-run-1", WindowName: "main"},
				}},
			},
		},
		onHeartbeat: func(call int) {
			if call == 1 {
				cancel()
			}
		},
	}
	ctrl := &stubController{
		sessionExists: map[string]bool{"triptych-run-1": false},
	}

	runner := Runner{
		Config: Config{
			HostID:            "host-1",
			Hostname:          "mbp.local",
			HeartbeatInterval: 5 * time.Millisecond,
		},
		Client:  client,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Launch:  &stubLauncher{},
		Control: ctrl,
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	if len(client.runUpdates) != 1 {
		t.Fatalf("run updates = %d, want 1", len(client.runUpdates))
	}
	update := client.runUpdates[0]
	if update.runID != "run-1" {
		t.Fatalf("run_id = %q", update.runID)
	}
	if update.state.Status != domain.RunStatusCrashed {
		t.Fatalf("status = %q, want crashed", update.state.Status)
	}
	if update.state.TerminalDisposition == nil || *update.state.TerminalDisposition != domain.TerminalDispositionFailed {
		t.Fatalf("disposition = %v, want failed", update.state.TerminalDisposition)
	}
	if update.state.FinishedAt == nil {
		t.Fatal("expected finished_at to be set")
	}
}

func TestRunnerReconcilesMissingSessionWithStopRequestedToCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &stubClient{
		workByHeartbeat: map[int]*Work{
			1: {
				HostID: "host-1",
				ActiveRuns: []ActiveRun{{
					RunID:         "run-2",
					JobID:         "job-2",
					RunStatus:     domain.RunStatusActive,
					Tmux:          TmuxRef{SessionName: "triptych-run-2", WindowName: "main"},
					StopRequested: true,
				}},
			},
		},
		onHeartbeat: func(call int) {
			if call == 1 {
				cancel()
			}
		},
	}
	ctrl := &stubController{
		sessionExists: map[string]bool{"triptych-run-2": false},
	}

	runner := Runner{
		Config: Config{
			HostID:            "host-1",
			Hostname:          "mbp.local",
			HeartbeatInterval: 5 * time.Millisecond,
		},
		Client:  client,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Launch:  &stubLauncher{},
		Control: ctrl,
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	if len(client.runUpdates) != 1 {
		t.Fatalf("run updates = %d, want 1", len(client.runUpdates))
	}
	update := client.runUpdates[0]
	if update.state.Status != domain.RunStatusExited {
		t.Fatalf("status = %q, want exited", update.state.Status)
	}
	if update.state.TerminalDisposition == nil || *update.state.TerminalDisposition != domain.TerminalDispositionCancelled {
		t.Fatalf("disposition = %v, want cancelled", update.state.TerminalDisposition)
	}
}

func TestRunnerReconcilesMissingSessionStoppingToCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &stubClient{
		workByHeartbeat: map[int]*Work{
			1: {
				HostID: "host-1",
				ActiveRuns: []ActiveRun{{
					RunID:     "run-3",
					JobID:     "job-3",
					RunStatus: domain.RunStatusStopping,
					Tmux:      TmuxRef{SessionName: "triptych-run-3", WindowName: "main"},
				}},
			},
		},
		onHeartbeat: func(call int) {
			if call == 1 {
				cancel()
			}
		},
	}
	ctrl := &stubController{
		sessionExists: map[string]bool{"triptych-run-3": false},
	}

	runner := Runner{
		Config: Config{
			HostID:            "host-1",
			Hostname:          "mbp.local",
			HeartbeatInterval: 5 * time.Millisecond,
		},
		Client:  client,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Launch:  &stubLauncher{},
		Control: ctrl,
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	if len(client.runUpdates) != 1 {
		t.Fatalf("run updates = %d, want 1", len(client.runUpdates))
	}
	update := client.runUpdates[0]
	if update.state.Status != domain.RunStatusExited {
		t.Fatalf("status = %q, want exited", update.state.Status)
	}
	if update.state.TerminalDisposition == nil || *update.state.TerminalDisposition != domain.TerminalDispositionCancelled {
		t.Fatalf("disposition = %v, want cancelled", update.state.TerminalDisposition)
	}
}

func TestRunnerReconcileLeavesExistingSessionAlone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &stubClient{
		workByHeartbeat: map[int]*Work{
			1: {
				HostID: "host-1",
				ActiveRuns: []ActiveRun{{
					RunID:     "run-4",
					JobID:     "job-4",
					RunStatus: domain.RunStatusActive,
					Tmux:      TmuxRef{SessionName: "triptych-run-4", WindowName: "main"},
				}},
			},
		},
		onHeartbeat: func(call int) {
			if call == 1 {
				cancel()
			}
		},
	}
	ctrl := &stubController{
		sessionExists: map[string]bool{"triptych-run-4": true},
	}

	runner := Runner{
		Config: Config{
			HostID:            "host-1",
			Hostname:          "mbp.local",
			HeartbeatInterval: 5 * time.Millisecond,
		},
		Client:  client,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Launch:  &stubLauncher{},
		Control: ctrl,
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	// No run state updates should have been made.
	if len(client.runUpdates) != 0 {
		t.Fatalf("run updates = %d, want 0", len(client.runUpdates))
	}
}
