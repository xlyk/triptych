package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xlyk/triptych/internal/domain"
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
