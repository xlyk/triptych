package tmux

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/xlyk/triptych/internal/domain"
)

func TestInteractiveCommandClaudeDefault(t *testing.T) {
	spec := LaunchSpec{
		RunID: "run-1",
		JobID: "job-1",
		Agent: domain.AgentClaude,
		Goal:  "Fix the tests",
	}
	cmd, inject, err := interactiveCommand(spec, LaunchConfig{})
	if err != nil {
		t.Fatalf("interactiveCommand(claude) error = %v", err)
	}
	for _, want := range []string{"sh -lc", "exec", "claude", "--verbose"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("interactive command missing %q:\n%s", want, cmd)
		}
	}
	if !inject {
		t.Fatal("expected injectGoal=true for claude")
	}
}

func TestInteractiveCommandClaudeWithSettingsAndPermissionMode(t *testing.T) {
	spec := LaunchSpec{Agent: domain.AgentClaude}
	cfg := LaunchConfig{Claude: ClaudeLaunchConfig{
		PermissionMode:     "dontAsk",
		SettingsJSON:       `{"theme":"dark"}`,
		TrustedDirectories: []string{"/repo", "/repo/subdir"},
	}}

	cmd, inject, err := interactiveCommand(spec, cfg)
	if err != nil {
		t.Fatalf("interactiveCommand(claude) error = %v", err)
	}
	if !inject {
		t.Fatal("expected injectGoal=true for claude")
	}
	for _, want := range []string{
		"claude",
		"--verbose",
		"--permission-mode",
		"dontAsk",
		"--settings",
		`"trustedDirectories":["/repo","/repo/subdir"]`,
		`"theme":"dark"`,
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("interactive command missing %q:\n%s", want, cmd)
		}
	}
}

func TestInteractiveCommandClaudeWithSettingsFile(t *testing.T) {
	spec := LaunchSpec{Agent: domain.AgentClaude}
	cfg := LaunchConfig{Claude: ClaudeLaunchConfig{SettingsFile: "/etc/triptych/claude.json"}}

	cmd, _, err := interactiveCommand(spec, cfg)
	if err != nil {
		t.Fatalf("interactiveCommand(claude) error = %v", err)
	}
	if !strings.Contains(cmd, "/etc/triptych/claude.json") {
		t.Fatalf("expected settings file in command: %s", cmd)
	}
}

func TestInteractiveCommandClaudeRejectsBadInlineSettingsJSON(t *testing.T) {
	spec := LaunchSpec{Agent: domain.AgentClaude}
	cfg := LaunchConfig{Claude: ClaudeLaunchConfig{
		SettingsJSON:       `{"theme":`,
		TrustedDirectories: []string{"/repo"},
	}}

	_, _, err := interactiveCommand(spec, cfg)
	if err == nil || !strings.Contains(err.Error(), "parse Claude settings JSON") {
		t.Fatalf("expected Claude settings parse error, got %v", err)
	}
}

func TestInteractiveCommandCodexDefault(t *testing.T) {
	spec := LaunchSpec{
		RunID: "run-1",
		JobID: "job-1",
		Agent: domain.AgentCodex,
		Goal:  "Refactor the module",
	}
	cmd, inject, err := interactiveCommand(spec, LaunchConfig{})
	if err != nil {
		t.Fatalf("interactiveCommand(codex) error = %v", err)
	}
	for _, want := range []string{"sh -lc", "exec", "codex", "--quiet"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("interactive command missing %q:\n%s", want, cmd)
		}
	}
	if !inject {
		t.Fatal("expected injectGoal=true for codex")
	}
}

func TestInteractiveCommandCodexWithTrustAndPolicies(t *testing.T) {
	spec := LaunchSpec{Agent: domain.AgentCodex, Workdir: "/tmp/repo"}
	cfg := LaunchConfig{Codex: CodexLaunchConfig{
		ConfigProfile:  "triptych",
		ApprovalPolicy: "never",
		SandboxMode:    "workspace-write",
		TrustProject:   true,
	}}

	cmd, inject, err := interactiveCommand(spec, cfg)
	if err != nil {
		t.Fatalf("interactiveCommand(codex) error = %v", err)
	}
	if !inject {
		t.Fatal("expected injectGoal=true for codex")
	}
	for _, want := range []string{
		"codex",
		"--quiet",
		"--profile",
		"triptych",
		"--ask-for-approval",
		"never",
		"--sandbox",
		"workspace-write",
		"--config",
		`projects."/tmp/repo".trust_level="trusted"`,
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("interactive command missing %q:\n%s", want, cmd)
		}
	}
}

func TestInteractiveCommandUnknownFallsBackToPlaceholder(t *testing.T) {
	spec := LaunchSpec{
		RunID: "run-1",
		JobID: "job-1",
		Agent: domain.Agent("unknown"),
		Goal:  "Do something",
	}
	cmd, inject, err := interactiveCommand(spec, LaunchConfig{})
	if err != nil {
		t.Fatalf("interactiveCommand(unknown) error = %v", err)
	}
	placeholder := placeholderCommand(spec)
	if cmd != placeholder {
		t.Fatalf("interactiveCommand(unknown) should fall back to placeholder:\n  got: %s\n  want: %s", cmd, placeholder)
	}
	if inject {
		t.Fatal("expected injectGoal=false for unknown agent")
	}
}

