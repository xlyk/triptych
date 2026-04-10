package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/xlyk/triptych/internal/db"
	"github.com/xlyk/triptych/internal/domain"
)

func TestHostRegisterAndHeartbeat(t *testing.T) {
	store := newMemoryStore()
	handler := NewHandler(store, nil)

	register := doJSON(t, handler, http.MethodPost, "/v1/hosts/register", map[string]any{
		"host_id":            "host-1",
		"hostname":           "mbp.local",
		"capabilities":       []string{"codex", "tmux"},
		"allowed_repo_roots": []string{"/Users/kyle/work"},
		"labels":             map[string]string{"env": "dev"},
	})
	assertStatus(t, register, http.StatusOK)

	list := doJSON(t, handler, http.MethodGet, "/v1/hosts", nil)
	assertStatus(t, list, http.StatusOK)
	var listBody struct {
		OK   bool `json:"ok"`
		Data struct {
			Hosts []hostSummary `json:"hosts"`
		} `json:"data"`
	}
	decodeResponse(t, list, &listBody)
	if len(listBody.Data.Hosts) != 1 {
		t.Fatalf("hosts len = %d, want 1", len(listBody.Data.Hosts))
	}
	if listBody.Data.Hosts[0].Health != hostHealthOnline {
		t.Fatalf("host health = %q, want %q", listBody.Data.Hosts[0].Health, hostHealthOnline)
	}

	heartbeat := doJSON(t, handler, http.MethodPost, "/v1/hosts/host-1/heartbeat", nil)
	assertStatus(t, heartbeat, http.StatusOK)
}

func TestCreateJobAndWorkBundle(t *testing.T) {
	store := newMemoryStore()
	now := time.Now().UTC()
	store.mustUpsertHost(&domain.Host{
		HostID:           "host-1",
		Hostname:         "mbp.local",
		Online:           true,
		LastHeartbeatAt:  &now,
		Capabilities:     []string{"codex", "tmux"},
		AllowedRepoRoots: []string{"/Users/kyle/work"},
		Labels:           map[string]string{},
	})
	handler := NewHandler(store, nil)

	resp := doJSON(t, handler, http.MethodPost, "/v1/jobs", map[string]any{
		"agent":     "codex",
		"host_id":   "host-1",
		"repo_path": "/Users/kyle/work/repo",
		"goal":      "Fix the tests",
	})
	assertStatus(t, resp, http.StatusCreated)

	var createBody struct {
		OK   bool `json:"ok"`
		Data struct {
			Job domain.Job `json:"job"`
			Run domain.Run `json:"run"`
		} `json:"data"`
	}
	decodeResponse(t, resp, &createBody)
	if createBody.Data.Job.Status != domain.JobStatusAssigned {
		t.Fatalf("job status = %q, want %q", createBody.Data.Job.Status, domain.JobStatusAssigned)
	}
	if createBody.Data.Run.Status != domain.RunStatusPendingLaunch {
		t.Fatalf("run status = %q, want %q", createBody.Data.Run.Status, domain.RunStatusPendingLaunch)
	}

	work := doJSON(t, handler, http.MethodGet, "/v1/hosts/host-1/work", nil)
	assertStatus(t, work, http.StatusOK)
	var workBody struct {
		OK   bool         `json:"ok"`
		Data workResponse `json:"data"`
	}
	decodeResponse(t, work, &workBody)
	if len(workBody.Data.LaunchableJobs) != 1 {
		t.Fatalf("launchable jobs len = %d, want 1", len(workBody.Data.LaunchableJobs))
	}
	if workBody.Data.LaunchableJobs[0].JobID != createBody.Data.Job.JobID {
		t.Fatalf("launchable job_id = %q, want %q", workBody.Data.LaunchableJobs[0].JobID, createBody.Data.Job.JobID)
	}
}

