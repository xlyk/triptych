package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xlyk/triptych/internal/domain"
)

// ErrNotFound is returned when a lookup finds no matching row.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned on idempotency or uniqueness conflicts.
var ErrConflict = errors.New("conflict")

// Store provides typed persistence operations for control-plane entities.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore creates a Store backed by the given pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// isUniqueViolation checks if a pgx error is a Postgres unique_violation (23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// --- Hosts ---

func (s *Store) CreateHost(ctx context.Context, h *domain.Host) error {
	caps, _ := json.Marshal(h.Capabilities)
	roots, _ := json.Marshal(h.AllowedRepoRoots)
	labels, _ := json.Marshal(h.Labels)
	now := time.Now().UTC()
	err := s.pool.QueryRow(ctx, `
		INSERT INTO hosts (host_id, hostname, online, last_heartbeat_at, capabilities, allowed_repo_roots, labels, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
		RETURNING created_at, updated_at
	`, h.HostID, h.Hostname, h.Online, h.LastHeartbeatAt, caps, roots, labels, now).Scan(&h.CreatedAt, &h.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("host %s: %w", h.HostID, ErrConflict)
		}
		return fmt.Errorf("create host: %w", err)
	}
	return nil
}

func (s *Store) GetHost(ctx context.Context, id domain.HostID) (*domain.Host, error) {
	h := &domain.Host{}
	var caps, roots, labels []byte
	err := s.pool.QueryRow(ctx, `
		SELECT host_id, hostname, online, last_heartbeat_at, capabilities, allowed_repo_roots, labels, created_at, updated_at
		FROM hosts WHERE host_id = $1
	`, id).Scan(&h.HostID, &h.Hostname, &h.Online, &h.LastHeartbeatAt, &caps, &roots, &labels, &h.CreatedAt, &h.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get host: %w", err)
	}
	_ = json.Unmarshal(caps, &h.Capabilities)
	_ = json.Unmarshal(roots, &h.AllowedRepoRoots)
	_ = json.Unmarshal(labels, &h.Labels)
	return h, nil
}