func TestInteractiveCommandDoesNotContainGoal(t *testing.T) {
	spec := LaunchSpec{
		RunID: "run-1",
		JobID: "job-1",
		Agent: domain.AgentClaude,
		Goal:  "This goal should NOT appear in the command",
	}
	cmd, _, err := interactiveCommand(spec, LaunchConfig{})
	if err != nil {
		t.Fatalf("interactiveCommand() error = %v", err)
	}
	if strings.Contains(cmd, spec.Goal) {
		t.Fatalf("interactive command should not contain the goal:\n  cmd: %s", cmd)
	}
}

func TestPlaceholderModeUsesPlaceholderCommand(t *testing.T) {
	spec := LaunchSpec{
		RunID: "run-1",
		JobID: "job-1",
		Agent: domain.AgentClaude,
		Goal:  "Should use placeholder",
	}
	placeholder := placeholderCommand(spec)
	interactive, _, err := interactiveCommand(spec, LaunchConfig{})
	if err != nil {
		t.Fatalf("interactiveCommand() error = %v", err)
	}
	if placeholder == interactive {
		t.Fatal("placeholder and interactive commands should differ for claude agent")
	}
}

func TestLaunchInteractiveInjectsGoal(t *testing.T) {
	runner := &launcherTestRunner{}
	l := Launcher{runner: runner, Mode: LaunchModeReal, Config: LaunchConfig{
		Claude: ClaudeLaunchConfig{TrustedDirectories: []string{"/tmp/repo"}, StartupHandshake: true},
	}}

	spec := LaunchSpec{
		RunID:   "run-1",
		JobID:   "job-1",
		Agent:   domain.AgentClaude,
		Workdir: "/tmp/repo",
		Goal:    "Fix the flaky test",
	}

	result, err := l.Launch(context.Background(), spec)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if !result.Created {
		t.Fatal("expected Created=true")
	}
	if len(runner.calls) != 5 {
		t.Fatalf("expected 5 tmux calls, got %d: %v", len(runner.calls), runner.summaries())
	}
	if !strings.Contains(runner.calls[1], "new-session") {
		t.Fatalf("call 1: expected new-session, got %q", runner.calls[1])
	}
	if strings.Contains(runner.calls[1], spec.Goal) {
		t.Fatalf("new-session command should not contain goal: %s", runner.calls[1])
	}
	if !strings.Contains(runner.calls[1], "--settings") || !strings.Contains(runner.calls[1], "trustedDirectories") {
		t.Fatalf("new-session should carry Claude trust settings: %s", runner.calls[1])
	}
	if !strings.Contains(runner.calls[2], "send-keys") || !strings.Contains(runner.calls[2], "Enter") {
		t.Fatalf("Claude bootstrap should send Enter first: %s", runner.calls[2])
	}
	if strings.Contains(runner.calls[2], spec.Goal) {
		t.Fatalf("Claude bootstrap should not contain goal text: %s", runner.calls[2])
	}
	if !strings.Contains(runner.calls[3], spec.Goal) || !strings.Contains(runner.calls[3], "-l") {
		t.Fatalf("send-keys should contain literal goal text: %s", runner.calls[3])
	}
	if !strings.Contains(runner.calls[4], "Enter") {
		t.Fatalf("final send-keys should contain Enter: %s", runner.calls[4])
	}
}

func TestLaunchPlaceholderDoesNotInjectGoal(t *testing.T) {
	runner := &launcherTestRunner{}
	l := Launcher{runner: runner, Mode: LaunchModePlaceholder}

	spec := LaunchSpec{
		RunID: "run-1",
		JobID: "job-1",
		Agent: domain.AgentClaude,
		Goal:  "Should not be injected",
	}

	result, err := l.Launch(context.Background(), spec)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if !result.Created {
		t.Fatal("expected Created=true")
	}
	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 tmux calls, got %d: %v", len(runner.calls), runner.summaries())
	}
}

func TestLaunchExistingSessionSkipsCreation(t *testing.T) {
	runner := &launcherTestRunner{hasSessionExists: true}
	l := Launcher{runner: runner, Mode: LaunchModeReal}

	spec := LaunchSpec{
		RunID: "run-1",
		JobID: "job-1",
		Agent: domain.AgentClaude,
		Goal:  "Should not launch",
	}

	result, err := l.Launch(context.Background(), spec)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if result.Created {
		t.Fatal("expected Created=false for existing session")
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 tmux call, got %d: %v", len(runner.calls), runner.summaries())
	}
}