func TestListJobsStatusFilter(t *testing.T) {
	store := newMemoryStore()
	now := time.Now().UTC()
	store.mustUpsertHost(&domain.Host{
		HostID:           "host-1",
		Hostname:         "mbp.local",
		Online:           true,
		LastHeartbeatAt:  &now,
		Capabilities:     []string{"codex", "tmux"},
		AllowedRepoRoots: []string{"/Users/kyle/work"},
		Labels:           map[string]string{},
	})
	store.jobs["job-running"] = domain.Job{JobID: "job-running", HostID: "host-1", Agent: domain.AgentCodex, Status: domain.JobStatusRunning, RepoPath: "/repo", Goal: "run"}
	store.jobs["job-failed"] = domain.Job{JobID: "job-failed", HostID: "host-1", Agent: domain.AgentCodex, Status: domain.JobStatusFailed, RepoPath: "/repo", Goal: "fail"}
	handler := NewHandler(store, nil)

	resp := doJSON(t, handler, http.MethodGet, "/v1/jobs?status=running", nil)
	assertStatus(t, resp, http.StatusOK)
	var body struct {
		OK   bool `json:"ok"`
		Data struct {
			Jobs []jobSummary `json:"jobs"`
		} `json:"data"`
	}
	decodeResponse(t, resp, &body)
	if len(body.Data.Jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(body.Data.Jobs))
	}
	if body.Data.Jobs[0].Job.JobID != "job-running" {
		t.Fatalf("job_id = %q, want %q", body.Data.Jobs[0].Job.JobID, "job-running")
	}
}

func TestListJobsStatusFilterInvalid(t *testing.T) {
	handler := NewHandler(newMemoryStore(), nil)
	resp := doJSON(t, handler, http.MethodGet, "/v1/jobs?status=bogus", nil)
	assertStatus(t, resp, http.StatusBadRequest)
}

