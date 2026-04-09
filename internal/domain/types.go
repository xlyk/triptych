package domain

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type HostID string
type JobID string
type RunID string
type CommandID string
type EventID string

func (id HostID) String() string    { return string(id) }
func (id JobID) String() string     { return string(id) }
func (id RunID) String() string     { return string(id) }
func (id CommandID) String() string { return string(id) }
func (id EventID) String() string   { return string(id) }

func (id HostID) Validate() error    { return validateRequiredID("host_id", id.String()) }
func (id JobID) Validate() error     { return validateRequiredID("job_id", id.String()) }
func (id RunID) Validate() error     { return validateRequiredID("run_id", id.String()) }
func (id CommandID) Validate() error { return validateRequiredID("command_id", id.String()) }
func (id EventID) Validate() error   { return validateRequiredID("event_id", id.String()) }

func validateRequiredID(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	return nil
}

type Agent string

const (
	AgentClaude Agent = "claude"
	AgentCodex  Agent = "codex"
)

func (a Agent) Validate() error {
	switch a {
	case AgentClaude, AgentCodex:
		return nil
	default:
		return fmt.Errorf("agent must be one of %q or %q", AgentClaude, AgentCodex)
	}
}

type JobStatus string

const (
	JobStatusQueued          JobStatus = "queued"
	JobStatusAssigned        JobStatus = "assigned"
	JobStatusLaunching       JobStatus = "launching"
	JobStatusRunning         JobStatus = "running"
	JobStatusWaitingForInput JobStatus = "waiting_for_input"
	JobStatusBlocked         JobStatus = "blocked"
	JobStatusCompleted       JobStatus = "completed"
	JobStatusFailed          JobStatus = "failed"
	JobStatusCancelled       JobStatus = "cancelled"
	JobStatusArchived        JobStatus = "archived"
)

func (s JobStatus) Validate() error {
	switch s {
	case JobStatusQueued,
		JobStatusAssigned,
		JobStatusLaunching,
		JobStatusRunning,
		JobStatusWaitingForInput,
		JobStatusBlocked,
		JobStatusCompleted,
		JobStatusFailed,
		JobStatusCancelled,
		JobStatusArchived:
		return nil
	default:
		return fmt.Errorf("invalid job status %q", s)
	}
}

type RunStatus string

const (
	RunStatusPendingLaunch RunStatus = "pending_launch"
	RunStatusStarting      RunStatus = "starting"
	RunStatusActive        RunStatus = "active"
	RunStatusWaiting       RunStatus = "waiting"
	RunStatusStopping      RunStatus = "stopping"
	RunStatusExited        RunStatus = "exited"
	RunStatusCrashed       RunStatus = "crashed"
)

func (s RunStatus) Validate() error {
	switch s {
	case RunStatusPendingLaunch,
		RunStatusStarting,
		RunStatusActive,
		RunStatusWaiting,
		RunStatusStopping,
		RunStatusExited,
		RunStatusCrashed:
		return nil
	default:
		return fmt.Errorf("invalid run status %q", s)
	}
}

type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
)

func (p Priority) Validate() error {
	switch p {
	case PriorityLow, PriorityNormal, PriorityHigh:
		return nil
	default:
		return fmt.Errorf("invalid priority %q", p)
	}
}

type JobCreateRequest struct {
	Agent          Agent             `json:"agent"`
	HostID         HostID            `json:"host_id"`
	RepoPath       string            `json:"repo_path"`
	Goal           string            `json:"goal"`
	Workdir        string            `json:"workdir,omitempty"`
	Priority       Priority          `json:"priority,omitempty"`
	MaxDuration    string            `json:"max_duration,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

func ValidateJobCreateRequest(r JobCreateRequest) error {
	return r.Validate()
}

func (r JobCreateRequest) Validate() error {
	var errs []error

	if err := r.Agent.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := r.HostID.Validate(); err != nil {
		errs = append(errs, err)
	}
	if !filepath.IsAbs(r.RepoPath) {
		errs = append(errs, errors.New("repo_path must be absolute"))
	}
	if strings.TrimSpace(r.Goal) == "" {
		errs = append(errs, errors.New("goal is required"))
	}
	if r.Workdir != "" && !filepath.IsAbs(r.Workdir) {
		errs = append(errs, errors.New("workdir must be absolute"))
	}
	if r.Priority != "" {
		if err := r.Priority.Validate(); err != nil {
			errs = append(errs, err)
		}
	}
	if r.MaxDuration != "" {
		d, err := time.ParseDuration(r.MaxDuration)
		if err != nil {
			errs = append(errs, errors.New("max_duration must be a valid duration"))
		} else if d <= 0 {
			errs = append(errs, errors.New("max_duration must be positive"))
		}
	}
	if r.IdempotencyKey != "" && strings.TrimSpace(r.IdempotencyKey) == "" {
		errs = append(errs, errors.New("idempotency_key must be non-empty when provided"))
	}

	return errors.Join(errs...)
}

func (r JobCreateRequest) Normalize() JobCreateRequest {
	out := r
	out.Goal = strings.TrimSpace(out.Goal)
	if out.Workdir == "" {
		out.Workdir = out.RepoPath
	}
	if out.Priority == "" {
		out.Priority = PriorityNormal
	}
	if out.MaxDuration == "" {
		out.MaxDuration = "4h"
	}
	if out.Metadata == nil {
		out.Metadata = map[string]string{}
	}
	return out
}

type CommandType string

const (
	CommandTypeSend      CommandType = "send"
	CommandTypeInterrupt CommandType = "interrupt"
	CommandTypeStop      CommandType = "stop"
)

func (t CommandType) Validate() error {
	switch t {
	case CommandTypeSend, CommandTypeInterrupt, CommandTypeStop:
		return nil
	default:
		return fmt.Errorf("invalid command type %q", t)
	}
}

type MutatingCommandRequestIdentity struct {
	RunID                 RunID       `json:"run_id"`
	CommandType           CommandType `json:"command_type"`
	RequestIdempotencyKey string      `json:"request_idempotency_key,omitempty"`
	PayloadFingerprint    string      `json:"payload_fingerprint,omitempty"`
}

func ValidateMutatingCommandRequestIdentity(r MutatingCommandRequestIdentity) error {
	return r.Validate()
}

func (r MutatingCommandRequestIdentity) Validate() error {
	var errs []error

	if err := r.RunID.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := r.CommandType.Validate(); err != nil {
		errs = append(errs, err)
	}
	if strings.TrimSpace(r.RequestIdempotencyKey) == "" {
		errs = append(errs, errors.New("request_idempotency_key is required"))
	}
	if strings.TrimSpace(r.PayloadFingerprint) == "" {
		errs = append(errs, errors.New("payload_fingerprint is required"))
	}

	return errors.Join(errs...)
}
