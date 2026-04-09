// Package client provides a lightweight HTTP client for the Triptych server API.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Envelope mirrors the server's JSON response shape.
type Envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *ErrorBody      `json:"error,omitempty"`
}

// ErrorBody is the error detail from the server.
type ErrorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

func (e *ErrorBody) String() string {
	if len(e.Details) == 0 {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	parts := make([]string, 0, len(e.Details))
	for k, v := range e.Details {
		parts = append(parts, k+"="+v)
	}
	return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, strings.Join(parts, ", "))
}

// Client talks to the Triptych control-plane server.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// New returns a Client pointing at the given base URL.
func New(baseURL string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: http.DefaultClient,
	}
}

// Get performs a GET request to the given path and returns the raw data payload.
func (c *Client) Get(path string) (json.RawMessage, error) {
	return c.doJSON(http.MethodGet, path, nil)
}

// Post performs a POST request to the given path and returns the raw data payload.
func (c *Client) Post(path string, body any) (json.RawMessage, error) {
	return c.doJSON(http.MethodPost, path, body)
}

func (c *Client) doJSON(method, path string, body any) (json.RawMessage, error) {
	url := c.BaseURL + path

	var bodyReader io.Reader
	if body != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			return nil, fmt.Errorf("encoding request: %w", err)
		}
		bodyReader = buf
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	var env Envelope
	if err := json.Unmarshal(respBody, &env); err != nil {
		// Non-JSON response (e.g. connection refused proxy page).
		return nil, fmt.Errorf("server returned non-JSON (HTTP %d): %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("server error (HTTP %d): %s", resp.StatusCode, env.Error.String())
		}
		return nil, fmt.Errorf("server error (HTTP %d): unknown error", resp.StatusCode)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected HTTP %d with ok=true", resp.StatusCode)
	}

	return env.Data, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