func TestJobCommandIdempotencyAndAckFlow(t *testing.T) {
	store := newMemoryStore()
	now := time.Now().UTC()
	store.mustUpsertHost(&domain.Host{
		HostID:           "host-1",
		Hostname:         "mbp.local",
		Online:           true,
		LastHeartbeatAt:  &now,
		Capabilities:     []string{"codex", "tmux"},
		AllowedRepoRoots: []string{"/Users/kyle/work"},
		Labels:           map[string]string{},
	})
	job := &domain.Job{
		JobID:       "job-1",
		HostID:      "host-1",
		Agent:       domain.AgentCodex,
		Status:      domain.JobStatusRunning,
		RepoPath:    "/Users/kyle/work/repo",
		Workdir:     "/Users/kyle/work/repo",
		Goal:        "Do the work",
		Priority:    domain.PriorityNormal,
		MaxDuration: "4h",
		Metadata:    map[string]string{},
	}
	run := &domain.Run{
		RunID:           "run-1",
		JobID:           "job-1",
		HostID:          "host-1",
		Status:          domain.RunStatusActive,
		TmuxSessionName: "agt-job-run",
		TmuxWindowName:  "main",
	}
	store.mustCreateJobWithRun(job, run)
	handler := NewHandler(store, nil)

	first := doJSON(t, handler, http.MethodPost, "/v1/jobs/job-1/commands/send", map[string]any{
		"text":            "continue",
		"idempotency_key": "req-1",
	})
	assertStatus(t, first, http.StatusCreated)
	var firstBody struct {
		OK   bool `json:"ok"`
		Data struct {
			Command domain.Command `json:"command"`
		} `json:"data"`
	}
	decodeResponse(t, first, &firstBody)

	second := doJSON(t, handler, http.MethodPost, "/v1/jobs/job-1/commands/send", map[string]any{
		"text":            "continue",
		"idempotency_key": "req-1",
	})
	assertStatus(t, second, http.StatusOK)
	var secondBody struct {
		OK   bool `json:"ok"`
		Data struct {
			Command domain.Command `json:"command"`
		} `json:"data"`
	}
	decodeResponse(t, second, &secondBody)
	if secondBody.Data.Command.CommandID != firstBody.Data.Command.CommandID {
		t.Fatalf("idempotent command_id = %q, want %q", secondBody.Data.Command.CommandID, firstBody.Data.Command.CommandID)
	}

	conflict := doJSON(t, handler, http.MethodPost, "/v1/jobs/job-1/commands/send", map[string]any{
		"text":            "different",
		"idempotency_key": "req-1",
	})
	assertStatus(t, conflict, http.StatusConflict)

	work := doJSON(t, handler, http.MethodGet, "/v1/hosts/host-1/work", nil)
	assertStatus(t, work, http.StatusOK)
	var workBody struct {
		OK   bool         `json:"ok"`
		Data workResponse `json:"data"`
	}
	decodeResponse(t, work, &workBody)
	if len(workBody.Data.PendingCommands) != 1 {
		t.Fatalf("pending commands len = %d, want 1", len(workBody.Data.PendingCommands))
	}

	ack := doJSON(t, handler, http.MethodPost, "/v1/commands/"+firstBody.Data.Command.CommandID.String()+"/ack", nil)
	assertStatus(t, ack, http.StatusOK)

	workAfterAck := doJSON(t, handler, http.MethodGet, "/v1/hosts/host-1/work", nil)
	assertStatus(t, workAfterAck, http.StatusOK)
	var workAfterAckBody struct {
		OK   bool         `json:"ok"`
		Data workResponse `json:"data"`
	}
	decodeResponse(t, workAfterAck, &workAfterAckBody)
	if len(workAfterAckBody.Data.PendingCommands) != 1 {
		t.Fatalf("pending commands after ack len = %d, want 1", len(workAfterAckBody.Data.PendingCommands))
	}

	observe := doJSON(t, handler, http.MethodPost, "/v1/commands/"+firstBody.Data.Command.CommandID.String()+"/observe", nil)
	assertStatus(t, observe, http.StatusOK)

	workAfterObserve := doJSON(t, handler, http.MethodGet, "/v1/hosts/host-1/work", nil)
	assertStatus(t, workAfterObserve, http.StatusOK)
	var workAfterObserveBody struct {
		OK   bool         `json:"ok"`
		Data workResponse `json:"data"`
	}
	decodeResponse(t, workAfterObserve, &workAfterObserveBody)
	if len(workAfterObserveBody.Data.PendingCommands) != 0 {
		t.Fatalf("pending commands after observe len = %d, want 0", len(workAfterObserveBody.Data.PendingCommands))
	}
}

