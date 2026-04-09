package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xlyk/triptych/internal/domain"
)

type Client interface {
	RegisterHost(context.Context, HostRegistration) error
	Heartbeat(context.Context, domain.HostID) error
	GetWork(context.Context, domain.HostID) (*Work, error)
	UpdateRunState(context.Context, domain.RunID, RunStateUpdate) error
}

type HostRegistration struct {
	HostID           domain.HostID
	Hostname         string
	Capabilities     []string
	AllowedRepoRoots []string
	Labels           map[string]string
}

type Work struct {
	HostID          domain.HostID     `json:"host_id"`
	LaunchableJobs  []LaunchableJob   `json:"launchable_jobs"`
	ActiveRuns      []json.RawMessage `json:"active_runs"`
	PendingCommands []json.RawMessage `json:"pending_commands"`
}

type LaunchableJob struct {
	JobID       domain.JobID    `json:"job_id"`
	RunID       domain.RunID    `json:"run_id"`
	Agent       domain.Agent    `json:"agent"`
	RepoPath    string          `json:"repo_path"`
	Workdir     string          `json:"workdir"`
	Goal        string          `json:"goal"`
	Priority    domain.Priority `json:"priority"`
	MaxDuration string          `json:"max_duration"`
}

type RunStateUpdate struct {
	Status          domain.RunStatus `json:"status"`
	TmuxSessionName *string          `json:"tmux_session_name,omitempty"`
	TmuxWindowName  *string          `json:"tmux_window_name,omitempty"`
	StartedAt       *time.Time       `json:"started_at,omitempty"`
}

type HTTPClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewHTTPClient(baseURL string, httpClient *http.Client) *HTTPClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &HTTPClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
	}
}

func (c *HTTPClient) RegisterHost(ctx context.Context, host HostRegistration) error {
	req := registerHostRequest{
		HostID:           host.HostID,
		Hostname:         host.Hostname,
		Capabilities:     append([]string(nil), host.Capabilities...),
		AllowedRepoRoots: append([]string(nil), host.AllowedRepoRoots...),
		Labels:           cloneLabels(host.Labels),
	}
	return c.postJSON(ctx, "/v1/hosts/register", req)
}

func (c *HTTPClient) Heartbeat(ctx context.Context, hostID domain.HostID) error {
	return c.postJSON(ctx, "/v1/hosts/"+hostID.String()+"/heartbeat", nil)
}

func (c *HTTPClient) GetWork(ctx context.Context, hostID domain.HostID) (*Work, error) {
	var resp struct {
		OK    bool       `json:"ok"`
		Data  Work       `json:"data"`
		Error *errorBody `json:"error,omitempty"`
	}
	if err := c.getJSON(ctx, "/v1/hosts/"+hostID.String()+"/work", &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

func (c *HTTPClient) UpdateRunState(ctx context.Context, runID domain.RunID, update RunStateUpdate) error {
	return c.postJSON(ctx, "/v1/runs/"+runID.String()+"/state", update)
}

type registerHostRequest struct {
	HostID           domain.HostID     `json:"host_id"`
	Hostname         string            `json:"hostname"`
	Capabilities     []string          `json:"capabilities"`
	AllowedRepoRoots []string          `json:"allowed_repo_roots"`
	Labels           map[string]string `json:"labels"`
}

type envelope struct {
	OK    bool       `json:"ok"`
	Error *errorBody `json:"error,omitempty"`
}

type errorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

func (e *errorBody) String() string {
	if len(e.Details) == 0 {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	parts := make([]string, 0, len(e.Details))
	for key, value := range e.Details {
		parts = append(parts, key+"="+value)
	}
	return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, strings.Join(parts, ", "))
}

func (c *HTTPClient) postJSON(ctx context.Context, path string, payload any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	var env envelope
	if err := json.Unmarshal(respBody, &env); err != nil {
		return fmt.Errorf("server returned non-JSON (HTTP %d): %s", resp.StatusCode, truncate(respBody, 200))
	}

	if !env.OK {
		if env.Error != nil {
			return fmt.Errorf("server error (HTTP %d): %s", resp.StatusCode, env.Error.String())
		}
		return fmt.Errorf("server error (HTTP %d): unknown error", resp.StatusCode)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected HTTP %d with ok=true", resp.StatusCode)
	}

	return nil
}

func (c *HTTPClient) getJSON(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	var env envelope
	if err := json.Unmarshal(respBody, &env); err != nil {
		return fmt.Errorf("server returned non-JSON (HTTP %d): %s", resp.StatusCode, truncate(respBody, 200))
	}
	if !env.OK {
		if env.Error != nil {
			return fmt.Errorf("server error (HTTP %d): %s", resp.StatusCode, env.Error.String())
		}
		return fmt.Errorf("server error (HTTP %d): unknown error", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected HTTP %d with ok=true", resp.StatusCode)
	}
	if err := json.Unmarshal(respBody, dst); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func cloneLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func truncate(data []byte, n int) string {
	s := string(data)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
