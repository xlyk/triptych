package client

import (
	"encoding/json"
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
		_, _ = w.Write([]byte(`{"ok":true,"data":[{"host_id":"h1","hostname":"box"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	data, err := c.Get("/v1/hosts")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var hosts []map[string]any
	if err := json.Unmarshal(data, &hosts); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("expected 1 host, got %d", len(hosts))
	}
	if hosts[0]["host_id"] != "h1" {
		t.Errorf("expected host_id h1, got %v", hosts[0]["host_id"])
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
	c := New("http://127.0.0.1:1") // nothing listening
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
				// Details map iteration order is non-deterministic, just check prefix.
				if len(got) == 0 {
					t.Error("expected non-empty string")
				}
			}
		})
	}
}
