package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeRoute struct {
	status int
	body   string
	check  func(*testing.T, *http.Request, []byte)
}

func fakeServer(t *testing.T, routes map[string]fakeRoute) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		route, ok := routes[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"not_found","message":"not found"}}`))
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if route.check != nil {
			route.check(t, r, body)
		}
		w.Header().Set("Content-Type", "application/json")
		if route.status != 0 {
			w.WriteHeader(route.status)
		}
		_, _ = w.Write([]byte(route.body))
	}))
}

func runCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, code int) {
	t.Helper()

	var out strings.Builder
	var err strings.Builder
	if serverURL != "" {
		t.Setenv("TRIPTYCH_SERVER_URL", serverURL)
	}

	code = run(args, &out, &err)
	return out.String(), err.String(), code
}

func TestUsage_NoArgs(t *testing.T) {
	_, stderr, code := runCLI(t, "http://unused")
	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("expected usage in stderr, got: %s", stderr)
	}
}

func TestUsage_UnknownCommand(t *testing.T) {
	_, stderr, code := runCLI(t, "http://unused", "widgets", "list")
	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("expected usage in stderr, got: %s", stderr)
	}
}

func TestHostsList(t *testing.T) {
	srv := fakeServer(t, map[string]fakeRoute{
		"GET /v1/hosts": {
			body: `{"ok":true,"data":{"hosts":[{"host_id":"h-abc","hostname":"dev-box","online":true,"health":"online"}]}}`,
		},
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "hosts", "list")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "h-abc") {
		t.Errorf("expected host id in output: %s", stdout)
	}
	if !strings.Contains(stdout, "dev-box") {
		t.Errorf("expected hostname in output: %s", stdout)
	}
}

func TestHostsGet(t *testing.T) {
	srv := fakeServer(t, map[string]fakeRoute{
		"GET /v1/hosts/h-abc": {
			body: `{"ok":true,"data":{"host":{"host_id":"h-abc","hostname":"dev-box","online":true,"health":"online"}}}`,
		},
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "hosts", "get", "h-abc")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Host:     h-abc") {
		t.Errorf("expected host id in output: %s", stdout)
	}
}

func TestHostsGet_MissingArg(t *testing.T) {
	_, stderr, code := runCLI(t, "http://unused", "hosts", "get")
	if code != 1 {
		t.Errorf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr, "usage:") {
		t.Errorf("expected usage hint: %s", stderr)
	}
}

func TestJobsList(t *testing.T) {
	srv := fakeServer(t, map[string]fakeRoute{
		"GET /v1/jobs": {
			body: `{"ok":true,"data":{"jobs":[{"job":{"job_id":"j-1","host_id":"h-1","agent":"claude","status":"running","goal":"fix bug","repo_path":"/repo"},"host_health":"online"}]}}`,
		},
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "jobs", "list")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "j-1") {
		t.Errorf("expected job id: %s", stdout)
	}
	if !strings.Contains(stdout, "running") {
		t.Errorf("expected status: %s", stdout)
	}
}

func TestJobsGet(t *testing.T) {
	srv := fakeServer(t, map[string]fakeRoute{
		"GET /v1/jobs/j-1": {
			body: `{"ok":true,"data":{"job":{"job":{"job_id":"j-1","host_id":"h-1","agent":"claude","status":"queued","goal":"refactor","repo_path":"/repo"},"run":{"run_id":"run-1","status":"pending_launch"},"host_health":"online"}}}`,
		},
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "jobs", "get", "j-1")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Job:      j-1") {
		t.Errorf("expected formatted job id: %s", stdout)
	}
	if !strings.Contains(stdout, "Run:      run-1 (pending_launch)") {
		t.Errorf("expected formatted run: %s", stdout)
	}
	if !strings.Contains(stdout, "Goal:     refactor") {
		t.Errorf("expected formatted goal: %s", stdout)
	}
}

func TestJobsTail(t *testing.T) {
	srv := fakeServer(t, map[string]fakeRoute{
		"GET /v1/jobs/j-1/tail": {
			body: `{"ok":true,"data":{"job_id":"j-1","snapshot":{"run_id":"run-1","host_id":"host-1","captured_at":"2026-04-09T18:00:00Z","updated_at":"2026-04-09T18:00:02Z","output":"hello world\n","line_count":1,"stale":false}}}`,
		},
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "jobs", "tail", "j-1")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	for _, want := range []string{
		"Job:      j-1",
		"Run:      run-1",
		"Host:     host-1",
		"Snapshot: fresh, 1 lines",
		"Captured: 2026-04-09T18:00:00Z",
		"Updated:  2026-04-09T18:00:02Z",
		"--- tail ---",
		"hello world",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("expected %q in output: %s", want, stdout)
		}
	}
}

func TestJobsTail_StaleSnapshot(t *testing.T) {
	srv := fakeServer(t, map[string]fakeRoute{
		"GET /v1/jobs/j-1/tail": {
			body: `{"ok":true,"data":{"job_id":"j-1","snapshot":{"run_id":"run-1","host_id":"host-1","line_count":37,"stale":true,"output":"old line\n"}}}`,
		},
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "jobs", "tail", "j-1")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	for _, want := range []string{
		"Snapshot: stale, 37 lines",
		"--- tail ---",
		"old line",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("expected %q in output: %s", want, stdout)
		}
	}
}

func TestJobsTail_NoOutput(t *testing.T) {
	srv := fakeServer(t, map[string]fakeRoute{
		"GET /v1/jobs/j-1/tail": {
			body: `{"ok":true,"data":{"job_id":"j-1","snapshot":{"run_id":"run-1","host_id":"host-1","line_count":0,"stale":false,"output":""}}}`,
		},
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "jobs", "tail", "j-1")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	for _, want := range []string{
		"Job:      j-1",
		"Snapshot: fresh, 0 lines",
		"--- tail ---",
		"(no output)",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("expected %q in output: %s", want, stdout)
		}
	}
}

func TestJobsAttach(t *testing.T) {
	srv := fakeServer(t, map[string]fakeRoute{
		"GET /v1/jobs/j-1/attach": {
			body: `{"ok":true,"data":{"job_id":"j-1","host_id":"h-1","tmux":{"session_name":"triptych-j-1","window_name":"main"},"attach":{"ssh_target":"h-1","command":"ssh h-1 -t tmux attach -t triptych-j-1"}}}`,
		},
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "jobs", "attach", "j-1")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	for _, want := range []string{
		"Job:      j-1",
		"Host:     h-1",
		"SSH:      h-1",
		"Session:  triptych-j-1",
		"Window:   main",
		"Check snapshot:",
		"tt jobs tail j-1",
		"Attach live session:",
		"ssh h-1 -t tmux attach -t triptych-j-1",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("expected %q in output: %s", want, stdout)
		}
	}
}

func TestJobsCreate(t *testing.T) {
	srv := fakeServer(t, map[string]fakeRoute{
		"POST /v1/jobs": {
			status: http.StatusCreated,
			body:   `{"ok":true,"data":{"job":{"job_id":"job-1","host_id":"host-1","agent":"codex","status":"assigned","goal":"Fix it","repo_path":"/repo"},"run":{"run_id":"run-1","status":"pending_launch"}}}`,
			check: func(t *testing.T, r *http.Request, body []byte) {
				t.Helper()
				if got := r.Header.Get("Content-Type"); got != "application/json" {
					t.Fatalf("content-type = %q, want application/json", got)
				}
				var req map[string]any
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if req["host_id"] != "host-1" || req["agent"] != "codex" || req["repo_path"] != "/repo" || req["goal"] != "Fix it" {
					t.Fatalf("unexpected request payload: %v", req)
				}
				if req["workdir"] != "/repo/subdir" || req["priority"] != "high" || req["max_duration"] != "30m" || req["idempotency_key"] != "idem-1" {
					t.Fatalf("expected optional fields in request payload: %v", req)
				}
			},
		},
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL,
		"jobs", "create",
		"--host", "host-1",
		"--agent", "codex",
		"--repo", "/repo",
		"--goal", "Fix it",
		"--workdir", "/repo/subdir",
		"--priority", "high",
		"--max-duration", "30m",
		"--idempotency-key", "idem-1",
	)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Created job job-1 on host-1 (codex)") {
		t.Fatalf("unexpected output: %s", stdout)
	}
	if !strings.Contains(stdout, "Run: run-1 pending_launch") {
		t.Fatalf("unexpected output: %s", stdout)
	}
}

func TestJobsCreate_MissingRequiredFlags(t *testing.T) {
	_, stderr, code := runCLI(t, "http://unused", "jobs", "create", "--host", "host-1")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr, "usage: tt jobs create") {
		t.Fatalf("expected usage hint, got: %s", stderr)
	}
}

func TestJobsSend(t *testing.T) {
	srv := fakeServer(t, map[string]fakeRoute{
		"POST /v1/jobs/job-1/commands/send": {
			status: http.StatusCreated,
			body:   `{"ok":true,"data":{"command":{"command_id":"cmd-1","job_id":"job-1","command_type":"send","state":"recorded"}}}`,
			check: func(t *testing.T, _ *http.Request, body []byte) {
				t.Helper()
				var req map[string]any
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if req["text"] != "continue with the refactor" {
					t.Fatalf("text = %v, want joined text", req["text"])
				}
				if req["idempotency_key"] != "req-1" {
					t.Fatalf("idempotency_key = %v, want req-1", req["idempotency_key"])
				}
			},
		},
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "jobs", "send", "--idempotency-key", "req-1", "job-1", "continue", "with", "the", "refactor")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Queued send cmd-1 for job job-1 (recorded)") {
		t.Fatalf("unexpected output: %s", stdout)
	}
}

func TestJobsInterruptAndStop(t *testing.T) {
	srv := fakeServer(t, map[string]fakeRoute{
		"POST /v1/jobs/job-1/commands/interrupt": {
			status: http.StatusCreated,
			body:   `{"ok":true,"data":{"command":{"command_id":"cmd-int","job_id":"job-1","command_type":"interrupt","state":"recorded"}}}`,
		},
		"POST /v1/jobs/job-1/commands/stop": {
			status: http.StatusCreated,
			body:   `{"ok":true,"data":{"command":{"command_id":"cmd-stop","job_id":"job-1","command_type":"stop","state":"recorded"}}}`,
			check: func(t *testing.T, _ *http.Request, body []byte) {
				t.Helper()
				var req map[string]any
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if req["idempotency_key"] != "req-stop" {
					t.Fatalf("idempotency_key = %v, want req-stop", req["idempotency_key"])
				}
			},
		},
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "jobs", "interrupt", "job-1")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Queued interrupt cmd-int for job job-1 (recorded)") {
		t.Fatalf("unexpected interrupt output: %s", stdout)
	}

	stdout, _, code = runCLI(t, srv.URL, "jobs", "stop", "--idempotency-key=req-stop", "job-1")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Queued stop cmd-stop for job job-1 (recorded)") {
		t.Fatalf("unexpected stop output: %s", stdout)
	}
}

func TestJobsSend_MissingText(t *testing.T) {
	_, stderr, code := runCLI(t, "http://unused", "jobs", "send", "job-1")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr, "usage: tt jobs send") {
		t.Fatalf("expected usage hint, got: %s", stderr)
	}
}

func TestJSONFlag(t *testing.T) {
	srv := fakeServer(t, map[string]fakeRoute{
		"POST /v1/jobs/job-1/commands/stop": {
			status: http.StatusCreated,
			body:   `{"ok":true,"data":{"command":{"command_id":"cmd-1","job_id":"job-1","command_type":"stop","state":"recorded"}}}`,
		},
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "--json", "jobs", "stop", "job-1")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &v); err != nil {
		t.Errorf("expected valid JSON: %v\noutput: %s", err, stdout)
	}
	if !strings.Contains(stdout, "cmd-1") {
		t.Errorf("expected command payload in JSON output: %s", stdout)
	}
}

func TestJSONFlag_PositionAfterCommand(t *testing.T) {
	srv := fakeServer(t, map[string]fakeRoute{
		"GET /v1/hosts": {
			body: `{"ok":true,"data":{"hosts":[]}}`,
		},
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "hosts", "list", "--json")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	trimmed := strings.TrimSpace(stdout)
	if trimmed != "{\n  \"hosts\": []\n}" {
		t.Errorf("unexpected JSON output: %s", trimmed)
	}
}

func TestServerError(t *testing.T) {
	srv := fakeServer(t, map[string]fakeRoute{})
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "hosts", "get", "no-such")
	if code != 1 {
		t.Errorf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr, "error") {
		t.Errorf("expected error in stderr: %s", stderr)
	}
}

func TestServerURLEnvVarUsed(t *testing.T) {
	t.Setenv("TRIPTYCH_SERVER_URL", "http://127.0.0.1:1")
	_, stderr, code := runCLI(t, "", "hosts", "list")
	if code != 1 {
		t.Errorf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr, "error") {
		t.Errorf("expected connection error: %s", stderr)
	}
}
