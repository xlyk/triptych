package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

func TestRunnerDoesNotReapplyCommandWithDurableReceipt(t *testing.T) {
	receipts := NewLocalCommandReceiptStore(filepath.Join(t.TempDir(), "receipts"))
	cmd := PendingCommand{CommandID: "cmd-keep", RunID: "run-1", CommandType: domain.CommandTypeSend}
	if err := receipts.MarkApplied(cmd); err != nil {
		t.Fatalf("mark applied: %v", err)
	}

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
					CommandID:   "cmd-keep",
					RunID:       "run-1",
					CommandType: domain.CommandTypeSend,
					Payload:     &CommandPayload{Text: "hello again"},
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
		Config:   Config{HostID: "host-1", Hostname: "mbp.local", HeartbeatInterval: 5 * time.Millisecond},
		Client:   client,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Launch:   &stubLauncher{},
		Control:  ctrl,
		Receipts: receipts,
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()
	if len(ctrl.sendCalls) != 0 {
		t.Fatalf("send calls = %d, want 0", len(ctrl.sendCalls))
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.ackedCommands) != 1 || client.ackedCommands[0] != "cmd-keep" {
		t.Fatalf("acked = %v, want [cmd-keep]", client.ackedCommands)
	}
	if len(client.observedCmds) != 1 || client.observedCmds[0] != "cmd-keep" {
		t.Fatalf("observed = %v, want [cmd-keep]", client.observedCmds)
	}
	if applied, err := receipts.HasApplied("cmd-keep"); err != nil {
		t.Fatalf("HasApplied() error = %v", err)
	} else if applied {
		t.Fatal("expected receipt to be cleared after observe")
	}
}

func TestRunnerLeavesAcknowledgedCommandPendingWhenActionFails(t *testing.T) {
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
					CommandID:   "cmd-fail",
					RunID:       "run-1",
					CommandType: domain.CommandTypeSend,
					Payload:     &CommandPayload{Text: "retry me"},
				}},
			},
		},
		onHeartbeat: func(call int) {
			if call == 1 {
				cancel()
			}
		},
	}
	ctrl := &stubController{sendErr: fmt.Errorf("tmux busy")}
	runner := Runner{
		Config:   Config{HostID: "host-1", Hostname: "mbp.local", HeartbeatInterval: 5 * time.Millisecond},
		Client:   client,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Launch:   &stubLauncher{},
		Control:  ctrl,
		Receipts: NewLocalCommandReceiptStore(filepath.Join(t.TempDir(), "receipts")),
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.ackedCommands) != 1 || client.ackedCommands[0] != "cmd-fail" {
		t.Fatalf("acked = %v", client.ackedCommands)
	}
	if len(client.observedCmds) != 0 {
		t.Fatalf("observed = %v, want none", client.observedCmds)
	}
}

