package db_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/xlyk/triptych/internal/db"
	"github.com/xlyk/triptych/internal/domain"
)

// testPool returns a connected, migrated pool or skips the test.
func testPool(t *testing.T) *db.Store {
	t.Helper()
	dsn := os.Getenv("TRIPTYCH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TRIPTYCH_TEST_DATABASE_URL not set; skipping DB tests")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Clean tables for test isolation (order matters due to FK).
	for _, table := range []string{"run_output_snapshots", "commands", "events", "runs", "jobs", "hosts"} {
		if _, err := pool.Exec(ctx, fmt.Sprintf("DELETE FROM %s", table)); err != nil {
			t.Fatalf("clean %s: %v", table, err)
		}
	}

	return db.NewStore(pool)
}

func TestMigrationSanity(t *testing.T) {
	dsn := os.Getenv("TRIPTYCH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TRIPTYCH_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer pool.Close()

	// Run migrate twice — second run should be a no-op.
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("second migrate (idempotency): %v", err)
	}
}

func TestHostCRUD(t *testing.T) {
	s := testPool(t)
	ctx := context.Background()

	h := &domain.Host{
		HostID:           "host-1",
		Hostname:         "mbp-1.local",
		Online:           true,
		Capabilities:     []string{"tmux", "claude_code"},
		AllowedRepoRoots: []string{"/Users/kyle/work"},
		Labels:           map[string]string{"env": "dev"},
	}

	if err := s.CreateHost(ctx, h); err != nil {
		t.Fatalf("create host: %v", err)
	}
	if h.CreatedAt.IsZero() {
		t.Fatal("created_at not set")
	}

	got, err := s.GetHost(ctx, "host-1")
	if err != nil {
		t.Fatalf("get host: %v", err)
	}
	if got.Hostname != "mbp-1.local" {
		t.Errorf("hostname = %q, want %q", got.Hostname, "mbp-1.local")
	}
	if len(got.Capabilities) != 2 {
		t.Errorf("capabilities len = %d, want 2", len(got.Capabilities))
	}

	// List
	hosts, err := s.ListHosts(ctx)
	if err != nil {
		t.Fatalf("list hosts: %v", err)
	}
	if len(hosts) != 1 {
		t.Errorf("list hosts len = %d, want 1", len(hosts))
	}

	// Heartbeat update
	if err := s.UpdateHostHeartbeat(ctx, "host-1", false); err != nil {
		t.Fatalf("update heartbeat: %v", err)
	}
	got2, _ := s.GetHost(ctx, "host-1")
	if got2.Online {
		t.Error("expected online=false after heartbeat update")
	}

	// Not found
	_, err = s.GetHost(ctx, "nonexistent")
	if !errors.Is(err, db.ErrNotFound) {
		t.Errorf("get nonexistent host: got %v, want ErrNotFound", err)
	}
}

func TestJobCRUD(t *testing.T) {
	s := testPool(t)
	ctx := context.Background()

	// Create host first (FK).
	h := &domain.Host{HostID: "host-j", Hostname: "test.local", Capabilities: []string{}, AllowedRepoRoots: []string{}, Labels: map[string]string{}}
	if err := s.CreateHost(ctx, h); err != nil {
		t.Fatalf("create host: %v", err)
	}

	j := &domain.Job{
		JobID:       "job-1",
		HostID:      "host-j",
		Agent:       domain.AgentClaude,
		Status:      domain.JobStatusAssigned,
		RepoPath:    "/Users/kyle/repo",
		Workdir:     "/Users/kyle/repo",
		Goal:        "Fix tests",
		Priority:    domain.PriorityNormal,
		MaxDuration: "4h",
		Metadata:    map[string]string{"source": "test"},
	}

	if err := s.CreateJob(ctx, j); err != nil {
		t.Fatalf("create job: %v", err)
	}

	got, err := s.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Goal != "Fix tests" {
		t.Errorf("goal = %q, want %q", got.Goal, "Fix tests")
	}

	// Update status
	if err := s.UpdateJobStatus(ctx, "job-1", domain.JobStatusRunning); err != nil {
		t.Fatalf("update job status: %v", err)
	}
	got2, _ := s.GetJob(ctx, "job-1")
	if got2.Status != domain.JobStatusRunning {
		t.Errorf("status = %q, want %q", got2.Status, domain.JobStatusRunning)
	}

	// List
	jobs, err := s.ListJobs(ctx)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Errorf("list jobs len = %d, want 1", len(jobs))
	}
}

