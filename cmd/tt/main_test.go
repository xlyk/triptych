package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// fakeServer returns an httptest.Server that serves canned responses.
func fakeServer(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"not_found","message":"not found"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func runCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, code int) {
	t.Helper()

	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()

	t.Setenv("TRIPTYCH_SERVER_URL", serverURL)

	code = run(args, outW, errW)

	if err := outW.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	if err := errW.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}

	outBuf := make([]byte, 8192)
	n, _ := outR.Read(outBuf)
	stdout = string(outBuf[:n])

	errBuf := make([]byte, 8192)
	n, _ = errR.Read(errBuf)
	stderr = string(errBuf[:n])

	return stdout, stderr, code
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
	srv := fakeServer(t, map[string]string{
		"GET /v1/hosts": `{"ok":true,"data":[{"host_id":"h-abc","hostname":"dev-box","online":true,"health":"online"}]}`,
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
	srv := fakeServer(t, map[string]string{
		"GET /v1/hosts/h-abc": `{"ok":true,"data":{"host_id":"h-abc","hostname":"dev-box","online":true,"health":"online"}}`,
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "hosts", "get", "h-abc")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "h-abc") {
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
	srv := fakeServer(t, map[string]string{
		"GET /v1/jobs": `{"ok":true,"data":[{"job":{"job_id":"j-1","host_id":"h-1","agent":"claude","status":"running","goal":"fix bug","repo_path":"/repo"},"host_health":"online"}]}`,
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
	srv := fakeServer(t, map[string]string{
		"GET /v1/jobs/j-1": `{"ok":true,"data":{"job":{"job":{"job_id":"j-1","host_id":"h-1","agent":"claude","status":"queued","goal":"refactor","repo_path":"/repo"},"host_health":"online"}}}`,
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "jobs", "get", "j-1")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Job:      j-1") {
		t.Errorf("expected formatted job id: %s", stdout)
	}
	if !strings.Contains(stdout, "Status:   queued") {
		t.Errorf("expected formatted status: %s", stdout)
	}
	if !strings.Contains(stdout, "Goal:     refactor") {
		t.Errorf("expected formatted goal: %s", stdout)
	}
}

func TestJobsTail(t *testing.T) {
	srv := fakeServer(t, map[string]string{
		"GET /v1/jobs/j-1/tail": `{"ok":true,"data":{"job_id":"j-1","snapshot":{"output":"hello world\n","line_count":1,"stale":false}}}`,
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "jobs", "tail", "j-1")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "hello world") {
		t.Errorf("expected output text: %s", stdout)
	}
}

func TestJobsAttach(t *testing.T) {
	srv := fakeServer(t, map[string]string{
		"GET /v1/jobs/j-1/attach": `{"ok":true,"data":{"job_id":"j-1","host_id":"h-1","tmux":{"session_name":"triptych-j-1","window_name":"main"},"attach":{"ssh_target":"h-1","command":"ssh h-1 -t tmux attach -t triptych-j-1"}}}`,
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "jobs", "attach", "j-1")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "triptych-j-1") {
		t.Errorf("expected tmux session: %s", stdout)
	}
	if !strings.Contains(stdout, "ssh") {
		t.Errorf("expected ssh command: %s", stdout)
	}
}

func TestJSONFlag(t *testing.T) {
	srv := fakeServer(t, map[string]string{
		"GET /v1/hosts": `{"ok":true,"data":[{"host_id":"h-1","hostname":"box"}]}`,
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "--json", "hosts", "list")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}

	// Should be valid, pretty-printed JSON.
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &v); err != nil {
		t.Errorf("expected valid JSON: %v\noutput: %s", err, stdout)
	}
	if !strings.Contains(stdout, "  ") {
		t.Errorf("expected indented JSON: %s", stdout)
	}
}

func TestJSONFlag_PositionAfterCommand(t *testing.T) {
	srv := fakeServer(t, map[string]string{
		"GET /v1/hosts": `{"ok":true,"data":[]}`,
	})
	defer srv.Close()

	stdout, _, code := runCLI(t, srv.URL, "hosts", "list", "--json")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// With --json, empty array should still be valid JSON.
	trimmed := strings.TrimSpace(stdout)
	if trimmed != "[]" {
		t.Errorf("expected [], got: %s", trimmed)
	}
}

func TestServerError(t *testing.T) {
	srv := fakeServer(t, map[string]string{}) // no routes, everything 404
	defer srv.Close()

	_, stderr, code := runCLI(t, srv.URL, "hosts", "get", "no-such")
	if code != 1 {
		t.Errorf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr, "error") {
		t.Errorf("expected error in stderr: %s", stderr)
	}
}

func TestEnvVarDefault(t *testing.T) {
	// Unset the env var; run should use default URL and fail to connect.
	t.Setenv("TRIPTYCH_SERVER_URL", "")
	_, stderr, code := runCLI(t, "", "hosts", "list")
	if code != 1 {
		t.Errorf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr, "error") {
		t.Errorf("expected connection error: %s", stderr)
	}
}
