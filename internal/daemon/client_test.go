package daemon

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xlyk/triptych/internal/domain"
)

func TestHTTPClientRegisterHost(t *testing.T) {
	var captured *http.Request
	var body []byte

	c := NewHTTPClient("http://example.test", &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			captured = req
			var err error
			body, err = io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			return jsonResponse(http.StatusOK, `{"ok":true,"data":{"host":{"host_id":"host-1"}}}`), nil
		}),
	})

	err := c.RegisterHost(context.Background(), HostRegistration{
		HostID:           "host-1",
		Hostname:         "mbp.local",
		Capabilities:     []string{"codex", "tmux"},
		AllowedRepoRoots: []string{"/tmp/repo"},
		Labels:           map[string]string{"env": "dev"},
	})
	if err != nil {
		t.Fatalf("RegisterHost() error = %v", err)
	}

	if captured == nil {
		t.Fatal("expected request")
	}
	if captured.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", captured.Method)
	}
	if captured.URL.String() != "http://example.test/v1/hosts/register" {
		t.Fatalf("url = %s", captured.URL.String())
	}
	if got := captured.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := string(body); !strings.Contains(got, `"host_id":"host-1"`) {
		t.Fatalf("body = %s", got)
	}
	if got := string(body); !strings.Contains(got, `"allowed_repo_roots":["/tmp/repo"]`) {
		t.Fatalf("body = %s", got)
	}
}

func TestHTTPClientHeartbeat(t *testing.T) {
	var captured *http.Request
	var body []byte

	c := NewHTTPClient("http://example.test/", &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			captured = req
			var err error
			if req.Body != nil {
				body, err = io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
			}
			return jsonResponse(http.StatusOK, `{"ok":true}`), nil
		}),
	})

	if err := c.Heartbeat(context.Background(), domain.HostID("host-1")); err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}

	if captured == nil {
		t.Fatal("expected request")
	}
	if captured.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", captured.Method)
	}
	if captured.URL.String() != "http://example.test/v1/hosts/host-1/heartbeat" {
		t.Fatalf("url = %s", captured.URL.String())
	}
	if len(body) != 0 {
		t.Fatalf("expected empty body, got %q", string(body))
	}
}

func TestHTTPClientServerError(t *testing.T) {
	c := NewHTTPClient("http://example.test", &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusConflict, `{"ok":false,"error":{"code":"conflict","message":"already exists"}}`), nil
		}),
	})

	err := c.RegisterHost(context.Background(), HostRegistration{HostID: "host-1", Hostname: "mbp.local"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("error = %v", err)
	}
}

func TestHTTPClientGetWork(t *testing.T) {
	var captured *http.Request

	c := NewHTTPClient("http://example.test", &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			captured = req
			return jsonResponse(http.StatusOK, `{"ok":true,"data":{"host_id":"host-1","launchable_jobs":[{"job_id":"job-1","run_id":"run-1","agent":"codex","repo_path":"/tmp/repo","workdir":"/tmp/repo","goal":"Fix it","priority":"normal","max_duration":"4h"}],"active_runs":[],"pending_commands":[]}}`), nil
		}),
	})

	work, err := c.GetWork(context.Background(), "host-1")
	if err != nil {
		t.Fatalf("GetWork() error = %v", err)
	}

	if captured == nil {
		t.Fatal("expected request")
	}
	if captured.Method != http.MethodGet {
		t.Fatalf("method = %s, want GET", captured.Method)
	}
	if captured.URL.String() != "http://example.test/v1/hosts/host-1/work" {
		t.Fatalf("url = %s", captured.URL.String())
	}
	if len(work.LaunchableJobs) != 1 {
		t.Fatalf("launchable_jobs len = %d, want 1", len(work.LaunchableJobs))
	}
	if work.LaunchableJobs[0].RunID != "run-1" {
		t.Fatalf("run_id = %q", work.LaunchableJobs[0].RunID)
	}
}