func (s *Store) ListHosts(ctx context.Context) ([]domain.Host, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT host_id, hostname, online, last_heartbeat_at, capabilities, allowed_repo_roots, labels, created_at, updated_at
		FROM hosts ORDER BY created_at
	`)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	defer rows.Close()
	var hosts []domain.Host
	for rows.Next() {
		var h domain.Host
		var caps, roots, labels []byte
		if err := rows.Scan(&h.HostID, &h.Hostname, &h.Online, &h.LastHeartbeatAt, &caps, &roots, &labels, &h.CreatedAt, &h.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan host: %w", err)
		}
		_ = json.Unmarshal(caps, &h.Capabilities)
		_ = json.Unmarshal(roots, &h.AllowedRepoRoots)
		_ = json.Unmarshal(labels, &h.Labels)
		hosts = append(hosts, h)
	}
	return hosts, rows.Err()
}

func (s *Store) UpdateHostHeartbeat(ctx context.Context, id domain.HostID, online bool) error {
	now := time.Now().UTC()
	tag, err := s.pool.Exec(ctx, `
		UPDATE hosts SET online = $2, last_heartbeat_at = $3, updated_at = $3 WHERE host_id = $1
	`, id, online, now)
	if err != nil {
		return fmt.Errorf("update host heartbeat: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Jobs ---

// CreateJob inserts a new job. If IdempotencyKey is set and a job with the
// same key already exists, the existing job is returned and ErrConflict is NOT
// returned (idempotent success). If a different job exists with the same key,
// ErrConflict is returned.
func (s *Store) CreateJob(ctx context.Context, j *domain.Job) error {
	meta, _ := json.Marshal(j.Metadata)
	var idempKey *string
	if j.IdempotencyKey != "" {
		idempKey = &j.IdempotencyKey
	}
	now := time.Now().UTC()
	err := s.pool.QueryRow(ctx, `
		INSERT INTO jobs (job_id, host_id, agent, status, repo_path, workdir, goal, priority, max_duration, idempotency_key, metadata_json, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)
		RETURNING created_at, updated_at
	`, j.JobID, j.HostID, j.Agent, j.Status, j.RepoPath, j.Workdir, j.Goal, j.Priority, j.MaxDuration, idempKey, meta, now).Scan(&j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			// If idempotency key conflict, try to return the existing job.
			if j.IdempotencyKey != "" {
				existing, getErr := s.GetJobByIdempotencyKey(ctx, j.IdempotencyKey)
				if getErr != nil {
					return fmt.Errorf("create job idempotency lookup: %w", getErr)
				}
				// Same key — return existing job (idempotent success).
				*j = *existing
				return nil
			}
			return fmt.Errorf("job %s: %w", j.JobID, ErrConflict)
		}
		return fmt.Errorf("create job: %w", err)
	}
	return nil
}

func (s *Store) GetJob(ctx context.Context, id domain.JobID) (*domain.Job, error) {
	j := &domain.Job{}
	var meta []byte
	var idempKey *string
	err := s.pool.QueryRow(ctx, `
		SELECT job_id, host_id, agent, status, repo_path, workdir, goal, priority, max_duration, idempotency_key, metadata_json, created_at, updated_at
		FROM jobs WHERE job_id = $1
	`, id).Scan(&j.JobID, &j.HostID, &j.Agent, &j.Status, &j.RepoPath, &j.Workdir, &j.Goal, &j.Priority, &j.MaxDuration, &idempKey, &meta, &j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get job: %w", err)
	}
	if idempKey != nil {
		j.IdempotencyKey = *idempKey
	}
	_ = json.Unmarshal(meta, &j.Metadata)
	return j, nil
}

func (s *Store) GetJobByIdempotencyKey(ctx context.Context, key string) (*domain.Job, error) {
	j := &domain.Job{}
	var meta []byte
	var idempKey *string
	err := s.pool.QueryRow(ctx, `
		SELECT job_id, host_id, agent, status, repo_path, workdir, goal, priority, max_duration, idempotency_key, metadata_json, created_at, updated_at
		FROM jobs WHERE idempotency_key = $1
	`, key).Scan(&j.JobID, &j.HostID, &j.Agent, &j.Status, &j.RepoPath, &j.Workdir, &j.Goal, &j.Priority, &j.MaxDuration, &idempKey, &meta, &j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get job by idempotency key: %w", err)
	}
	if idempKey != nil {
		j.IdempotencyKey = *idempKey
	}
	_ = json.Unmarshal(meta, &j.Metadata)
	return j, nil
}

func (s *Store) ListJobs(ctx context.Context) ([]domain.Job, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT job_id, host_id, agent, status, repo_path, workdir, goal, priority, max_duration, idempotency_key, metadata_json, created_at, updated_at
		FROM jobs ORDER BY created_at
	`)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()
	var jobs []domain.Job
	for rows.Next() {
		var j domain.Job
		var meta []byte
		var idempKey *string
		if err := rows.Scan(&j.JobID, &j.HostID, &j.Agent, &j.Status, &j.RepoPath, &j.Workdir, &j.Goal, &j.Priority, &j.MaxDuration, &idempKey, &meta, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		if idempKey != nil {
			j.IdempotencyKey = *idempKey
		}
		_ = json.Unmarshal(meta, &j.Metadata)
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (s *Store) UpdateJobStatus(ctx context.Context, id domain.JobID, status domain.JobStatus) error {
	now := time.Now().UTC()
	tag, err := s.pool.Exec(ctx, `
		UPDATE jobs SET status = $2, updated_at = $3 WHERE job_id = $1
	`, id, status, now)
	if err != nil {
		return fmt.Errorf("update job status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Runs ---

// CreateRun inserts a new run. The partial unique index on runs enforces
// at most one active (non-terminal) run per job.
func (s *Store) CreateRun(ctx context.Context, r *domain.Run) error {
	now := time.Now().UTC()
	err := s.pool.QueryRow(ctx, `
		INSERT INTO runs (run_id, job_id, host_id, status, tmux_session_name, tmux_window_name, started_at, finished_at, last_event_at, stop_requested, terminal_disposition, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)
		RETURNING created_at, updated_at
	`, r.RunID, r.JobID, r.HostID, r.Status, nilIfEmpty(r.TmuxSessionName), nilIfEmpty(r.TmuxWindowName),
		r.StartedAt, r.FinishedAt, r.LastEventAt, r.StopRequested, nilIfEmpty(string(r.TerminalDisposition)), now,
	).Scan(&r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("run %s: %w", r.RunID, ErrConflict)
		}
		return fmt.Errorf("create run: %w", err)
	}
	return nil
}

func (s *Store) GetRun(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	r := &domain.Run{}
	var tmuxSession, tmuxWindow, termDisp *string
	err := s.pool.QueryRow(ctx, `
		SELECT run_id, job_id, host_id, status, tmux_session_name, tmux_window_name, started_at, finished_at, last_event_at, stop_requested, terminal_disposition, created_at, updated_at
		FROM runs WHERE run_id = $1
	`, id).Scan(&r.RunID, &r.JobID, &r.HostID, &r.Status, &tmuxSession, &tmuxWindow, &r.StartedAt, &r.FinishedAt, &r.LastEventAt, &r.StopRequested, &termDisp, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get run: %w", err)
	}
	if tmuxSession != nil {
		r.TmuxSessionName = *tmuxSession
	}
	if tmuxWindow != nil {
		r.TmuxWindowName = *tmuxWindow
	}
	if termDisp != nil {
		r.TerminalDisposition = domain.TerminalDisposition(*termDisp)
	}
	return r, nil
}

func (s *Store) UpdateRunStatus(ctx context.Context, id domain.RunID, status domain.RunStatus) error {
	now := time.Now().UTC()
	tag, err := s.pool.Exec(ctx, `
		UPDATE runs SET status = $2, updated_at = $3 WHERE run_id = $1
	`, id, status, now)
	if err != nil {
		return fmt.Errorf("update run status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) FinishRun(ctx context.Context, id domain.RunID, status domain.RunStatus, disposition domain.TerminalDisposition) error {
	now := time.Now().UTC()
	tag, err := s.pool.Exec(ctx, `
		UPDATE runs SET status = $2, terminal_disposition = $3, finished_at = $4, updated_at = $4 WHERE run_id = $1
	`, id, status, disposition, now)
	if err != nil {
		return fmt.Errorf("finish run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Commands ---

// CreateCommand inserts a new command. If RequestIdempotencyKey is set and a
// matching (run_id, command_type, request_idempotency_key) already exists,
// the existing command is returned (idempotent success).
func (s *Store) CreateCommand(ctx context.Context, c *domain.Command) error {
	var idempKey *string
	if c.RequestIdempotencyKey != "" {
		idempKey = &c.RequestIdempotencyKey
	}
	var payload []byte
	if c.PayloadJSON != "" {
		payload = []byte(c.PayloadJSON)
	}
	now := time.Now().UTC()
	err := s.pool.QueryRow(ctx, `
		INSERT INTO commands (command_id, job_id, run_id, host_id, command_type, request_idempotency_key, payload_json, state, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
		RETURNING created_at, updated_at
	`, c.CommandID, c.JobID, c.RunID, c.HostID, c.CommandType, idempKey, payload, c.State, now).Scan(&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			if c.RequestIdempotencyKey != "" {
				existing, getErr := s.GetCommandByIdempotencyKey(ctx, c.RunID, c.CommandType, c.RequestIdempotencyKey)
				if getErr != nil {
					return fmt.Errorf("create command idempotency lookup: %w", getErr)
				}
				*c = *existing
				return nil
			}
			return fmt.Errorf("command %s: %w", c.CommandID, ErrConflict)
		}
		return fmt.Errorf("create command: %w", err)
	}
	return nil
}

func (s *Store) GetCommand(ctx context.Context, id domain.CommandID) (*domain.Command, error) {
	c := &domain.Command{}
	var idempKey *string
	var payload []byte
	err := s.pool.QueryRow(ctx, `
		SELECT command_id, job_id, run_id, host_id, command_type, request_idempotency_key, payload_json, state, created_at, updated_at
		FROM commands WHERE command_id = $1
	`, id).Scan(&c.CommandID, &c.JobID, &c.RunID, &c.HostID, &c.CommandType, &idempKey, &payload, &c.State, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get command: %w", err)
	}
	if idempKey != nil {
		c.RequestIdempotencyKey = *idempKey
	}
	if payload != nil {
		c.PayloadJSON = string(payload)
	}
	return c, nil
}

func (s *Store) GetCommandByIdempotencyKey(ctx context.Context, runID domain.RunID, cmdType domain.CommandType, key string) (*domain.Command, error) {
	c := &domain.Command{}
	var idempKey *string
	var payload []byte
	err := s.pool.QueryRow(ctx, `
		SELECT command_id, job_id, run_id, host_id, command_type, request_idempotency_key, payload_json, state, created_at, updated_at
		FROM commands WHERE run_id = $1 AND command_type = $2 AND request_idempotency_key = $3
	`, runID, cmdType, key).Scan(&c.CommandID, &c.JobID, &c.RunID, &c.HostID, &c.CommandType, &idempKey, &payload, &c.State, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get command by idempotency key: %w", err)
	}
	if idempKey != nil {
		c.RequestIdempotencyKey = *idempKey
	}
	if payload != nil {
		c.PayloadJSON = string(payload)
	}
	return c, nil
}

func (s *Store) UpdateCommandState(ctx context.Context, id domain.CommandID, state domain.CommandState) error {
	now := time.Now().UTC()
	tag, err := s.pool.Exec(ctx, `
		UPDATE commands SET state = $2, updated_at = $3 WHERE command_id = $1
	`, id, state, now)
	if err != nil {
		return fmt.Errorf("update command state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Events ---

func (s *Store) CreateEvent(ctx context.Context, e *domain.Event) error {
	var payload []byte
	if e.PayloadJSON != "" {
		payload = []byte(e.PayloadJSON)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO events (event_id, host_id, job_id, run_id, source, event_type, occurred_at, payload_json)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, e.EventID, nilIfEmpty(string(e.HostID)), nilIfEmpty(string(e.JobID)), nilIfEmpty(string(e.RunID)),
		e.Source, e.EventType, e.OccurredAt, payload)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("event %s: %w", e.EventID, ErrConflict)
		}
		return fmt.Errorf("create event: %w", err)
	}
	return nil
}

func (s *Store) ListEventsByJob(ctx context.Context, jobID domain.JobID) ([]domain.Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT event_id, host_id, job_id, run_id, source, event_type, occurred_at, payload_json
		FROM events WHERE job_id = $1 ORDER BY occurred_at
	`, jobID)
	if err != nil {
		return nil, fmt.Errorf("list events by job: %w", err)
	}
	defer rows.Close()
	var events []domain.Event
	for rows.Next() {
		var e domain.Event
		var hostID, jobID, runID *string
		var payload []byte
		if err := rows.Scan(&e.EventID, &hostID, &jobID, &runID, &e.Source, &e.EventType, &e.OccurredAt, &payload); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if hostID != nil {
			e.HostID = domain.HostID(*hostID)
		}
		if jobID != nil {
			e.JobID = domain.JobID(*jobID)
		}
		if runID != nil {
			e.RunID = domain.RunID(*runID)
		}
		if payload != nil {
			e.PayloadJSON = string(payload)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// --- Output Snapshots ---

// UpsertOutputSnapshot inserts or replaces the output snapshot for a run.
func (s *Store) UpsertOutputSnapshot(ctx context.Context, snap *domain.OutputSnapshot) error {
	now := time.Now().UTC()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO run_output_snapshots (run_id, host_id, captured_at, line_count, stale, output_text, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (run_id) DO UPDATE SET
			host_id = EXCLUDED.host_id,
			captured_at = EXCLUDED.captured_at,
			line_count = EXCLUDED.line_count,
			stale = EXCLUDED.stale,
			output_text = EXCLUDED.output_text,
			updated_at = EXCLUDED.updated_at
	`, snap.RunID, snap.HostID, snap.CapturedAt, snap.LineCount, snap.Stale, snap.OutputText, now)
	if err != nil {
		return fmt.Errorf("upsert output snapshot: %w", err)
	}
	snap.UpdatedAt = now
	return nil
}

func (s *Store) GetOutputSnapshot(ctx context.Context, runID domain.RunID) (*domain.OutputSnapshot, error) {
	snap := &domain.OutputSnapshot{}
	err := s.pool.QueryRow(ctx, `
		SELECT run_id, host_id, captured_at, line_count, stale, output_text, updated_at
		FROM run_output_snapshots WHERE run_id = $1
	`, runID).Scan(&snap.RunID, &snap.HostID, &snap.CapturedAt, &snap.LineCount, &snap.Stale, &snap.OutputText, &snap.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get output snapshot: %w", err)
	}
	return snap, nil
}

// --- helpers ---

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