func TestRunStateSnapshotTailAndAttach(t *testing.T) {
	store := newMemoryStore()
	now := time.Now().UTC()
	store.mustUpsertHost(&domain.Host{
		HostID:           "host-1",
		Hostname:         "mbp.local",
		Online:           true,
		LastHeartbeatAt:  &now,
		Capabilities:     []string{"codex", "tmux"},
		AllowedRepoRoots: []string{"/Users/kyle/work"},
		Labels:           map[string]string{},
	})
	job := &domain.Job{
		JobID:       "job-1",
		HostID:      "host-1",
		Agent:       domain.AgentCodex,
		Status:      domain.JobStatusAssigned,
		RepoPath:    "/Users/kyle/work/repo",
		Workdir:     "/Users/kyle/work/repo",
		Goal:        "Do the work",
		Priority:    domain.PriorityNormal,
		MaxDuration: "4h",
		Metadata:    map[string]string{},
	}
	run := &domain.Run{
		RunID:          "run-1",
		JobID:          "job-1",
		HostID:         "host-1",
		Status:         domain.RunStatusPendingLaunch,
		TmuxWindowName: "main",
	}
	store.mustCreateJobWithRun(job, run)
	handler := NewHandler(store, nil)

	state := doJSON(t, handler, http.MethodPost, "/v1/runs/run-1/state", map[string]any{
		"status":            "active",
		"tmux_session_name": "agt-job-1-run-1",
		"tmux_window_name":  "main",
	})
	assertStatus(t, state, http.StatusOK)

	attach := doJSON(t, handler, http.MethodGet, "/v1/jobs/job-1/attach", nil)
	assertStatus(t, attach, http.StatusOK)

	snapshot := doJSON(t, handler, http.MethodPost, "/v1/runs/run-1/snapshot", map[string]any{
		"host_id":     "host-1",
		"captured_at": now.Format(time.RFC3339),
		"line_count":  2,
		"stale":       false,
		"output":      "line 1\nline 2\n",
	})
	assertStatus(t, snapshot, http.StatusOK)

	tail := doJSON(t, handler, http.MethodGet, "/v1/jobs/job-1/tail", nil)
	assertStatus(t, tail, http.StatusOK)
	var tailBody struct {
		OK   bool `json:"ok"`
		Data struct {
			JobID    domain.JobID          `json:"job_id"`
			Snapshot domain.OutputSnapshot `json:"snapshot"`
		} `json:"data"`
	}
	decodeResponse(t, tail, &tailBody)
	if tailBody.Data.Snapshot.OutputText != "line 1\nline 2\n" {
		t.Fatalf("snapshot output = %q", tailBody.Data.Snapshot.OutputText)
	}

	jobResp := doJSON(t, handler, http.MethodGet, "/v1/jobs/job-1", nil)
	assertStatus(t, jobResp, http.StatusOK)
	var jobBody struct {
		OK   bool `json:"ok"`
		Data struct {
			Job jobSummary `json:"job"`
		} `json:"data"`
	}
	decodeResponse(t, jobResp, &jobBody)
	if jobBody.Data.Job.Job.Status != domain.JobStatusRunning {
		t.Fatalf("job status = %q, want %q", jobBody.Data.Job.Job.Status, domain.JobStatusRunning)
	}
	if jobBody.Data.Job.Run == nil || jobBody.Data.Job.Run.TmuxSessionName != "agt-job-1-run-1" {
		t.Fatalf("job run attach data not updated")
	}
}

func doJSON(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody *bytes.Reader
	if body == nil {
		reqBody = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reqBody = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, reqBody)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, want, rec.Body.String())
	}
}

func decodeResponse(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("unmarshal response: %v body=%s", err, rec.Body.String())
	}
}

type memoryStore struct {
	mu        sync.Mutex
	hosts     map[domain.HostID]domain.Host
	jobs      map[domain.JobID]domain.Job
	runs      map[domain.RunID]domain.Run
	commands  map[domain.CommandID]domain.Command
	events    map[domain.EventID]domain.Event
	snapshots map[domain.RunID]domain.OutputSnapshot
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		hosts:     map[domain.HostID]domain.Host{},
		jobs:      map[domain.JobID]domain.Job{},
		runs:      map[domain.RunID]domain.Run{},
		commands:  map[domain.CommandID]domain.Command{},
		events:    map[domain.EventID]domain.Event{},
		snapshots: map[domain.RunID]domain.OutputSnapshot{},
	}
}

func (s *memoryStore) ListHosts(context.Context) ([]domain.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Host, 0, len(s.hosts))
	for _, host := range s.hosts {
		out = append(out, host)
	}
	return out, nil
}

func (s *memoryStore) GetHost(_ context.Context, id domain.HostID) (*domain.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	host, ok := s.hosts[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	copy := host
	return &copy, nil
}

func (s *memoryStore) UpsertHost(_ context.Context, host *domain.Host) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if host.CreatedAt.IsZero() {
		host.CreatedAt = time.Now().UTC()
	}
	host.UpdatedAt = time.Now().UTC()
	s.hosts[host.HostID] = *host
	return nil
}