func TestJobIdempotency(t *testing.T) {
	s := testPool(t)
	ctx := context.Background()

	h := &domain.Host{HostID: "host-ji", Hostname: "test.local", Capabilities: []string{}, AllowedRepoRoots: []string{}, Labels: map[string]string{}}
	if err := s.CreateHost(ctx, h); err != nil {
		t.Fatalf("create host: %v", err)
	}

	j1 := &domain.Job{
		JobID:          "job-idemp-1",
		HostID:         "host-ji",
		Agent:          domain.AgentCodex,
		Status:         domain.JobStatusAssigned,
		RepoPath:       "/Users/kyle/repo",
		Workdir:        "/Users/kyle/repo",
		Goal:           "Do stuff",
		Priority:       domain.PriorityNormal,
		MaxDuration:    "4h",
		IdempotencyKey: "idem-key-1",
		Metadata:       map[string]string{},
	}
	if err := s.CreateJob(ctx, j1); err != nil {
		t.Fatalf("create job 1: %v", err)
	}

	// Second create with same idempotency key should succeed and return existing.
	j2 := &domain.Job{
		JobID:          "job-idemp-2",
		HostID:         "host-ji",
		Agent:          domain.AgentCodex,
		Status:         domain.JobStatusAssigned,
		RepoPath:       "/Users/kyle/repo",
		Workdir:        "/Users/kyle/repo",
		Goal:           "Do stuff differently",
		Priority:       domain.PriorityNormal,
		MaxDuration:    "4h",
		IdempotencyKey: "idem-key-1",
		Metadata:       map[string]string{},
	}
	if err := s.CreateJob(ctx, j2); err != nil {
		t.Fatalf("create job 2 (idempotent): %v", err)
	}
	if j2.JobID != "job-idemp-1" {
		t.Errorf("idempotent create returned job_id=%q, want %q", j2.JobID, "job-idemp-1")
	}
}

func TestRunCRUD(t *testing.T) {
	s := testPool(t)
	ctx := context.Background()

	h := &domain.Host{HostID: "host-r", Hostname: "test.local", Capabilities: []string{}, AllowedRepoRoots: []string{}, Labels: map[string]string{}}
	_ = s.CreateHost(ctx, h)
	j := &domain.Job{JobID: "job-r", HostID: "host-r", Agent: domain.AgentClaude, Status: domain.JobStatusAssigned, RepoPath: "/r", Workdir: "/r", Goal: "g", Priority: domain.PriorityNormal, MaxDuration: "1h", Metadata: map[string]string{}}
	_ = s.CreateJob(ctx, j)

	r := &domain.Run{
		RunID:  "run-1",
		JobID:  "job-r",
		HostID: "host-r",
		Status: domain.RunStatusPendingLaunch,
	}
	if err := s.CreateRun(ctx, r); err != nil {
		t.Fatalf("create run: %v", err)
	}

	got, err := s.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Status != domain.RunStatusPendingLaunch {
		t.Errorf("status = %q, want %q", got.Status, domain.RunStatusPendingLaunch)
	}

	// Update status
	if err := s.UpdateRunStatus(ctx, "run-1", domain.RunStatusActive); err != nil {
		t.Fatalf("update run status: %v", err)
	}

	// Finish
	if err := s.FinishRun(ctx, "run-1", domain.RunStatusExited, domain.TerminalDispositionCompleted); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	got2, _ := s.GetRun(ctx, "run-1")
	if got2.TerminalDisposition != domain.TerminalDispositionCompleted {
		t.Errorf("terminal_disposition = %q, want %q", got2.TerminalDisposition, domain.TerminalDispositionCompleted)
	}
}

func TestRunOneActivePerJob(t *testing.T) {
	s := testPool(t)
	ctx := context.Background()

	h := &domain.Host{HostID: "host-ra", Hostname: "t.local", Capabilities: []string{}, AllowedRepoRoots: []string{}, Labels: map[string]string{}}
	_ = s.CreateHost(ctx, h)
	j := &domain.Job{JobID: "job-ra", HostID: "host-ra", Agent: domain.AgentClaude, Status: domain.JobStatusAssigned, RepoPath: "/r", Workdir: "/r", Goal: "g", Priority: domain.PriorityNormal, MaxDuration: "1h", Metadata: map[string]string{}}
	_ = s.CreateJob(ctx, j)

	r1 := &domain.Run{RunID: "run-ra-1", JobID: "job-ra", HostID: "host-ra", Status: domain.RunStatusActive}
	if err := s.CreateRun(ctx, r1); err != nil {
		t.Fatalf("create run 1: %v", err)
	}

	// Second active run for same job should fail.
	r2 := &domain.Run{RunID: "run-ra-2", JobID: "job-ra", HostID: "host-ra", Status: domain.RunStatusPendingLaunch}
	err := s.CreateRun(ctx, r2)
	if !errors.Is(err, db.ErrConflict) {
		t.Errorf("second active run: got %v, want ErrConflict", err)
	}

	// After finishing the first run, a new one should be allowed.
	if err := s.FinishRun(ctx, "run-ra-1", domain.RunStatusExited, domain.TerminalDispositionCompleted); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	r3 := &domain.Run{RunID: "run-ra-3", JobID: "job-ra", HostID: "host-ra", Status: domain.RunStatusPendingLaunch}
	if err := s.CreateRun(ctx, r3); err != nil {
		t.Fatalf("create run after finish: %v", err)
	}
}

