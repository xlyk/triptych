package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xlyk/triptych/internal/domain"
	triptychtmux "github.com/xlyk/triptych/internal/tmux"
)

const (
	DefaultServerURL         = "http://127.0.0.1:8080"
	DefaultHeartbeatInterval = 15 * time.Second
)

type Config struct {
	ServerURL         string
	HostID            domain.HostID
	Hostname          string
	Capabilities      []string
	AllowedRepoRoots  []string
	Labels            map[string]string
	StateDir          string
	HeartbeatInterval time.Duration
	LaunchMode        triptychtmux.LaunchMode
	Launch            triptychtmux.LaunchConfig
}

func LoadConfig() (Config, error) {
	return LoadConfigFromEnv(os.Getenv, os.Hostname)
}

func LoadConfigFromEnv(getenv func(string) string, hostname func() (string, error)) (Config, error) {
	cfg := Config{
		ServerURL:         DefaultServerURL,
		Labels:            map[string]string{},
		StateDir:          defaultStateDir(getenv),
		HeartbeatInterval: DefaultHeartbeatInterval,
	}

	if raw := strings.TrimSpace(getenv("TRIPTYCH_SERVER_URL")); raw != "" {
		cfg.ServerURL = strings.TrimRight(raw, "/")
	}

	cfg.HostID = domain.HostID(strings.TrimSpace(getenv("TRIPTYCH_HOST_ID")))
	if err := cfg.HostID.Validate(); err != nil {
		return Config{}, err
	}

	if raw := strings.TrimSpace(getenv("TRIPTYCH_HOSTNAME")); raw != "" {
		cfg.Hostname = raw
	} else {
		name, err := hostname()
		if err != nil {
			return Config{}, fmt.Errorf("hostname: %w", err)
		}
		if strings.TrimSpace(name) == "" {
			return Config{}, fmt.Errorf("hostname is empty")
		}
		cfg.Hostname = name
	}

	cfg.Capabilities = splitCSV(getenv("TRIPTYCH_CAPABILITIES"))
	cfg.AllowedRepoRoots = splitCSV(getenv("TRIPTYCH_ALLOWED_REPO_ROOTS"))
	for _, root := range cfg.AllowedRepoRoots {
		if !filepath.IsAbs(root) {
			return Config{}, fmt.Errorf("TRIPTYCH_ALLOWED_REPO_ROOTS entries must be absolute: %q", root)
		}
	}

	labels, err := parseLabels(getenv("TRIPTYCH_LABELS"))
	if err != nil {
		return Config{}, err
	}
	cfg.Labels = labels

	if raw := strings.TrimSpace(getenv("TRIPTYCH_STATE_DIR")); raw != "" {
		cfg.StateDir = filepath.Clean(raw)
	}

	if raw := strings.TrimSpace(getenv("TRIPTYCH_HEARTBEAT_INTERVAL")); raw != "" {
		interval, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("TRIPTYCH_HEARTBEAT_INTERVAL: %w", err)
		}
		if interval <= 0 {
			return Config{}, fmt.Errorf("TRIPTYCH_HEARTBEAT_INTERVAL must be positive")
		}
		cfg.HeartbeatInterval = interval
	}

	if raw := strings.TrimSpace(getenv("TRIPTYCH_LAUNCH_MODE")); raw != "" {
		switch triptychtmux.LaunchMode(raw) {
		case triptychtmux.LaunchModeReal, triptychtmux.LaunchModePlaceholder:
			cfg.LaunchMode = triptychtmux.LaunchMode(raw)
		default:
			return Config{}, fmt.Errorf("TRIPTYCH_LAUNCH_MODE must be \"\" (real) or \"placeholder\", got %q", raw)
		}
	}

	claudeSettingsFile := strings.TrimSpace(getenv("TRIPTYCH_CLAUDE_SETTINGS_FILE"))
	if claudeSettingsFile != "" {
		if !filepath.IsAbs(claudeSettingsFile) {
			return Config{}, fmt.Errorf("TRIPTYCH_CLAUDE_SETTINGS_FILE must be absolute: %q", claudeSettingsFile)
		}
		cfg.Launch.Claude.SettingsFile = filepath.Clean(claudeSettingsFile)
	}
	cfg.Launch.Claude.SettingsJSON = strings.TrimSpace(getenv("TRIPTYCH_CLAUDE_SETTINGS_JSON"))
	if cfg.Launch.Claude.SettingsFile != "" && cfg.Launch.Claude.SettingsJSON != "" {
		return Config{}, fmt.Errorf("TRIPTYCH_CLAUDE_SETTINGS_FILE and TRIPTYCH_CLAUDE_SETTINGS_JSON are mutually exclusive")
	}
	if cfg.Launch.Claude.SettingsJSON != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(cfg.Launch.Claude.SettingsJSON), &parsed); err != nil {
			return Config{}, fmt.Errorf("TRIPTYCH_CLAUDE_SETTINGS_JSON must be valid JSON: %w", err)
		}
	}
	cfg.Launch.Claude.TrustedDirectories = splitCSV(getenv("TRIPTYCH_CLAUDE_TRUSTED_DIRECTORIES"))
	for _, dir := range cfg.Launch.Claude.TrustedDirectories {
		if !filepath.IsAbs(dir) {
			return Config{}, fmt.Errorf("TRIPTYCH_CLAUDE_TRUSTED_DIRECTORIES entries must be absolute: %q", dir)
		}
	}
	if cfg.Launch.Claude.SettingsFile != "" && len(cfg.Launch.Claude.TrustedDirectories) > 0 {
		return Config{}, fmt.Errorf("TRIPTYCH_CLAUDE_TRUSTED_DIRECTORIES cannot be combined with TRIPTYCH_CLAUDE_SETTINGS_FILE; put trustedDirectories in the settings file instead")
	}
	cfg.Launch.Claude.PermissionMode = strings.TrimSpace(getenv("TRIPTYCH_CLAUDE_PERMISSION_MODE"))
	if raw := strings.TrimSpace(getenv("TRIPTYCH_CLAUDE_STARTUP_HANDSHAKE")); raw != "" {
		startupHandshake, err := parseBoolEnv(raw)
		if err != nil {
			return Config{}, fmt.Errorf("TRIPTYCH_CLAUDE_STARTUP_HANDSHAKE: %w", err)
		}
		cfg.Launch.Claude.StartupHandshake = startupHandshake
	}

	cfg.Launch.Codex.ConfigProfile = strings.TrimSpace(getenv("TRIPTYCH_CODEX_CONFIG_PROFILE"))
	cfg.Launch.Codex.ApprovalPolicy = strings.TrimSpace(getenv("TRIPTYCH_CODEX_APPROVAL_POLICY"))
	cfg.Launch.Codex.SandboxMode = strings.TrimSpace(getenv("TRIPTYCH_CODEX_SANDBOX_MODE"))
	if raw := strings.TrimSpace(getenv("TRIPTYCH_CODEX_TRUST_PROJECT")); raw != "" {
		trustProject, err := parseBoolEnv(raw)
		if err != nil {
			return Config{}, fmt.Errorf("TRIPTYCH_CODEX_TRUST_PROJECT: %w", err)
		}
		cfg.Launch.Codex.TrustProject = trustProject
	}

	return cfg, nil
}

func defaultStateDir(getenv func(string) string) string {
	home := strings.TrimSpace(getenv("HOME"))
	if home == "" {
		return filepath.Clean(".triptych")
	}
	return filepath.Join(home, ".triptych")
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseLabels(raw string) (map[string]string, error) {
	labels := map[string]string{}
	for _, part := range splitCSV(raw) {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("TRIPTYCH_LABELS entry must be key=value: %q", part)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			return nil, fmt.Errorf("TRIPTYCH_LABELS key must be non-empty")
		}
		labels[key] = value
	}
	return labels, nil
}

func parseBoolEnv(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("must be a boolean value")
	}
}