func (s *memoryStore) UpdateHostHeartbeat(_ context.Context, id domain.HostID, online bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	host, ok := s.hosts[id]
	if !ok {
		return db.ErrNotFound
	}
	now := time.Now().UTC()
	host.Online = online
	host.LastHeartbeatAt = &now
	host.UpdatedAt = now
	s.hosts[id] = host
	return nil
}

func (s *memoryStore) GetJob(_ context.Context, id domain.JobID) (*domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	copy := job
	return &copy, nil
}

func (s *memoryStore) GetJobByIdempotencyKey(_ context.Context, key string) (*domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, job := range s.jobs {
		if job.IdempotencyKey == key {
			copy := job
			return &copy, nil
		}
	}
	return nil, db.ErrNotFound
}

func (s *memoryStore) ListJobs(_ context.Context, statusFilter *domain.JobStatus) ([]domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		if statusFilter != nil && job.Status != *statusFilter {
			continue
		}
		out = append(out, job)
	}
	return out, nil
}

func (s *memoryStore) UpdateJobStatus(_ context.Context, id domain.JobID, status domain.JobStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return db.ErrNotFound
	}
	job.Status = status
	s.jobs[id] = job
	return nil
}

func (s *memoryStore) CreateJobWithInitialRun(_ context.Context, job *domain.Job, run *domain.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[job.JobID]; ok {
		return db.ErrConflict
	}
	now := time.Now().UTC()
	job.CreatedAt = now
	job.UpdatedAt = now
	run.CreatedAt = now
	run.UpdatedAt = now
	s.jobs[job.JobID] = *job
	s.runs[run.RunID] = *run
	return nil
}

func (s *memoryStore) GetRun(_ context.Context, id domain.RunID) (*domain.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	copy := run
	return &copy, nil
}

func (s *memoryStore) GetLatestRunByJob(_ context.Context, jobID domain.JobID) (*domain.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found *domain.Run
	for _, run := range s.runs {
		if run.JobID != jobID {
			continue
		}
		copy := run
		if found == nil || copy.CreatedAt.After(found.CreatedAt) {
			found = &copy
		}
	}
	if found == nil {
		return nil, db.ErrNotFound
	}
	return found, nil
}

func (s *memoryStore) UpdateRunState(_ context.Context, id domain.RunID, update db.RunStateUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return db.ErrNotFound
	}
	run.Status = update.Status
	if update.TmuxSessionName != nil {
		run.TmuxSessionName = *update.TmuxSessionName
	}
	if update.TmuxWindowName != nil {
		run.TmuxWindowName = *update.TmuxWindowName
	}
	if update.StartedAt != nil {
		run.StartedAt = update.StartedAt
	}
	if update.FinishedAt != nil {
		run.FinishedAt = update.FinishedAt
	}
	if update.LastEventAt != nil {
		run.LastEventAt = update.LastEventAt
	}
	if update.StopRequested != nil {
		run.StopRequested = *update.StopRequested
	}
	if update.TerminalDisposition != nil {
		run.TerminalDisposition = *update.TerminalDisposition
	}
	s.runs[id] = run
	return nil
}

func (s *memoryStore) SetRunStopRequested(_ context.Context, id domain.RunID, stopRequested bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return db.ErrNotFound
	}
	run.StopRequested = stopRequested
	s.runs[id] = run
	return nil
}

func (s *memoryStore) ListLaunchableJobRunsByHost(_ context.Context, hostID domain.HostID) ([]db.JobRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.JobRun
	for _, run := range s.runs {
		if run.HostID != hostID || run.Status != domain.RunStatusPendingLaunch {
			continue
		}
		job := s.jobs[run.JobID]
		out = append(out, db.JobRun{Job: job, Run: run})
	}
	return out, nil
}

func (s *memoryStore) ListActiveJobRunsByHost(_ context.Context, hostID domain.HostID) ([]db.JobRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.JobRun
	for _, run := range s.runs {
		if run.HostID != hostID {
			continue
		}
		if run.Status == domain.RunStatusPendingLaunch || run.Status == domain.RunStatusExited || run.Status == domain.RunStatusCrashed {
			continue
		}
		job := s.jobs[run.JobID]
		out = append(out, db.JobRun{Job: job, Run: run})
	}
	return out, nil
}

