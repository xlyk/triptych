package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestControllerSendKeysClearsPromptAndSendsLiteralText(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{}
	ctrl := NewControllerWithRunner(runner)

	if err := ctrl.SendKeys(context.Background(), "sess-1", "main", "hello world"); err != nil {
		t.Fatalf("SendKeys() error = %v", err)
	}

	if len(runner.calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(runner.calls))
	}
	want := []string{
		"tmux send-keys -t sess-1:main C-u",
		"tmux send-keys -t sess-1:main -l hello world",
		"tmux send-keys -t sess-1:main Enter",
	}
	for i, got := range runner.calls {
		if got != want[i] {
			t.Fatalf("call[%d] = %q, want %q", i, got, want[i])
		}
	}
}

func TestControllerSendInterrupt(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{}
	ctrl := NewControllerWithRunner(runner)

	if err := ctrl.SendInterrupt(context.Background(), "sess-1", "main"); err != nil {
		t.Fatalf("SendInterrupt() error = %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(runner.calls))
	}
	got := runner.calls[0]
	want := "tmux send-keys -t sess-1:main C-c "
	if got != want {
		t.Fatalf("call = %q, want %q", got, want)
	}
}

func TestControllerHasSessionExists(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{}
	ctrl := NewControllerWithRunner(runner)

	exists, err := ctrl.HasSession(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("HasSession() error = %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true")
	}

	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(runner.calls))
	}
	got := runner.calls[0]
	want := "tmux has-session -t =sess-1"
	if got != want {
		t.Fatalf("call = %q, want %q", got, want)
	}
}

func TestControllerHasSessionNotExists(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{
		err: &exec.ExitError{},
	}
	ctrl := NewControllerWithRunner(runner)

	exists, err := ctrl.HasSession(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("HasSession() error = %v", err)
	}
	if exists {
		t.Fatal("expected exists=false for non-existent session")
	}
}

func TestControllerHasSessionError(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{
		err: fmt.Errorf("network error"),
	}
	ctrl := NewControllerWithRunner(runner)

	_, err := ctrl.HasSession(context.Background(), "sess-1")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "network error") {
		t.Fatalf("error = %v", err)
	}
}

func TestControllerKillSessionExists(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{}
	ctrl := NewControllerWithRunner(runner)

	killed, err := ctrl.KillSession(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("KillSession() error = %v", err)
	}
	if !killed {
		t.Fatal("expected killed=true")
	}

	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(runner.calls))
	}
	got := runner.calls[0]
	want := "tmux kill-session -t =sess-1"
	if got != want {
		t.Fatalf("call = %q, want %q", got, want)
	}
}

func TestControllerKillSessionNotExists(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{
		err: &exec.ExitError{},
	}
	ctrl := NewControllerWithRunner(runner)

	killed, err := ctrl.KillSession(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("KillSession() error = %v", err)
	}
	if killed {
		t.Fatal("expected killed=false for non-existent session")
	}
}

func TestControllerKillSessionError(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{
		err: fmt.Errorf("network error"),
	}
	ctrl := NewControllerWithRunner(runner)

	_, err := ctrl.KillSession(context.Background(), "sess-1")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "network error") {
		t.Fatalf("error = %v", err)
	}
}

type recordingRunner struct {
	calls []string
	err   error
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return r.err
}
