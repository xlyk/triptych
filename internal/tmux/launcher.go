package tmux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/xlyk/triptych/internal/domain"
)

const DefaultWindowName = "main"

// LaunchMode controls whether the launcher starts real agent processes or
// lightweight placeholders. Default (empty string) is real mode.
type LaunchMode string

const (
	// LaunchModeReal starts real agent CLIs (claude, codex) in tmux.
	LaunchModeReal LaunchMode = ""
	// LaunchModePlaceholder starts a minimal placeholder process that prints
	// metadata and sleeps. Used by the E2E harness for fast deterministic tests.
	LaunchModePlaceholder LaunchMode = "placeholder"
)

type CommandRunner interface {
	Run(context.Context, string, ...string) error
}

type LaunchSpec struct {
	RunID   domain.RunID
	JobID   domain.JobID
	Agent   domain.Agent
	Workdir string
	Goal    string
}

type LaunchConfig struct {
	Claude ClaudeLaunchConfig
	Codex  CodexLaunchConfig
}

type ClaudeLaunchConfig struct {
	SettingsFile       string
	SettingsJSON       string
	TrustedDirectories []string
	PermissionMode     string
	StartupHandshake   bool
}

type CodexLaunchConfig struct {
	ConfigProfile  string
	ApprovalPolicy string
	SandboxMode    string
	TrustProject   bool
}

type LaunchResult struct {
	SessionName string
	WindowName  string
	Created     bool
}

type Launcher struct {
	runner CommandRunner
	Mode   LaunchMode
	Config LaunchConfig
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

	var cmd string
	var injectGoal bool
	if l.Mode == LaunchModePlaceholder {
		cmd = placeholderCommand(spec)
	} else {
		cmd, injectGoal, err = interactiveCommand(spec, l.Config)
		if err != nil {
			return LaunchResult{}, err
		}
	}

	args := []string{"new-session", "-d", "-s", sessionName, "-n", DefaultWindowName}
	if spec.Workdir != "" {
		args = append(args, "-c", spec.Workdir)
	}
	args = append(args, cmd)

	if err := l.runner.Run(ctx, "tmux", args...); err != nil {
		return LaunchResult{}, fmt.Errorf("start tmux session %q: %w", sessionName, err)
	}

	result.Created = true

	// For interactive sessions, optionally perform a small startup handshake and
	// then inject the job goal into the terminal. tmux buffers keystrokes, so the
	// CLI will read them once it's ready.
	if injectGoal && spec.Goal != "" {
		target := sessionName + ":" + DefaultWindowName
		cleanupOnError := func(step string, err error) (LaunchResult, error) {
			cleanupErr := l.runner.Run(ctx, "tmux", "kill-session", "-t", "="+sessionName)
			if cleanupErr != nil {
				return LaunchResult{}, fmt.Errorf("%s in %q: %w (cleanup failed: %v)", step, sessionName, err, cleanupErr)
			}
			return LaunchResult{}, fmt.Errorf("%s in %q: %w", step, sessionName, err)
		}
		if spec.Agent == domain.AgentClaude && l.Config.Claude.StartupHandshake {
			// Claude's interactive workspace-trust prompt is not reliably
			// suppressible in our environment, so this optional handshake accepts
			// the default trust choice before sending the goal.
			if err := l.runner.Run(ctx, "tmux", "send-keys", "-t", target, "Enter"); err != nil {
				return cleanupOnError("bootstrap Claude trust prompt", err)
			}
		}
		// Send goal text literally (the -l flag prevents tmux from
		// interpreting key names like "Enter" inside the goal string).
		if err := l.runner.Run(ctx, "tmux", "send-keys", "-t", target, "-l", spec.Goal); err != nil {
			return cleanupOnError("inject goal", err)
		}
		// Send Enter to submit the goal.
		if err := l.runner.Run(ctx, "tmux", "send-keys", "-t", target, "Enter"); err != nil {
			return cleanupOnError("send Enter", err)
		}
	}

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

// interactiveCommand builds the command that starts a long-lived interactive
// agent CLI. The goal is NOT baked into the command — it is injected into the
// tmux session via send-keys after the session is created (see Launch).
//
//   - claude → claude --verbose          (interactive session)
//   - codex  → codex --quiet             (interactive session)
//
// Both are wrapped in `sh -lc` so command construction and `exec` behave
// consistently under tmux. Returns the shell command and whether goal
// injection is needed.
func interactiveCommand(spec LaunchSpec, cfg LaunchConfig) (cmd string, injectGoal bool, err error) {
	var inner string
	switch spec.Agent {
	case domain.AgentClaude:
		inner, err = claudeInteractiveCommand(cfg.Claude)
	case domain.AgentCodex:
		inner = codexInteractiveCommand(spec.Workdir, cfg.Codex)
	default:
		// Fallback: treat unknown agent as a placeholder so the session is
		// still inspectable rather than silently failing.
		return placeholderCommand(spec), false, nil
	}
	if err != nil {
		return "", false, err
	}
	return "sh -lc " + shellQuote(inner), true, nil
}

func claudeInteractiveCommand(cfg ClaudeLaunchConfig) (string, error) {
	args := []string{"exec", "claude", "--verbose"}
	if cfg.PermissionMode != "" {
		args = append(args, "--permission-mode", cfg.PermissionMode)
	}
	settingsArg, err := claudeSettingsArg(cfg)
	if err != nil {
		return "", err
	}
	if settingsArg != "" {
		args = append(args, "--settings", settingsArg)
	}
	return shellJoin(args...), nil
}

func claudeSettingsArg(cfg ClaudeLaunchConfig) (string, error) {
	if cfg.SettingsFile != "" {
		return cfg.SettingsFile, nil
	}

	settingsJSON := strings.TrimSpace(cfg.SettingsJSON)
	if len(cfg.TrustedDirectories) == 0 {
		return settingsJSON, nil
	}

	settings := map[string]any{}
	if settingsJSON != "" {
		if err := json.Unmarshal([]byte(settingsJSON), &settings); err != nil {
			return "", fmt.Errorf("parse Claude settings JSON: %w", err)
		}
	}
	settings["trustedDirectories"] = cfg.TrustedDirectories
	encoded, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("encode Claude settings JSON: %w", err)
	}
	return string(encoded), nil
}

func codexInteractiveCommand(workdir string, cfg CodexLaunchConfig) string {
	args := []string{"exec", "codex", "--quiet"}
	if cfg.ConfigProfile != "" {
		args = append(args, "--profile", cfg.ConfigProfile)
	}
	if cfg.ApprovalPolicy != "" {
		args = append(args, "--ask-for-approval", cfg.ApprovalPolicy)
	}
	if cfg.SandboxMode != "" {
		args = append(args, "--sandbox", cfg.SandboxMode)
	}
	if cfg.TrustProject && workdir != "" {
		args = append(args, "--config", fmt.Sprintf(`projects.%q.trust_level=%q`, workdir, "trusted"))
	}
	return shellJoin(args...)
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

func shellJoin(parts ...string) string {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, shellQuote(part))
	}
	return strings.Join(quoted, " ")
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}
