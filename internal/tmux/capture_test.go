package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

type stubOutputRunner struct {
	output string
	err    error
	calls  []string
}

func (r *stubOutputRunner) RunOutput(_ context.Context, name string, args ...string) (string, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return r.output, r.err
}

func TestCapturePaneSuccess(t *testing.T) {
	t.Parallel()
	runner := &stubOutputRunner{
		output: "line1\nline2\nline3\n",
	}
	cap := NewCapturerWithRunner(runner)

	text, count, err := cap.CapturePane(context.Background(), "sess-1", "main")
	if err != nil {
		t.Fatalf("CapturePane() error = %v", err)
	}
	if count != 3 {
		t.Fatalf("line count = %d, want 3", count)
	}
	if text != "line1\nline2\nline3" {
		t.Fatalf("text = %q", text)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(runner.calls))
	}
	got := runner.calls[0]
	want := "tmux capture-pane -p -t sess-1:main -S -200"
	if got != want {
		t.Fatalf("call = %q, want %q", got, want)
	}
}

func TestCapturePaneEmptyOutput(t *testing.T) {
	t.Parallel()
	runner := &stubOutputRunner{output: "\n"}
	cap := NewCapturerWithRunner(runner)

	text, count, err := cap.CapturePane(context.Background(), "sess-1", "main")
	if err != nil {
		t.Fatalf("CapturePane() error = %v", err)
	}
	if count != 0 {
		t.Fatalf("line count = %d, want 0", count)
	}
	if text != "" {
		t.Fatalf("text = %q, want empty", text)
	}
}

func TestCapturePaneCompletelyEmpty(t *testing.T) {
	t.Parallel()
	runner := &stubOutputRunner{output: ""}
	cap := NewCapturerWithRunner(runner)

	text, count, err := cap.CapturePane(context.Background(), "sess-1", "main")
	if err != nil {
		t.Fatalf("CapturePane() error = %v", err)
	}
	if count != 0 {
		t.Fatalf("line count = %d, want 0", count)
	}
	if text != "" {
		t.Fatalf("text = %q, want empty", text)
	}
}

func TestCapturePaneSingleLine(t *testing.T) {
	t.Parallel()
	runner := &stubOutputRunner{output: "hello world\n"}
	cap := NewCapturerWithRunner(runner)

	text, count, err := cap.CapturePane(context.Background(), "sess-1", "main")
	if err != nil {
		t.Fatalf("CapturePane() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("line count = %d, want 1", count)
	}
	if text != "hello world" {
		t.Fatalf("text = %q", text)
	}
}

func TestCapturePaneExitError(t *testing.T) {
	t.Parallel()
	runner := &stubOutputRunner{err: &exec.ExitError{}}
	cap := NewCapturerWithRunner(runner)

	_, _, err := cap.CapturePane(context.Background(), "sess-gone", "main")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "capture-pane") {
		t.Fatalf("error = %v", err)
	}
}

func TestCapturePaneNetworkError(t *testing.T) {
	t.Parallel()
	runner := &stubOutputRunner{err: fmt.Errorf("connection refused")}
	cap := NewCapturerWithRunner(runner)

	_, _, err := cap.CapturePane(context.Background(), "sess-1", "main")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("error = %v", err)
	}
}

func TestCapturePaneTrailingNewlines(t *testing.T) {
	t.Parallel()
	runner := &stubOutputRunner{output: "a\nb\n\n\n"}
	cap := NewCapturerWithRunner(runner)

	text, count, err := cap.CapturePane(context.Background(), "sess-1", "main")
	if err != nil {
		t.Fatalf("CapturePane() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("line count = %d, want 2", count)
	}
	if text != "a\nb" {
		t.Fatalf("text = %q", text)
	}
}
