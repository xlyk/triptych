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

type stubClient struct {
	mu              sync.Mutex
	calls           []string
	registration    HostRegistration
	heartbeats      int
	onHeartbeat     func(int)
	getWorkHosts    []domain.HostID
	workByHeartbeat map[int]*Work
	runUpdates      []stubRunUpdate
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
		copy := *work
		copy.LaunchableJobs = append([]LaunchableJob(nil), work.LaunchableJobs...)
		return &copy, nil
	}
	return &Work{HostID: hostID}, nil
}

func (s *stubClient) UpdateRunState(_ context.Context, runID domain.RunID, state RunStateUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runUpdates = append(s.runUpdates, stubRunUpdate{runID: runID, state: state})
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