func TestCommandCRUD(t *testing.T) {
	s := testPool(t)
	ctx := context.Background()

	h := &domain.Host{HostID: "host-c", Hostname: "t.local", Capabilities: []string{}, AllowedRepoRoots: []string{}, Labels: map[string]string{}}
	_ = s.CreateHost(ctx, h)
	j := &domain.Job{JobID: "job-c", HostID: "host-c", Agent: domain.AgentClaude, Status: domain.JobStatusRunning, RepoPath: "/r", Workdir: "/r", Goal: "g", Priority: domain.PriorityNormal, MaxDuration: "1h", Metadata: map[string]string{}}
	_ = s.CreateJob(ctx, j)
	r := &domain.Run{RunID: "run-c", JobID: "job-c", HostID: "host-c", Status: domain.RunStatusActive}
	_ = s.CreateRun(ctx, r)

	c := &domain.Command{
		CommandID:   "cmd-1",
		JobID:       "job-c",
		RunID:       "run-c",
		HostID:      "host-c",
		CommandType: domain.CommandTypeSend,
		PayloadJSON: `{"text":"hello"}`,
		State:       domain.CommandStateRecorded,
	}
	if err := s.CreateCommand(ctx, c); err != nil {
		t.Fatalf("create command: %v", err)
	}

	got, err := s.GetCommand(ctx, "cmd-1")
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	if got.PayloadJSON != `{"text":"hello"}` {
		t.Errorf("payload = %q", got.PayloadJSON)
	}

	// Update state
	if err := s.UpdateCommandState(ctx, "cmd-1", domain.CommandStateAcknowledged); err != nil {
		t.Fatalf("update command state: %v", err)
	}
	got2, _ := s.GetCommand(ctx, "cmd-1")
	if got2.State != domain.CommandStateAcknowledged {
		t.Errorf("state = %q, want %q", got2.State, domain.CommandStateAcknowledged)
	}
}

func TestCommandIdempotency(t *testing.T) {
	s := testPool(t)
	ctx := context.Background()

	h := &domain.Host{HostID: "host-ci", Hostname: "t.local", Capabilities: []string{}, AllowedRepoRoots: []string{}, Labels: map[string]string{}}
	_ = s.CreateHost(ctx, h)
	j := &domain.Job{JobID: "job-ci", HostID: "host-ci", Agent: domain.AgentClaude, Status: domain.JobStatusRunning, RepoPath: "/r", Workdir: "/r", Goal: "g", Priority: domain.PriorityNormal, MaxDuration: "1h", Metadata: map[string]string{}}
	_ = s.CreateJob(ctx, j)
	r := &domain.Run{RunID: "run-ci", JobID: "job-ci", HostID: "host-ci", Status: domain.RunStatusActive}
	_ = s.CreateRun(ctx, r)

	c1 := &domain.Command{
		CommandID:             "cmd-ci-1",
		JobID:                 "job-ci",
		RunID:                 "run-ci",
		HostID:                "host-ci",
		CommandType:           domain.CommandTypeSend,
		RequestIdempotencyKey: "req-1",
		PayloadJSON:           `{"text":"hello"}`,
		State:                 domain.CommandStateRecorded,
	}
	if err := s.CreateCommand(ctx, c1); err != nil {
		t.Fatalf("create command 1: %v", err)
	}

	// Retry with same idempotency key should return existing.
	c2 := &domain.Command{
		CommandID:             "cmd-ci-2",
		JobID:                 "job-ci",
		RunID:                 "run-ci",
		HostID:                "host-ci",
		CommandType:           domain.CommandTypeSend,
		RequestIdempotencyKey: "req-1",
		PayloadJSON:           `{"text":"hello"}`,
		State:                 domain.CommandStateRecorded,
	}
	if err := s.CreateCommand(ctx, c2); err != nil {
		t.Fatalf("create command 2 (idempotent): %v", err)
	}
	if c2.CommandID != "cmd-ci-1" {
		t.Errorf("idempotent create returned command_id=%q, want %q", c2.CommandID, "cmd-ci-1")
	}

	// Same key but different command type should be independent.
	c3 := &domain.Command{
		CommandID:             "cmd-ci-3",
		JobID:                 "job-ci",
		RunID:                 "run-ci",
		HostID:                "host-ci",
		CommandType:           domain.CommandTypeInterrupt,
		RequestIdempotencyKey: "req-1",
		PayloadJSON:           `{}`,
		State:                 domain.CommandStateRecorded,
	}
	if err := s.CreateCommand(ctx, c3); err != nil {
		t.Fatalf("create command 3 (different type, same key): %v", err)
	}
	if c3.CommandID != "cmd-ci-3" {
		t.Errorf("expected new command, got command_id=%q", c3.CommandID)
	}
}

