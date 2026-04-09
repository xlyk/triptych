package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGet_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/hosts" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"data":{"hosts":[{"host_id":"h1","hostname":"box"}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	data, err := c.Get("/v1/hosts")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp struct {
		Hosts []map[string]any `json:"hosts"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Hosts) != 1 {
		t.Fatalf("expected 1 host, got %d", len(resp.Hosts))
	}
	if resp.Hosts[0]["host_id"] != "h1" {
		t.Errorf("expected host_id h1, got %v", resp.Hosts[0]["host_id"])
	}
}

func TestPost_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/jobs/job-1/commands/send" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content-type = %q, want application/json", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		if req["text"] != "continue" {
			t.Errorf("text = %v, want continue", req["text"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true,"data":{"command":{"command_id":"cmd-1"}}}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	data, err := c.Post("/v1/jobs/job-1/commands/send", map[string]any{"text": "continue"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp struct {
		Command map[string]any `json:"command"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Command["command_id"] != "cmd-1" {
		t.Fatalf("command_id = %v, want cmd-1", resp.Command["command_id"])
	}
}

func TestPost_EmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if len(body) != 0 {
			t.Fatalf("expected empty body, got %q", string(body))
		}
		if got := r.Header.Get("Content-Type"); got != "" {
			t.Fatalf("content-type = %q, want empty", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true,"data":{"command":{"command_id":"cmd-2"}}}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	if _, err := c.Post("/v1/jobs/job-1/commands/interrupt", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGet_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"not_found","message":"host not found"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.Get("/v1/hosts/missing")
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got == "" {
		t.Error("expected non-empty error message")
	}
}

func TestGet_NonJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.Get("/v1/hosts")
	if err == nil {
		t.Fatal("expected error for non-JSON response")
	}
}

func TestGet_ConnectionRefused(t *testing.T) {
	c := New("http://127.0.0.1:1")
	_, err := c.Get("/v1/hosts")
	if err == nil {
		t.Fatal("expected error for connection refused")
	}
}

func TestErrorBody_String(t *testing.T) {
	tests := []struct {
		name string
		eb   ErrorBody
		want string
	}{
		{
			name: "no details",
			eb:   ErrorBody{Code: "not_found", Message: "host not found"},
			want: "not_found: host not found",
		},
		{
			name: "with details",
			eb:   ErrorBody{Code: "invalid_argument", Message: "bad input", Details: map[string]string{"goal": "required"}},
			want: "invalid_argument: bad input (goal=required)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.eb.String()
			if tt.eb.Details == nil {
				if got != tt.want {
					t.Errorf("got %q, want %q", got, tt.want)
				}
			} else {
				if len(got) == 0 {
					t.Error("expected non-empty string")
				}
			}
		})
	}
}
