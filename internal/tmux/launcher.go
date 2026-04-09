package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/xlyk/triptych/internal/domain"
)

const DefaultWindowName = "main"

type CommandRunner interface {
	Run(context.Context, string, ...string) error
}

type LaunchSpec struct {
	RunID   domain.RunID
	JobID   domain.JobID
	Workdir string
	Goal    string
}

type LaunchResult struct {
	SessionName string
	WindowName  string
	Created     bool
}

type Launcher struct {
	runner CommandRunner
}

func NewLauncher() Launcher {
	return Launcher{runner: execRunner{}}
}

func NewLauncherWithRunner(runner CommandRunner) Launcher {
	return Launcher{runner: runner}
}

func (l Launcher) Launch(ctx context.Context, spec LaunchSpec) (LaunchResult, error) {
	if l.runner == nil {
		l.runner = execRunner{}
	}

	sessionName := SessionNameForRun(spec.RunID)
	result := LaunchResult{
		SessionName: sessionName,
		WindowName:  DefaultWindowName,
	}

	exists, err := l.hasSession(ctx, sessionName)
	if err != nil {
		return LaunchResult{}, err
	}
	if exists {
		return result, nil
	}

	args := []string{"new-session", "-d", "-s", sessionName, "-n", DefaultWindowName}
	if spec.Workdir != "" {
		args = append(args, "-c", spec.Workdir)
	}
	args = append(args, placeholderCommand(spec))
	if err := l.runner.Run(ctx, "tmux", args...); err != nil {
		return LaunchResult{}, fmt.Errorf("start tmux session %q: %w", sessionName, err)
	}

	result.Created = true
	return result, nil
}

func SessionNameForRun(runID domain.RunID) string {
	return "triptych-" + sanitizeName(runID.String())
}

func placeholderCommand(spec LaunchSpec) string {
	return "sh -lc " + shellQuote(strings.Join([]string{
		"printf '%s\\n' " + shellQuote("Triptych placeholder run"),
		"printf '%s\\n' " + shellQuote("run_id="+spec.RunID.String()),
		"printf '%s\\n' " + shellQuote("job_id="+spec.JobID.String()),
		"printf '%s\\n' " + shellQuote("goal="+spec.Goal),
		"exec tail -f /dev/null",
	}, "; "))
}

func (l Launcher) hasSession(ctx context.Context, sessionName string) (bool, error) {
	err := l.runner.Run(ctx, "tmux", "has-session", "-t", "="+sessionName)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check tmux session %q: %w", sessionName, err)
}

func sanitizeName(value string) string {
	if value == "" {
		return "run"
	}
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	sanitized := strings.Trim(b.String(), "-")
	if sanitized == "" {
		return "run"
	}
	return sanitized
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}