func TestEventCRUD(t *testing.T) {
	s := testPool(t)
	ctx := context.Background()

	h := &domain.Host{HostID: "host-e", Hostname: "t.local", Capabilities: []string{}, AllowedRepoRoots: []string{}, Labels: map[string]string{}}
	_ = s.CreateHost(ctx, h)
	j := &domain.Job{JobID: "job-e", HostID: "host-e", Agent: domain.AgentClaude, Status: domain.JobStatusAssigned, RepoPath: "/r", Workdir: "/r", Goal: "g", Priority: domain.PriorityNormal, MaxDuration: "1h", Metadata: map[string]string{}}
	_ = s.CreateJob(ctx, j)

	e := &domain.Event{
		EventID:     "evt-1",
		HostID:      "host-e",
		JobID:       "job-e",
		Source:      "daemon",
		EventType:   "job.created",
		OccurredAt:  time.Now().UTC(),
		PayloadJSON: `{"foo":"bar"}`,
	}
	if err := s.CreateEvent(ctx, e); err != nil {
		t.Fatalf("create event: %v", err)
	}

	events, err := s.ListEventsByJob(ctx, "job-e")
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	if events[0].EventType != "job.created" {
		t.Errorf("event_type = %q", events[0].EventType)
	}
}

func TestOutputSnapshotUpsert(t *testing.T) {
	s := testPool(t)
	ctx := context.Background()

	h := &domain.Host{HostID: "host-s", Hostname: "t.local", Capabilities: []string{}, AllowedRepoRoots: []string{}, Labels: map[string]string{}}
	_ = s.CreateHost(ctx, h)
	j := &domain.Job{JobID: "job-s", HostID: "host-s", Agent: domain.AgentClaude, Status: domain.JobStatusRunning, RepoPath: "/r", Workdir: "/r", Goal: "g", Priority: domain.PriorityNormal, MaxDuration: "1h", Metadata: map[string]string{}}
	_ = s.CreateJob(ctx, j)
	r := &domain.Run{RunID: "run-s", JobID: "job-s", HostID: "host-s", Status: domain.RunStatusActive}
	_ = s.CreateRun(ctx, r)

	snap := &domain.OutputSnapshot{
		RunID:      "run-s",
		HostID:     "host-s",
		CapturedAt: time.Now().UTC(),
		LineCount:  10,
		Stale:      false,
		OutputText: "line 1\nline 2\n",
	}
	if err := s.UpsertOutputSnapshot(ctx, snap); err != nil {
		t.Fatalf("upsert snapshot 1: %v", err)
	}

	got, err := s.GetOutputSnapshot(ctx, "run-s")
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	if got.LineCount != 10 {
		t.Errorf("line_count = %d, want 10", got.LineCount)
	}

	// Upsert again with new data.
	snap2 := &domain.OutputSnapshot{
		RunID:      "run-s",
		HostID:     "host-s",
		CapturedAt: time.Now().UTC(),
		LineCount:  20,
		Stale:      false,
		OutputText: "updated output\n",
	}
	if err := s.UpsertOutputSnapshot(ctx, snap2); err != nil {
		t.Fatalf("upsert snapshot 2: %v", err)
	}

	got2, err := s.GetOutputSnapshot(ctx, "run-s")
	if err != nil {
		t.Fatalf("get snapshot after upsert: %v", err)
	}
	if got2.LineCount != 20 {
		t.Errorf("line_count after upsert = %d, want 20", got2.LineCount)
	}
	if got2.OutputText != "updated output\n" {
		t.Errorf("output_text = %q", got2.OutputText)
	}

	// Not found
	_, err = s.GetOutputSnapshot(ctx, "nonexistent")
	if !errors.Is(err, db.ErrNotFound) {
		t.Errorf("get nonexistent snapshot: got %v, want ErrNotFound", err)
	}
}
