package daemon

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

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
