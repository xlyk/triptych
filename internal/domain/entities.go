package domain

import "time"

// CommandState represents the lifecycle state of a command.
type CommandState string

const (
	CommandStateRecorded     CommandState = "recorded"
	CommandStateAcknowledged CommandState = "acknowledged"
	CommandStateObserved     CommandState = "observed"
)

// TerminalDisposition captures how a run ended.
type TerminalDisposition string

const (
	TerminalDispositionCompleted TerminalDisposition = "completed"
	TerminalDispositionFailed    TerminalDisposition = "failed"
	TerminalDispositionCancelled TerminalDisposition = "cancelled"
)

// Host is a registered daemon-managed execution machine.
type Host struct {
	HostID           HostID            `json:"host_id"`
	Hostname         string            `json:"hostname"`
	Online           bool              `json:"online"`
	LastHeartbeatAt  *time.Time        `json:"last_heartbeat_at,omitempty"`
	Capabilities     []string          `json:"capabilities"`
	AllowedRepoRoots []string          `json:"allowed_repo_roots"`
	Labels           map[string]string `json:"labels"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

// Job is the durable unit of requested work.
type Job struct {
	JobID          JobID             `json:"job_id"`
	HostID         HostID            `json:"host_id"`
	Agent          Agent             `json:"agent"`
	Status         JobStatus         `json:"status"`
	RepoPath       string            `json:"repo_path"`
	Workdir        string            `json:"workdir"`
	Goal           string            `json:"goal"`
	Priority       Priority          `json:"priority"`
	MaxDuration    string            `json:"max_duration"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	Metadata       map[string]string `json:"metadata"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// Run is one concrete execution attempt of a job.
type Run struct {
	RunID               RunID               `json:"run_id"`
	JobID               JobID               `json:"job_id"`
	HostID              HostID              `json:"host_id"`
	Status              RunStatus           `json:"status"`
	TmuxSessionName     string              `json:"tmux_session_name,omitempty"`
	TmuxWindowName      string              `json:"tmux_window_name,omitempty"`
	StartedAt           *time.Time          `json:"started_at,omitempty"`
	FinishedAt          *time.Time          `json:"finished_at,omitempty"`
	LastEventAt         *time.Time          `json:"last_event_at,omitempty"`
	StopRequested       bool                `json:"stop_requested"`
	TerminalDisposition TerminalDisposition `json:"terminal_disposition,omitempty"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
}

// Command is a mutating operator request against an active run.
type Command struct {
	CommandID             CommandID    `json:"command_id"`
	JobID                 JobID        `json:"job_id"`
	RunID                 RunID        `json:"run_id"`
	HostID                HostID       `json:"host_id"`
	CommandType           CommandType  `json:"command_type"`
	RequestIdempotencyKey string       `json:"request_idempotency_key,omitempty"`
	PayloadJSON           string       `json:"payload_json,omitempty"`
	State                 CommandState `json:"state"`
	CreatedAt             time.Time    `json:"created_at"`
	UpdatedAt             time.Time    `json:"updated_at"`
}

// Event is a canonical durable record of a lifecycle fact.
type Event struct {
	EventID     EventID   `json:"event_id"`
	HostID      HostID    `json:"host_id,omitempty"`
	JobID       JobID     `json:"job_id,omitempty"`
	RunID       RunID     `json:"run_id,omitempty"`
	Source      string    `json:"source"`
	EventType   string    `json:"event_type"`
	OccurredAt  time.Time `json:"occurred_at"`
	PayloadJSON string    `json:"payload_json,omitempty"`
}

// OutputSnapshot is the latest bounded normalized terminal snapshot for a run.
type OutputSnapshot struct {
	RunID      RunID     `json:"run_id"`
	HostID     HostID    `json:"host_id"`
	CapturedAt time.Time `json:"captured_at"`
	LineCount  int       `json:"line_count"`
	Stale      bool      `json:"stale"`
	OutputText string    `json:"output"`
	UpdatedAt  time.Time `json:"updated_at"`
}
