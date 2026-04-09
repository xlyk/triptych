package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// MaxCaptureLines is the maximum number of lines captured from a tmux pane.
const MaxCaptureLines = 200

// OutputRunner runs a command and returns its stdout.
type OutputRunner interface {
	RunOutput(ctx context.Context, name string, args ...string) (string, error)
}

// Capturer reads tmux pane content.
type Capturer struct {
	runner OutputRunner
}

// NewCapturer returns a Capturer that shells out to tmux.
func NewCapturer() Capturer {
	return Capturer{runner: execRunner{}}
}

// NewCapturerWithRunner returns a Capturer using the given OutputRunner.
func NewCapturerWithRunner(runner OutputRunner) Capturer {
	return Capturer{runner: runner}
}

// CapturePane captures the last MaxCaptureLines of output from a tmux pane.
// Returns the captured text, line count, and any error.
func (c Capturer) CapturePane(ctx context.Context, sessionName, windowName string) (string, int, error) {
	if c.runner == nil {
		c.runner = execRunner{}
	}
	target := sessionName + ":" + windowName
	out, err := c.runner.RunOutput(ctx, "tmux", "capture-pane", "-p", "-t", target, "-S", "-"+strconv.Itoa(MaxCaptureLines))
	if err != nil {
		return "", 0, fmt.Errorf("capture-pane %q: %w", target, err)
	}
	trimmed := strings.TrimRight(out, "\n")
	if trimmed == "" {
		return "", 0, nil
	}
	lines := strings.Count(trimmed, "\n") + 1
	return trimmed, lines, nil
}

// RunOutput on execRunner shells out and captures stdout.
func (execRunner) RunOutput(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}
