package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// Controller sends keystrokes and manages tmux sessions.
type Controller struct {
	runner CommandRunner
}

func NewController() Controller {
	return Controller{runner: execRunner{}}
}

func NewControllerWithRunner(runner CommandRunner) Controller {
	return Controller{runner: runner}
}

// SendKeys sends text followed by Enter to the given tmux target.
func (c Controller) SendKeys(ctx context.Context, sessionName, windowName, text string) error {
	if c.runner == nil {
		c.runner = execRunner{}
	}
	target := sessionName + ":" + windowName
	if err := c.runner.Run(ctx, "tmux", "send-keys", "-t", target, text, "Enter"); err != nil {
		return fmt.Errorf("send-keys to %q: %w", target, err)
	}
	return nil
}

// SendInterrupt sends C-c to the given tmux target.
func (c Controller) SendInterrupt(ctx context.Context, sessionName, windowName string) error {
	if c.runner == nil {
		c.runner = execRunner{}
	}
	target := sessionName + ":" + windowName
	if err := c.runner.Run(ctx, "tmux", "send-keys", "-t", target, "C-c", ""); err != nil {
		return fmt.Errorf("send C-c to %q: %w", target, err)
	}
	return nil
}

// HasSession reports whether a tmux session with the given name exists.
func (c Controller) HasSession(ctx context.Context, sessionName string) (bool, error) {
	if c.runner == nil {
		c.runner = execRunner{}
	}
	err := c.runner.Run(ctx, "tmux", "has-session", "-t", "="+sessionName)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, fmt.Errorf("check tmux session %q: %w", sessionName, err)
}

// KillSession kills the entire tmux session. Returns true if the session
// existed and was killed, false if it did not exist.
func (c Controller) KillSession(ctx context.Context, sessionName string) (bool, error) {
	if c.runner == nil {
		c.runner = execRunner{}
	}
	err := c.runner.Run(ctx, "tmux", "kill-session", "-t", "="+sessionName)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// tmux returns non-zero when the session doesn't exist
		return false, nil
	}
	return false, fmt.Errorf("kill tmux session %q: %w", sessionName, err)
}