func TestLaunchInteractiveCodexInjectsGoal(t *testing.T) {
	runner := &launcherTestRunner{}
	l := Launcher{runner: runner, Mode: LaunchModeReal, Config: LaunchConfig{Codex: CodexLaunchConfig{
		ApprovalPolicy: "never",
		SandboxMode:    "workspace-write",
		TrustProject:   true,
	}}}

	spec := LaunchSpec{
		RunID:   "run-1",
		JobID:   "job-1",
		Agent:   domain.AgentCodex,
		Workdir: "/tmp/repo",
		Goal:    "Refactor the module",
	}

	_, err := l.Launch(context.Background(), spec)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if len(runner.calls) != 4 {
		t.Fatalf("expected 4 tmux calls, got %d: %v", len(runner.calls), runner.summaries())
	}
	if !strings.Contains(runner.calls[1], "--ask-for-approval") || !strings.Contains(runner.calls[1], "--sandbox") || !strings.Contains(runner.calls[1], "trust_level") {
		t.Fatalf("new-session should use configured codex launch flags: %s", runner.calls[1])
	}
	if !strings.Contains(runner.calls[2], spec.Goal) {
		t.Fatalf("send-keys should contain goal: %s", runner.calls[2])
	}
}

func TestLaunchEmptyGoalSkipsInjection(t *testing.T) {
	runner := &launcherTestRunner{}
	l := Launcher{runner: runner, Mode: LaunchModeReal}

	spec := LaunchSpec{
		RunID: "run-1",
		JobID: "job-1",
		Agent: domain.AgentClaude,
		Goal:  "",
	}

	_, err := l.Launch(context.Background(), spec)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 tmux calls for empty goal, got %d: %v", len(runner.calls), runner.summaries())
	}
}

func TestLaunchClaudeWithoutStartupHandshakeSkipsBootstrapEnter(t *testing.T) {
	runner := &launcherTestRunner{}
	l := Launcher{runner: runner, Mode: LaunchModeReal}

	spec := LaunchSpec{
		RunID:   "run-1",
		JobID:   "job-1",
		Agent:   domain.AgentClaude,
		Workdir: "/tmp/repo",
		Goal:    "Fix the flaky test",
	}

	_, err := l.Launch(context.Background(), spec)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if len(runner.calls) != 4 {
		t.Fatalf("expected 4 tmux calls without handshake, got %d: %v", len(runner.calls), runner.summaries())
	}
	if strings.Contains(runner.calls[2], "Enter") {
		t.Fatalf("expected no bootstrap Enter when handshake disabled, got %q", runner.calls[2])
	}
	if !strings.Contains(runner.calls[2], spec.Goal) || !strings.Contains(runner.calls[3], "Enter") {
		t.Fatalf("expected goal then Enter without handshake, got %v", runner.summaries())
	}
}

func TestLaunchInjectionFailureCleansUpSession(t *testing.T) {
	runner := &launcherTestRunner{failContains: "send-keys -t triptych-run-1:main -l Fix the flaky test"}
	l := Launcher{runner: runner, Mode: LaunchModeReal, Config: LaunchConfig{Claude: ClaudeLaunchConfig{StartupHandshake: true}}}

	spec := LaunchSpec{
		RunID:   "run-1",
		JobID:   "job-1",
		Agent:   domain.AgentClaude,
		Workdir: "/tmp/repo",
		Goal:    "Fix the flaky test",
	}

	_, err := l.Launch(context.Background(), spec)
	if err == nil {
		t.Fatal("expected Launch() to fail")
	}
	if !strings.Contains(err.Error(), "inject goal") {
		t.Fatalf("expected inject-goal error, got %v", err)
	}
	if len(runner.calls) != 5 {
		t.Fatalf("expected 5 tmux calls including cleanup, got %d: %v", len(runner.calls), runner.summaries())
	}
	if !strings.Contains(runner.calls[4], "kill-session") || !strings.Contains(runner.calls[4], "=triptych-run-1") {
		t.Fatalf("expected cleanup kill-session call, got %q", runner.calls[4])
	}
}

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"run-1", "run-1"},
		{"RUN-1", "run-1"},
		{"", "run"},
		{"---", "run"},
		{"a.b.c", "a-b-c"},
	}
	for _, tc := range tests {
		got := sanitizeName(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestSessionNameForRun(t *testing.T) {
	got := SessionNameForRun("run-abc-123")
	want := "triptych-run-abc-123"
	if got != want {
		t.Fatalf("SessionNameForRun() = %q, want %q", got, want)
	}
}

type launcherTestRunner struct {
	calls            []string
	hasSessionExists bool
	failContains     string
	failErr          error
}

func (r *launcherTestRunner) Run(_ context.Context, name string, args ...string) error {
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if len(args) > 0 && args[0] == "has-session" && !r.hasSessionExists {
		return exitError1()
	}
	if r.failContains != "" && strings.Contains(call, r.failContains) {
		if r.failErr != nil {
			return r.failErr
		}
		return errors.New("forced failure")
	}
	return nil
}

func (r *launcherTestRunner) summaries() []string {
	return r.calls
}

func exitError1() *exec.ExitError {
	err := exec.Command("sh", "-c", "exit 1").Run()
	return err.(*exec.ExitError)
}