func (s *memoryStore) CreateCommand(_ context.Context, command *domain.Command) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.commands {
		if existing.RunID == command.RunID && existing.CommandType == command.CommandType && existing.RequestIdempotencyKey != "" && existing.RequestIdempotencyKey == command.RequestIdempotencyKey {
			return db.ErrConflict
		}
	}
	now := time.Now().UTC()
	command.CreatedAt = now
	command.UpdatedAt = now
	s.commands[command.CommandID] = *command
	return nil
}

func (s *memoryStore) GetCommand(_ context.Context, id domain.CommandID) (*domain.Command, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	command, ok := s.commands[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	copy := command
	return &copy, nil
}

func (s *memoryStore) GetCommandByIdempotencyKey(_ context.Context, runID domain.RunID, commandType domain.CommandType, key string) (*domain.Command, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, command := range s.commands {
		if command.RunID == runID && command.CommandType == commandType && command.RequestIdempotencyKey == key {
			copy := command
			return &copy, nil
		}
	}
	return nil, db.ErrNotFound
}

func (s *memoryStore) UpdateCommandState(_ context.Context, id domain.CommandID, state domain.CommandState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	command, ok := s.commands[id]
	if !ok {
		return db.ErrNotFound
	}
	command.State = state
	s.commands[id] = command
	return nil
}

func (s *memoryStore) ListPendingCommandsByHost(_ context.Context, hostID domain.HostID) ([]domain.Command, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Command
	for _, command := range s.commands {
		if command.HostID == hostID && command.State != domain.CommandStateObserved {
			out = append(out, command)
		}
	}
	return out, nil
}

func (s *memoryStore) CreateEvent(_ context.Context, event *domain.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.events[event.EventID]; ok {
		return db.ErrConflict
	}
	s.events[event.EventID] = *event
	return nil
}

func (s *memoryStore) UpsertOutputSnapshot(_ context.Context, snapshot *domain.OutputSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot.UpdatedAt = time.Now().UTC()
	s.snapshots[snapshot.RunID] = *snapshot
	return nil
}

func (s *memoryStore) GetOutputSnapshot(_ context.Context, runID domain.RunID) (*domain.OutputSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.snapshots[runID]
	if !ok {
		return nil, db.ErrNotFound
	}
	copy := snapshot
	return &copy, nil
}

func (s *memoryStore) mustUpsertHost(host *domain.Host) {
	if err := s.UpsertHost(context.Background(), host); err != nil {
		panic(err)
	}
}

func (s *memoryStore) mustCreateJobWithRun(job *domain.Job, run *domain.Run) {
	if err := s.CreateJobWithInitialRun(context.Background(), job, run); err != nil {
		panic(err)
	}
}

var _ Store = (*memoryStore)(nil)

func TestPathWithinRoots(t *testing.T) {
	if !pathWithinRoots("/Users/kyle/work/repo", []string{"/Users/kyle/work"}) {
		t.Fatal("expected path to be within root")
	}
	if pathWithinRoots("/tmp/repo", []string{"/Users/kyle/work"}) {
		t.Fatal("expected path to be outside root")
	}
}

func TestComputeHostHealthOfflineWithoutHeartbeat(t *testing.T) {
	host := domain.Host{HostID: "host-1"}
	if got := computeHostHealth(host, time.Now().UTC()); got != hostHealthOffline {
		t.Fatalf("health = %q, want %q", got, hostHealthOffline)
	}
}

func TestDecodeJSONInvalid(t *testing.T) {
	err := decodeJSON(httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString("{")), &struct{}{})
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Code != "invalid_argument" {
		t.Fatalf("decodeJSON error = %v", err)
	}
}