func TestHTTPClientUpdateRunState(t *testing.T) {
	var captured *http.Request
	var body []byte
	startedAt := time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)
	sessionName := "triptych-run-1"
	windowName := "main"

	c := NewHTTPClient("http://example.test", &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			captured = req
			var err error
			body, err = io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			return jsonResponse(http.StatusOK, `{"ok":true}`), nil
		}),
	})

	err := c.UpdateRunState(context.Background(), "run-1", RunStateUpdate{
		Status:          domain.RunStatusActive,
		TmuxSessionName: &sessionName,
		TmuxWindowName:  &windowName,
		StartedAt:       &startedAt,
	})
	if err != nil {
		t.Fatalf("UpdateRunState() error = %v", err)
	}

	if captured == nil {
		t.Fatal("expected request")
	}
	if captured.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", captured.Method)
	}
	if captured.URL.String() != "http://example.test/v1/runs/run-1/state" {
		t.Fatalf("url = %s", captured.URL.String())
	}
	payload := string(body)
	if !strings.Contains(payload, `"status":"active"`) {
		t.Fatalf("body = %s", payload)
	}
	if !strings.Contains(payload, `"tmux_session_name":"triptych-run-1"`) {
		t.Fatalf("body = %s", payload)
	}
	if !strings.Contains(payload, `"started_at":"2026-04-08T12:00:00Z"`) {
		t.Fatalf("body = %s", payload)
	}
}

func TestHTTPClientAckCommand(t *testing.T) {
	var captured *http.Request

	c := NewHTTPClient("http://example.test", &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			captured = req
			return jsonResponse(http.StatusOK, `{"ok":true}`), nil
		}),
	})

	if err := c.AckCommand(context.Background(), "cmd-abc123"); err != nil {
		t.Fatalf("AckCommand() error = %v", err)
	}

	if captured == nil {
		t.Fatal("expected request")
	}
	if captured.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", captured.Method)
	}
	if captured.URL.String() != "http://example.test/v1/commands/cmd-abc123/ack" {
		t.Fatalf("url = %s", captured.URL.String())
	}
}

func TestHTTPClientUploadSnapshot(t *testing.T) {
	var captured *http.Request
	var body []byte

	c := NewHTTPClient("http://example.test", &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			captured = req
			var err error
			body, err = io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			return jsonResponse(http.StatusOK, `{"ok":true,"data":{"snapshot":{"run_id":"run-1"}}}`), nil
		}),
	})

	capturedAt := time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)
	err := c.UploadSnapshot(context.Background(), "run-1", SnapshotUpload{
		HostID:     "host-1",
		CapturedAt: capturedAt,
		LineCount:  5,
		Stale:      false,
		Output:     "line1\nline2\nline3\nline4\nline5",
	})
	if err != nil {
		t.Fatalf("UploadSnapshot() error = %v", err)
	}

	if captured == nil {
		t.Fatal("expected request")
	}
	if captured.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", captured.Method)
	}
	if captured.URL.String() != "http://example.test/v1/runs/run-1/snapshot" {
		t.Fatalf("url = %s", captured.URL.String())
	}
	payload := string(body)
	if !strings.Contains(payload, `"host_id":"host-1"`) {
		t.Fatalf("body = %s", payload)
	}
	if !strings.Contains(payload, `"line_count":5`) {
		t.Fatalf("body = %s", payload)
	}
	if !strings.Contains(payload, `"output":"line1\nline2\nline3\nline4\nline5"`) {
		t.Fatalf("body = %s", payload)
	}
}

func TestHTTPClientObserveCommand(t *testing.T) {
	var captured *http.Request

	c := NewHTTPClient("http://example.test", &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			captured = req
			return jsonResponse(http.StatusOK, `{"ok":true}`), nil
		}),
	})

	if err := c.ObserveCommand(context.Background(), "cmd-abc123"); err != nil {
		t.Fatalf("ObserveCommand() error = %v", err)
	}

	if captured == nil {
		t.Fatal("expected request")
	}
	if captured.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", captured.Method)
	}
	if captured.URL.String() != "http://example.test/v1/commands/cmd-abc123/observe" {
		t.Fatalf("url = %s", captured.URL.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(bytes.NewBufferString(body)),
	}
}
