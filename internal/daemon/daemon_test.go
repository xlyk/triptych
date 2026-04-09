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

type stubClient struct {
	mu           sync.Mutex
	calls        []string
	registration HostRegistration
	heartbeats   int
	onHeartbeat  func(int)
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
