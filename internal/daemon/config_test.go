package daemon

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigFromEnv(t *testing.T) {
	t.Run("defaults and parsing", func(t *testing.T) {
		env := map[string]string{
			"HOME":                        "/tmp/home",
			"TRIPTYCH_HOST_ID":            "host-1",
			"TRIPTYCH_CAPABILITIES":       "codex, tmux ,",
			"TRIPTYCH_ALLOWED_REPO_ROOTS": "/tmp/repo1, /tmp/repo2",
			"TRIPTYCH_LABELS":             "env=dev,region=local",
			"TRIPTYCH_HEARTBEAT_INTERVAL": "30s",
		}

		cfg, err := LoadConfigFromEnv(func(key string) string { return env[key] }, func() (string, error) {
			return "host.local", nil
		})
		if err != nil {
			t.Fatalf("LoadConfigFromEnv() error = %v", err)
		}

		if cfg.ServerURL != DefaultServerURL {
			t.Fatalf("ServerURL = %q, want %q", cfg.ServerURL, DefaultServerURL)
		}
		if cfg.HostID != "host-1" {
			t.Fatalf("HostID = %q, want %q", cfg.HostID, "host-1")
		}
		if cfg.Hostname != "host.local" {
			t.Fatalf("Hostname = %q, want %q", cfg.Hostname, "host.local")
		}
		if !reflect.DeepEqual(cfg.Capabilities, []string{"codex", "tmux"}) {
			t.Fatalf("Capabilities = %#v", cfg.Capabilities)
		}
		if !reflect.DeepEqual(cfg.AllowedRepoRoots, []string{"/tmp/repo1", "/tmp/repo2"}) {
			t.Fatalf("AllowedRepoRoots = %#v", cfg.AllowedRepoRoots)
		}
		if !reflect.DeepEqual(cfg.Labels, map[string]string{"env": "dev", "region": "local"}) {
			t.Fatalf("Labels = %#v", cfg.Labels)
		}
		if cfg.StateDir != "/tmp/home/.triptych" {
			t.Fatalf("StateDir = %q, want %q", cfg.StateDir, "/tmp/home/.triptych")
		}
		if cfg.HeartbeatInterval != 30*time.Second {
			t.Fatalf("HeartbeatInterval = %s, want %s", cfg.HeartbeatInterval, 30*time.Second)
		}
	})

	t.Run("explicit hostname and server URL trim slash", func(t *testing.T) {
		env := map[string]string{
			"TRIPTYCH_SERVER_URL": "http://example.test:8080/",
			"TRIPTYCH_HOST_ID":    "host-2",
			"TRIPTYCH_HOSTNAME":   "agent-box",
			"TRIPTYCH_STATE_DIR":  "/var/lib/triptych",
		}

		cfg, err := LoadConfigFromEnv(func(key string) string { return env[key] }, func() (string, error) {
			t.Fatal("hostname fallback should not be called")
			return "", nil
		})
		if err != nil {
			t.Fatalf("LoadConfigFromEnv() error = %v", err)
		}
		if cfg.ServerURL != "http://example.test:8080" {
			t.Fatalf("ServerURL = %q", cfg.ServerURL)
		}
		if cfg.Hostname != "agent-box" {
			t.Fatalf("Hostname = %q", cfg.Hostname)
		}
		if cfg.StateDir != "/var/lib/triptych" {
			t.Fatalf("StateDir = %q", cfg.StateDir)
		}
		if cfg.HeartbeatInterval != DefaultHeartbeatInterval {
			t.Fatalf("HeartbeatInterval = %s, want %s", cfg.HeartbeatInterval, DefaultHeartbeatInterval)
		}
	})
}

func TestLoadConfigFromEnvErrors(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		hostFn  func() (string, error)
		wantErr string
	}{
		{
			name:    "missing host id",
			env:     map[string]string{},
			hostFn:  func() (string, error) { return "host.local", nil },
			wantErr: "host_id is required",
		},
		{
			name: "hostname lookup failure",
			env: map[string]string{
				"TRIPTYCH_HOST_ID": "host-1",
			},
			hostFn:  func() (string, error) { return "", errors.New("boom") },
			wantErr: "hostname: boom",
		},
		{
			name: "relative repo root",
			env: map[string]string{
				"TRIPTYCH_HOST_ID":            "host-1",
				"TRIPTYCH_ALLOWED_REPO_ROOTS": "relative/path",
			},
			hostFn:  func() (string, error) { return "host.local", nil },
			wantErr: "TRIPTYCH_ALLOWED_REPO_ROOTS entries must be absolute",
		},
		{
			name: "bad labels",
			env: map[string]string{
				"TRIPTYCH_HOST_ID": "host-1",
				"TRIPTYCH_LABELS":  "env",
			},
			hostFn:  func() (string, error) { return "host.local", nil },
			wantErr: "TRIPTYCH_LABELS entry must be key=value",
		},
		{
			name: "bad heartbeat interval",
			env: map[string]string{
				"TRIPTYCH_HOST_ID":            "host-1",
				"TRIPTYCH_HEARTBEAT_INTERVAL": "nope",
			},
			hostFn:  func() (string, error) { return "host.local", nil },
			wantErr: "TRIPTYCH_HEARTBEAT_INTERVAL",
		},
		{
			name: "non-positive heartbeat interval",
			env: map[string]string{
				"TRIPTYCH_HOST_ID":            "host-1",
				"TRIPTYCH_HEARTBEAT_INTERVAL": "0s",
			},
			hostFn:  func() (string, error) { return "host.local", nil },
			wantErr: "TRIPTYCH_HEARTBEAT_INTERVAL must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfigFromEnv(func(key string) string { return tt.env[key] }, tt.hostFn)
			if err == nil {
				t.Fatal("expected error")
			}
			if got := err.Error(); got == "" || !contains(got, tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", got, tt.wantErr)
			}
		})
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
