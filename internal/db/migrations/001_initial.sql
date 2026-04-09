-- 001_initial.sql: Initial schema for triptych control plane.

CREATE TABLE IF NOT EXISTS hosts (
    host_id         TEXT PRIMARY KEY,
    hostname        TEXT NOT NULL,
    online          BOOLEAN NOT NULL DEFAULT false,
    last_heartbeat_at TIMESTAMPTZ,
    capabilities    JSONB NOT NULL DEFAULT '[]',
    allowed_repo_roots JSONB NOT NULL DEFAULT '[]',
    labels          JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS jobs (
    job_id          TEXT PRIMARY KEY,
    host_id         TEXT NOT NULL REFERENCES hosts(host_id),
    agent           TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'assigned',
    repo_path       TEXT NOT NULL,
    workdir         TEXT NOT NULL,
    goal            TEXT NOT NULL,
    priority        TEXT NOT NULL DEFAULT 'normal',
    max_duration    TEXT NOT NULL DEFAULT '4h',
    idempotency_key TEXT,
    metadata_json   JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_idempotency_key
    ON jobs(idempotency_key) WHERE idempotency_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS runs (
    run_id               TEXT PRIMARY KEY,
    job_id               TEXT NOT NULL REFERENCES jobs(job_id),
    host_id              TEXT NOT NULL REFERENCES hosts(host_id),
    status               TEXT NOT NULL DEFAULT 'pending_launch',
    tmux_session_name    TEXT,
    tmux_window_name     TEXT,
    started_at           TIMESTAMPTZ,
    finished_at          TIMESTAMPTZ,
    last_event_at        TIMESTAMPTZ,
    stop_requested       BOOLEAN NOT NULL DEFAULT false,
    terminal_disposition TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Enforce at most one active (non-terminal) run per job.
CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_one_active_per_job
    ON runs(job_id) WHERE status NOT IN ('exited', 'crashed');

CREATE TABLE IF NOT EXISTS commands (
    command_id              TEXT PRIMARY KEY,
    job_id                  TEXT NOT NULL REFERENCES jobs(job_id),
    run_id                  TEXT NOT NULL REFERENCES runs(run_id),
    host_id                 TEXT NOT NULL REFERENCES hosts(host_id),
    command_type            TEXT NOT NULL,
    request_idempotency_key TEXT,
    payload_json            JSONB,
    state                   TEXT NOT NULL DEFAULT 'recorded',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Deduplicate mutating commands by (run_id, command_type, request_idempotency_key).
CREATE UNIQUE INDEX IF NOT EXISTS idx_commands_idempotency
    ON commands(run_id, command_type, request_idempotency_key)
    WHERE request_idempotency_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS events (
    event_id     TEXT PRIMARY KEY,
    host_id      TEXT,
    job_id       TEXT,
    run_id       TEXT,
    source       TEXT NOT NULL,
    event_type   TEXT NOT NULL,
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    payload_json JSONB
);

CREATE INDEX IF NOT EXISTS idx_events_job_id ON events(job_id);
CREATE INDEX IF NOT EXISTS idx_events_run_id ON events(run_id);

CREATE TABLE IF NOT EXISTS run_output_snapshots (
    run_id      TEXT PRIMARY KEY REFERENCES runs(run_id),
    host_id     TEXT NOT NULL REFERENCES hosts(host_id),
    captured_at TIMESTAMPTZ NOT NULL,
    line_count  INTEGER NOT NULL DEFAULT 0,
    stale       BOOLEAN NOT NULL DEFAULT false,
    output_text TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