func TestLocalCommandReceiptStoreRoundTrip(t *testing.T) {
	store := NewLocalCommandReceiptStore(filepath.Join(t.TempDir(), "receipts"))
	cmd := PendingCommand{CommandID: "cmd-store", RunID: "run-1", CommandType: domain.CommandTypeInterrupt}
	if err := store.MarkApplied(cmd); err != nil {
		t.Fatalf("MarkApplied() error = %v", err)
	}
	if ok, err := store.HasApplied(cmd.CommandID); err != nil {
		t.Fatalf("HasApplied() error = %v", err)
	} else if !ok {
		t.Fatal("expected receipt to exist")
	}
	path := filepath.Join(store.baseDir, "cmd-store.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if len(data) == 0 {
		t.Fatal("receipt file should not be empty")
	}
	if err := store.Clear(cmd.CommandID); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if ok, err := store.HasApplied(cmd.CommandID); err != nil {
		t.Fatalf("HasApplied() after clear error = %v", err)
	} else if ok {
		t.Fatal("expected receipt to be removed")
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
	snapshots       []stubSnapshot
}

type stubSnapshot struct {
	runID    domain.RunID
	snapshot SnapshotUpload
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

func (s *stubClient) UploadSnapshot(_ context.Context, runID domain.RunID, snapshot SnapshotUpload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots = append(s.snapshots, stubSnapshot{runID: runID, snapshot: snapshot})
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
	sendErr         error
	interruptErr    error
	killErr         error
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
	return s.sendErr
}

func (s *stubController) SendInterrupt(_ context.Context, session, window string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interrupts = append(s.interrupts, stubInterruptCall{session: session, window: window})
	return s.interruptErr
}

func (s *stubController) KillSession(_ context.Context, session string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kills = append(s.kills, session)
	return s.killResult, s.killErr
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

type stubCapturer struct {
	mu     sync.Mutex
	output string
	lines  int
	err    error
	calls  []captureCall
}

type captureCall struct {
	session string
	window  string
}

func (s *stubCapturer) CapturePane(_ context.Context, sessionName, windowName string) (string, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, captureCall{session: sessionName, window: windowName})
	return s.output, s.lines, s.err
}

func TestRunnerCapturesSnapshotForActiveRuns(t *testing.T) {
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
		sessionExists: map[string]bool{"triptych-run-1": true},
	}
	cap := &stubCapturer{
		output: "hello\nworld",
		lines:  2,
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
		Capture: cap,
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	cap.mu.Lock()
	defer cap.mu.Unlock()

	if len(cap.calls) != 1 {
		t.Fatalf("capture calls = %d, want 1", len(cap.calls))
	}
	if cap.calls[0].session != "triptych-run-1" {
		t.Fatalf("captured session = %q", cap.calls[0].session)
	}
	if cap.calls[0].window != "main" {
		t.Fatalf("captured window = %q", cap.calls[0].window)
	}

	if len(client.snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(client.snapshots))
	}
	snap := client.snapshots[0]
	if snap.runID != "run-1" {
		t.Fatalf("snapshot run_id = %q", snap.runID)
	}
	if snap.snapshot.HostID != "host-1" {
		t.Fatalf("snapshot host_id = %q", snap.snapshot.HostID)
	}
	if snap.snapshot.LineCount != 2 {
		t.Fatalf("snapshot line_count = %d", snap.snapshot.LineCount)
	}
	if snap.snapshot.Output != "hello\nworld" {
		t.Fatalf("snapshot output = %q", snap.snapshot.Output)
	}
	if snap.snapshot.Stale {
		t.Fatal("snapshot should not be stale")
	}
	if snap.snapshot.CapturedAt.IsZero() {
		t.Fatal("snapshot captured_at should be set")
	}
}

func TestRunnerSkipsSnapshotForNonLiveRuns(t *testing.T) {
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
					Tmux:      TmuxRef{SessionName: "", WindowName: ""},
				}},
			},
		},
		onHeartbeat: func(call int) {
			if call == 1 {
				cancel()
			}
		},
	}
	cap := &stubCapturer{output: "test", lines: 1}

	runner := Runner{
		Config: Config{
			HostID:            "host-1",
			Hostname:          "mbp.local",
			HeartbeatInterval: 5 * time.Millisecond,
		},
		Client:  client,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Launch:  &stubLauncher{},
		Control: &stubController{},
		Capture: cap,
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()

	if len(cap.calls) != 0 {
		t.Fatalf("capture calls = %d, want 0 (no session name)", len(cap.calls))
	}
}

func TestRunnerSnapshotCaptureErrorDoesNotFail(t *testing.T) {
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
	cap := &stubCapturer{
		err: fmt.Errorf("tmux not running"),
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
		Control: &stubController{sessionExists: map[string]bool{"triptych-run-1": true}},
		Capture: cap,
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v, want nil (capture errors are non-fatal)", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	if len(client.snapshots) != 0 {
		t.Fatalf("snapshots = %d, want 0 (capture failed)", len(client.snapshots))
	}
}
