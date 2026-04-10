package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/xlyk/triptych/internal/db"
	"github.com/xlyk/triptych/internal/domain"
)

const (
	hostHealthOnline  = "online"
	hostHealthStale   = "stale"
	hostHealthOffline = "offline"

	defaultWindowName = "main"
	hostStaleAfter    = 45 * time.Second
	hostOfflineAfter  = 2 * time.Minute
)

type Store interface {
	ListHosts(context.Context) ([]domain.Host, error)
	GetHost(context.Context, domain.HostID) (*domain.Host, error)
	UpsertHost(context.Context, *domain.Host) error
	UpdateHostHeartbeat(context.Context, domain.HostID, bool) error

	GetJob(context.Context, domain.JobID) (*domain.Job, error)
	GetJobByIdempotencyKey(context.Context, string) (*domain.Job, error)
	ListJobs(context.Context, *domain.JobStatus) ([]domain.Job, error)
	UpdateJobStatus(context.Context, domain.JobID, domain.JobStatus) error
	CreateJobWithInitialRun(context.Context, *domain.Job, *domain.Run) error

	GetRun(context.Context, domain.RunID) (*domain.Run, error)
	GetLatestRunByJob(context.Context, domain.JobID) (*domain.Run, error)
	UpdateRunState(context.Context, domain.RunID, db.RunStateUpdate) error
	SetRunStopRequested(context.Context, domain.RunID, bool) error
	ListLaunchableJobRunsByHost(context.Context, domain.HostID) ([]db.JobRun, error)
	ListActiveJobRunsByHost(context.Context, domain.HostID) ([]db.JobRun, error)

	CreateCommand(context.Context, *domain.Command) error
	GetCommand(context.Context, domain.CommandID) (*domain.Command, error)
	GetCommandByIdempotencyKey(context.Context, domain.RunID, domain.CommandType, string) (*domain.Command, error)
	UpdateCommandState(context.Context, domain.CommandID, domain.CommandState) error
	ListPendingCommandsByHost(context.Context, domain.HostID) ([]domain.Command, error)

	CreateEvent(context.Context, *domain.Event) error

	UpsertOutputSnapshot(context.Context, *domain.OutputSnapshot) error
	GetOutputSnapshot(context.Context, domain.RunID) (*domain.OutputSnapshot, error)
}

type Handler struct {
	store  Store
	logger *slog.Logger
	now    func() time.Time
}

func NewHandler(store Store, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{
		store:  store,
		logger: logger,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/hosts", h.handleListHosts)
	mux.HandleFunc("GET /v1/hosts/{host_id}", h.handleGetHost)
	mux.HandleFunc("POST /v1/hosts/register", h.handleRegisterHost)
	mux.HandleFunc("POST /v1/hosts/{host_id}/heartbeat", h.handleHeartbeat)
	mux.HandleFunc("GET /v1/hosts/{host_id}/work", h.handleGetHostWork)

	mux.HandleFunc("POST /v1/jobs", h.handleCreateJob)
	mux.HandleFunc("GET /v1/jobs", h.handleListJobs)
	mux.HandleFunc("GET /v1/jobs/{job_id}", h.handleGetJob)
	mux.HandleFunc("GET /v1/jobs/{job_id}/tail", h.handleGetJobTail)
	mux.HandleFunc("GET /v1/jobs/{job_id}/attach", h.handleGetJobAttach)
	mux.HandleFunc("POST /v1/jobs/{job_id}/commands/send", h.handleSendCommand)
	mux.HandleFunc("POST /v1/jobs/{job_id}/commands/interrupt", h.handleInterruptCommand)
	mux.HandleFunc("POST /v1/jobs/{job_id}/commands/stop", h.handleStopCommand)

	mux.HandleFunc("POST /v1/events", h.handleCreateEvent)
	mux.HandleFunc("POST /v1/commands/{command_id}/ack", h.handleAckCommand)
	mux.HandleFunc("POST /v1/commands/{command_id}/observe", h.handleObserveCommand)
	mux.HandleFunc("POST /v1/runs/{run_id}/state", h.handleUpdateRunState)
	mux.HandleFunc("POST /v1/runs/{run_id}/snapshot", h.handleUpdateSnapshot)

	return recoverMiddleware(loggingMiddleware(logger, mux))
}

type envelope struct {
	OK    bool       `json:"ok"`
	Data  any        `json:"data,omitempty"`
	Error *errorBody `json:"error,omitempty"`
}

type errorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

type apiError struct {
	Status  int
	Code    string
	Message string
	Details map[string]string
}

func (e *apiError) Error() string {
	return e.Message
}

type hostSummary struct {
	domain.Host
	Health string `json:"health"`
}

type jobSummary struct {
	Job        domain.Job  `json:"job"`
	Run        *domain.Run `json:"run,omitempty"`
	HostHealth string      `json:"host_health"`
}

type createJobResponse struct {
	Job domain.Job `json:"job"`
	Run domain.Run `json:"run"`
}

type tailResponse struct {
	JobID    domain.JobID          `json:"job_id"`
	Snapshot domain.OutputSnapshot `json:"snapshot"`
}

type attachResponse struct {
	JobID  domain.JobID  `json:"job_id"`
	HostID domain.HostID `json:"host_id"`
	Tmux   struct {
		SessionName string `json:"session_name"`
		WindowName  string `json:"window_name"`
	} `json:"tmux"`
	Attach struct {
		SSHTarget string `json:"ssh_target"`
		Command   string `json:"command"`
	} `json:"attach"`
}

type commandResponse struct {
	Command domain.Command `json:"command"`
}

type workResponse struct {
	HostID          domain.HostID    `json:"host_id"`
	LaunchableJobs  []launchableJob  `json:"launchable_jobs"`
	ActiveRuns      []activeRun      `json:"active_runs"`
	PendingCommands []pendingCommand `json:"pending_commands"`
}

type launchableJob struct {
	JobID       domain.JobID    `json:"job_id"`
	RunID       domain.RunID    `json:"run_id"`
	Agent       domain.Agent    `json:"agent"`
	RepoPath    string          `json:"repo_path"`
	Workdir     string          `json:"workdir"`
	Goal        string          `json:"goal"`
	Priority    domain.Priority `json:"priority"`
	MaxDuration string          `json:"max_duration"`
}

type activeRun struct {
	RunID         domain.RunID     `json:"run_id"`
	JobID         domain.JobID     `json:"job_id"`
	JobStatus     domain.JobStatus `json:"job_status"`
	RunStatus     domain.RunStatus `json:"run_status"`
	Tmux          tmuxRef          `json:"tmux"`
	LastEventAt   *time.Time       `json:"last_event_at,omitempty"`
	StopRequested bool             `json:"stop_requested"`
}

type tmuxRef struct {
	SessionName string `json:"session_name,omitempty"`
	WindowName  string `json:"window_name,omitempty"`
}

type pendingCommand struct {
	CommandID   domain.CommandID   `json:"command_id"`
	RunID       domain.RunID       `json:"run_id"`
	CommandType domain.CommandType `json:"command_type"`
	Payload     any                `json:"payload,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
}

type registerHostRequest struct {
	HostID           domain.HostID     `json:"host_id"`
	Hostname         string            `json:"hostname"`
	Capabilities     []string          `json:"capabilities"`
	AllowedRepoRoots []string          `json:"allowed_repo_roots"`
	Labels           map[string]string `json:"labels"`
}

type createCommandRequest struct {
	Text           string `json:"text,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type createEventRequest struct {
	EventID    domain.EventID  `json:"event_id,omitempty"`
	HostID     domain.HostID   `json:"host_id,omitempty"`
	JobID      domain.JobID    `json:"job_id,omitempty"`
	RunID      domain.RunID    `json:"run_id,omitempty"`
	Source     string          `json:"source"`
	EventType  string          `json:"event_type"`
	OccurredAt *time.Time      `json:"occurred_at,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

type updateRunStateRequest struct {
	Status              domain.RunStatus            `json:"status"`
	TmuxSessionName     *string                     `json:"tmux_session_name,omitempty"`
	TmuxWindowName      *string                     `json:"tmux_window_name,omitempty"`
	StartedAt           *time.Time                  `json:"started_at,omitempty"`
	FinishedAt          *time.Time                  `json:"finished_at,omitempty"`
	LastEventAt         *time.Time                  `json:"last_event_at,omitempty"`
	StopRequested       *bool                       `json:"stop_requested,omitempty"`
	TerminalDisposition *domain.TerminalDisposition `json:"terminal_disposition,omitempty"`
}

type updateSnapshotRequest struct {
	HostID     domain.HostID `json:"host_id"`
	CapturedAt *time.Time    `json:"captured_at,omitempty"`
	LineCount  int           `json:"line_count"`
	Stale      bool          `json:"stale"`
	Output     string        `json:"output"`
}

func (h *Handler) handleListHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := h.store.ListHosts(r.Context())
	if err != nil {
		h.writeErr(w, err)
		return
	}
	out := make([]hostSummary, 0, len(hosts))
	for _, host := range hosts {
		out = append(out, hostSummary{Host: host, Health: computeHostHealth(host, h.now())})
	}
	h.writeOK(w, http.StatusOK, struct {
		Hosts []hostSummary `json:"hosts"`
	}{Hosts: out})
}

func (h *Handler) handleGetHost(w http.ResponseWriter, r *http.Request) {
	host, err := h.requireHost(r.Context(), domain.HostID(r.PathValue("host_id")))
	if err != nil {
		h.writeErr(w, err)
		return
	}
	h.writeOK(w, http.StatusOK, struct {
		Host hostSummary `json:"host"`
	}{Host: hostSummary{Host: *host, Health: computeHostHealth(*host, h.now())}})
}

func (h *Handler) handleRegisterHost(w http.ResponseWriter, r *http.Request) {
	var req registerHostRequest
	if err := decodeJSON(r, &req); err != nil {
		h.writeErr(w, err)
		return
	}
	if err := validateRegisterHostRequest(req); err != nil {
		h.writeErr(w, err)
		return
	}
	now := h.now()
	host := &domain.Host{
		HostID:           req.HostID,
		Hostname:         strings.TrimSpace(req.Hostname),
		Online:           true,
		LastHeartbeatAt:  &now,
		Capabilities:     append([]string(nil), req.Capabilities...),
		AllowedRepoRoots: append([]string(nil), req.AllowedRepoRoots...),
		Labels:           cloneMap(req.Labels),
	}
	if err := h.store.UpsertHost(r.Context(), host); err != nil {
		h.writeErr(w, err)
		return
	}
	h.writeOK(w, http.StatusOK, struct {
		Host hostSummary `json:"host"`
	}{Host: hostSummary{Host: *host, Health: computeHostHealth(*host, h.now())}})
}

func (h *Handler) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	hostID := domain.HostID(r.PathValue("host_id"))
	if err := hostID.Validate(); err != nil {
		h.writeErr(w, invalidArgument(err.Error()))
		return
	}
	if err := h.store.UpdateHostHeartbeat(r.Context(), hostID, true); err != nil {
		h.writeErr(w, err)
		return
	}
	host, err := h.requireHost(r.Context(), hostID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	h.writeOK(w, http.StatusOK, struct {
		Host hostSummary `json:"host"`
	}{Host: hostSummary{Host: *host, Health: computeHostHealth(*host, h.now())}})
}

func (h *Handler) handleGetHostWork(w http.ResponseWriter, r *http.Request) {
	hostID := domain.HostID(r.PathValue("host_id"))
	if _, err := h.requireHost(r.Context(), hostID); err != nil {
		h.writeErr(w, err)
		return
	}
	launchable, err := h.store.ListLaunchableJobRunsByHost(r.Context(), hostID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	active, err := h.store.ListActiveJobRunsByHost(r.Context(), hostID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	commands, err := h.store.ListPendingCommandsByHost(r.Context(), hostID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	resp := workResponse{
		HostID:          hostID,
		LaunchableJobs:  make([]launchableJob, 0, len(launchable)),
		ActiveRuns:      make([]activeRun, 0, len(active)),
		PendingCommands: make([]pendingCommand, 0, len(commands)),
	}
	for _, item := range launchable {
		resp.LaunchableJobs = append(resp.LaunchableJobs, launchableJob{
			JobID:       item.Job.JobID,
			RunID:       item.Run.RunID,
			Agent:       item.Job.Agent,
			RepoPath:    item.Job.RepoPath,
			Workdir:     item.Job.Workdir,
			Goal:        item.Job.Goal,
			Priority:    item.Job.Priority,
			MaxDuration: item.Job.MaxDuration,
		})
	}
	for _, item := range active {
		resp.ActiveRuns = append(resp.ActiveRuns, activeRun{
			RunID:         item.Run.RunID,
			JobID:         item.Job.JobID,
			JobStatus:     item.Job.Status,
			RunStatus:     item.Run.Status,
			Tmux:          tmuxRef{SessionName: item.Run.TmuxSessionName, WindowName: normalizeWindowName(item.Run.TmuxWindowName)},
			LastEventAt:   item.Run.LastEventAt,
			StopRequested: item.Run.StopRequested,
		})
	}
	for _, cmd := range commands {
		resp.PendingCommands = append(resp.PendingCommands, pendingCommand{
			CommandID:   cmd.CommandID,
			RunID:       cmd.RunID,
			CommandType: cmd.CommandType,
			Payload:     decodePayload(cmd.PayloadJSON),
			CreatedAt:   cmd.CreatedAt,
		})
	}
	h.writeOK(w, http.StatusOK, resp)
}

func (h *Handler) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req domain.JobCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		h.writeErr(w, err)
		return
	}
	req = req.Normalize()
	if err := req.Validate(); err != nil {
		h.writeErr(w, invalidArgument(err.Error()))
		return
	}
	host, err := h.requireHost(r.Context(), req.HostID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	if err := validateJobAgainstHost(req, *host); err != nil {
		h.writeErr(w, err)
		return
	}
	if req.IdempotencyKey != "" {
		existing, err := h.store.GetJobByIdempotencyKey(r.Context(), req.IdempotencyKey)
		if err == nil {
			if !sameJobRequest(req, *existing) {
				h.writeErr(w, &apiError{
					Status:  http.StatusConflict,
					Code:    "conflict",
					Message: "job idempotency key already used for a different request",
				})
				return
			}
			run, err := h.store.GetLatestRunByJob(r.Context(), existing.JobID)
			if err != nil {
				h.writeErr(w, err)
				return
			}
			h.writeOK(w, http.StatusOK, createJobResponse{Job: *existing, Run: *run})
			return
		}
		if !errors.Is(err, db.ErrNotFound) {
			h.writeErr(w, err)
			return
		}
	}

	job := &domain.Job{
		JobID:          domain.JobID(newID("job")),
		HostID:         req.HostID,
		Agent:          req.Agent,
		Status:         domain.JobStatusAssigned,
		RepoPath:       req.RepoPath,
		Workdir:        req.Workdir,
		Goal:           req.Goal,
		Priority:       req.Priority,
		MaxDuration:    req.MaxDuration,
		IdempotencyKey: req.IdempotencyKey,
		Metadata:       cloneMap(req.Metadata),
	}
	run := &domain.Run{
		RunID:          domain.RunID(newID("run")),
		JobID:          job.JobID,
		HostID:         job.HostID,
		Status:         domain.RunStatusPendingLaunch,
		TmuxWindowName: defaultWindowName,
	}
	if err := h.store.CreateJobWithInitialRun(r.Context(), job, run); err != nil {
		h.writeErr(w, err)
		return
	}
	h.writeOK(w, http.StatusCreated, createJobResponse{Job: *job, Run: *run})
}

func (h *Handler) handleListJobs(w http.ResponseWriter, r *http.Request) {
	var statusFilter *domain.JobStatus
	if raw := strings.TrimSpace(r.URL.Query().Get("status")); raw != "" {
		status := domain.JobStatus(raw)
		if err := status.Validate(); err != nil {
			h.writeErr(w, &apiError{
				Status:  http.StatusBadRequest,
				Code:    "invalid_argument",
				Message: "invalid status filter",
				Details: map[string]string{"status": err.Error()},
			})
			return
		}
		statusFilter = &status
	}
	jobs, err := h.store.ListJobs(r.Context(), statusFilter)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	out := make([]jobSummary, 0, len(jobs))
	for _, job := range jobs {
		host, err := h.store.GetHost(r.Context(), job.HostID)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		item := jobSummary{Job: job, HostHealth: computeHostHealth(*host, h.now())}
		run, err := h.store.GetLatestRunByJob(r.Context(), job.JobID)
		if err == nil {
			item.Run = run
		} else if !errors.Is(err, db.ErrNotFound) {
			h.writeErr(w, err)
			return
		}
		out = append(out, item)
	}
	h.writeOK(w, http.StatusOK, struct {
		Jobs []jobSummary `json:"jobs"`
	}{Jobs: out})
}

func (h *Handler) handleGetJob(w http.ResponseWriter, r *http.Request) {
	job, err := h.requireJob(r.Context(), domain.JobID(r.PathValue("job_id")))
	if err != nil {
		h.writeErr(w, err)
		return
	}
	host, err := h.requireHost(r.Context(), job.HostID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	resp := jobSummary{
		Job:        *job,
		HostHealth: computeHostHealth(*host, h.now()),
	}
	run, err := h.store.GetLatestRunByJob(r.Context(), job.JobID)
	if err == nil {
		resp.Run = run
	} else if !errors.Is(err, db.ErrNotFound) {
		h.writeErr(w, err)
		return
	}
	h.writeOK(w, http.StatusOK, struct {
		Job jobSummary `json:"job"`
	}{Job: resp})
}

func (h *Handler) handleGetJobTail(w http.ResponseWriter, r *http.Request) {
	job, run, err := h.requireJobAndRun(r.Context(), domain.JobID(r.PathValue("job_id")))
	if err != nil {
		h.writeErr(w, err)
		return
	}
	snapshot, err := h.store.GetOutputSnapshot(r.Context(), run.RunID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			h.writeErr(w, &apiError{
				Status:  http.StatusNotFound,
				Code:    "not_found",
				Message: "output snapshot not found",
			})
			return
		}
		h.writeErr(w, err)
		return
	}
	h.writeOK(w, http.StatusOK, tailResponse{JobID: job.JobID, Snapshot: *snapshot})
}

func (h *Handler) handleGetJobAttach(w http.ResponseWriter, r *http.Request) {
	job, run, err := h.requireJobAndRun(r.Context(), domain.JobID(r.PathValue("job_id")))
	if err != nil {
		h.writeErr(w, err)
		return
	}
	if !isLiveRun(*run) || strings.TrimSpace(run.TmuxSessionName) == "" {
		h.writeErr(w, &apiError{
			Status:  http.StatusConflict,
			Code:    "conflict",
			Message: "job has no known live tmux-backed runtime",
		})
		return
	}
	resp := attachResponse{JobID: job.JobID, HostID: job.HostID}
	resp.Tmux.SessionName = run.TmuxSessionName
	resp.Tmux.WindowName = normalizeWindowName(run.TmuxWindowName)
	resp.Attach.SSHTarget = job.HostID.String()
	resp.Attach.Command = fmt.Sprintf("ssh %s 'tmux attach -t %s'", job.HostID, run.TmuxSessionName)
	h.writeOK(w, http.StatusOK, resp)
}

func (h *Handler) handleSendCommand(w http.ResponseWriter, r *http.Request) {
	h.handleCreateJobCommand(w, r, domain.CommandTypeSend)
}

func (h *Handler) handleInterruptCommand(w http.ResponseWriter, r *http.Request) {
	h.handleCreateJobCommand(w, r, domain.CommandTypeInterrupt)
}

func (h *Handler) handleStopCommand(w http.ResponseWriter, r *http.Request) {
	h.handleCreateJobCommand(w, r, domain.CommandTypeStop)
}

func (h *Handler) handleCreateJobCommand(w http.ResponseWriter, r *http.Request, commandType domain.CommandType) {
	_, run, err := h.requireJobAndRun(r.Context(), domain.JobID(r.PathValue("job_id")))
	if err != nil {
		h.writeErr(w, err)
		return
	}
	var req createCommandRequest
	if err := decodeJSONAllowEmpty(r, &req); err != nil {
		h.writeErr(w, err)
		return
	}
	if err := validateCommandRequest(commandType, req); err != nil {
		h.writeErr(w, err)
		return
	}
	if err := validateCommandAllowed(commandType, *run); err != nil {
		h.writeErr(w, err)
		return
	}

	payloadJSON := "{}"
	if commandType == domain.CommandTypeSend {
		payloadJSON = fmt.Sprintf(`{"text":%q}`, strings.TrimSpace(req.Text))
	}
	if req.IdempotencyKey != "" {
		existing, err := h.store.GetCommandByIdempotencyKey(r.Context(), run.RunID, commandType, req.IdempotencyKey)
		if err == nil {
			if existing.PayloadJSON != payloadJSON {
				h.writeErr(w, &apiError{
					Status:  http.StatusConflict,
					Code:    "conflict",
					Message: "command idempotency key already used for a different payload",
				})
				return
			}
			if commandType == domain.CommandTypeStop {
				if err := h.store.SetRunStopRequested(r.Context(), run.RunID, true); err != nil {
					h.writeErr(w, err)
					return
				}
			}
			h.writeOK(w, http.StatusOK, commandResponse{Command: *existing})
			return
		}
		if !errors.Is(err, db.ErrNotFound) {
			h.writeErr(w, err)
			return
		}
	}

	cmd := &domain.Command{
		CommandID:             domain.CommandID(newID("cmd")),
		JobID:                 run.JobID,
		RunID:                 run.RunID,
		HostID:                run.HostID,
		CommandType:           commandType,
		RequestIdempotencyKey: req.IdempotencyKey,
		PayloadJSON:           payloadJSON,
		State:                 domain.CommandStateRecorded,
	}
	if err := h.store.CreateCommand(r.Context(), cmd); err != nil {
		h.writeErr(w, err)
		return
	}
	if commandType == domain.CommandTypeStop {
		if err := h.store.SetRunStopRequested(r.Context(), run.RunID, true); err != nil {
			h.writeErr(w, err)
			return
		}
	}
	h.writeOK(w, http.StatusCreated, commandResponse{Command: *cmd})
}

func (h *Handler) handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	var req createEventRequest
	if err := decodeJSON(r, &req); err != nil {
		h.writeErr(w, err)
		return
	}
	if err := validateEventRequest(req); err != nil {
		h.writeErr(w, err)
		return
	}
	occurredAt := h.now()
	if req.OccurredAt != nil {
		occurredAt = req.OccurredAt.UTC()
	}
	eventID := req.EventID
	if eventID == "" {
		eventID = domain.EventID(newID("evt"))
	}
	event := &domain.Event{
		EventID:     eventID,
		HostID:      req.HostID,
		JobID:       req.JobID,
		RunID:       req.RunID,
		Source:      strings.TrimSpace(req.Source),
		EventType:   strings.TrimSpace(req.EventType),
		OccurredAt:  occurredAt,
		PayloadJSON: string(req.Payload),
	}
	if err := h.store.CreateEvent(r.Context(), event); err != nil {
		h.writeErr(w, err)
		return
	}
	h.writeOK(w, http.StatusCreated, struct {
		Event domain.Event `json:"event"`
	}{Event: *event})
}

func (h *Handler) handleAckCommand(w http.ResponseWriter, r *http.Request) {
	h.handleAdvanceCommandState(w, r, domain.CommandStateAcknowledged)
}

func (h *Handler) handleObserveCommand(w http.ResponseWriter, r *http.Request) {
	h.handleAdvanceCommandState(w, r, domain.CommandStateObserved)
}

func (h *Handler) handleAdvanceCommandState(w http.ResponseWriter, r *http.Request, target domain.CommandState) {
	command, err := h.requireCommand(r.Context(), domain.CommandID(r.PathValue("command_id")))
	if err != nil {
		h.writeErr(w, err)
		return
	}
	next := target
	if command.State == domain.CommandStateObserved || command.State == target {
		h.writeOK(w, http.StatusOK, commandResponse{Command: *command})
		return
	}
	if target == domain.CommandStateAcknowledged && command.State == domain.CommandStateObserved {
		next = domain.CommandStateObserved
	}
	if err := h.store.UpdateCommandState(r.Context(), command.CommandID, next); err != nil {
		h.writeErr(w, err)
		return
	}
	command.State = next
	h.writeOK(w, http.StatusOK, commandResponse{Command: *command})
}

func (h *Handler) handleUpdateRunState(w http.ResponseWriter, r *http.Request) {
	runID := domain.RunID(r.PathValue("run_id"))
	if _, err := h.requireRun(r.Context(), runID); err != nil {
		h.writeErr(w, err)
		return
	}
	var req updateRunStateRequest
	if err := decodeJSON(r, &req); err != nil {
		h.writeErr(w, err)
		return
	}
	if err := req.Status.Validate(); err != nil {
		h.writeErr(w, invalidArgument(err.Error()))
		return
	}
	if req.TerminalDisposition != nil {
		switch *req.TerminalDisposition {
		case domain.TerminalDispositionCompleted, domain.TerminalDispositionFailed, domain.TerminalDispositionCancelled:
		default:
			h.writeErr(w, invalidArgument("invalid terminal_disposition"))
			return
		}
	}
	update := db.RunStateUpdate{
		Status:              req.Status,
		TmuxSessionName:     req.TmuxSessionName,
		TmuxWindowName:      req.TmuxWindowName,
		StartedAt:           req.StartedAt,
		FinishedAt:          req.FinishedAt,
		LastEventAt:         req.LastEventAt,
		StopRequested:       req.StopRequested,
		TerminalDisposition: req.TerminalDisposition,
	}
	if err := h.store.UpdateRunState(r.Context(), runID, update); err != nil {
		h.writeErr(w, err)
		return
	}
	run, err := h.requireRun(r.Context(), runID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	jobStatus := deriveJobStatus(*run)
	if err := h.store.UpdateJobStatus(r.Context(), run.JobID, jobStatus); err != nil {
		h.writeErr(w, err)
		return
	}
	job, err := h.requireJob(r.Context(), run.JobID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	h.writeOK(w, http.StatusOK, createJobResponse{Job: *job, Run: *run})
}

func (h *Handler) handleUpdateSnapshot(w http.ResponseWriter, r *http.Request) {
	runID := domain.RunID(r.PathValue("run_id"))
	run, err := h.requireRun(r.Context(), runID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	var req updateSnapshotRequest
	if err := decodeJSON(r, &req); err != nil {
		h.writeErr(w, err)
		return
	}
	if err := req.HostID.Validate(); err != nil {
		h.writeErr(w, invalidArgument(err.Error()))
		return
	}
	if req.LineCount < 0 {
		h.writeErr(w, invalidArgument("line_count must be non-negative"))
		return
	}
	if req.HostID != run.HostID {
		h.writeErr(w, &apiError{
			Status:  http.StatusConflict,
			Code:    "conflict",
			Message: "snapshot host_id does not match run host_id",
		})
		return
	}
	capturedAt := h.now()
	if req.CapturedAt != nil {
		capturedAt = req.CapturedAt.UTC()
	}
	snapshot := &domain.OutputSnapshot{
		RunID:      runID,
		HostID:     req.HostID,
		CapturedAt: capturedAt,
		LineCount:  req.LineCount,
		Stale:      req.Stale,
		OutputText: req.Output,
	}
	if err := h.store.UpsertOutputSnapshot(r.Context(), snapshot); err != nil {
		h.writeErr(w, err)
		return
	}
	h.writeOK(w, http.StatusOK, struct {
		Snapshot domain.OutputSnapshot `json:"snapshot"`
	}{Snapshot: *snapshot})
}

func (h *Handler) requireHost(ctx context.Context, hostID domain.HostID) (*domain.Host, error) {
	if err := hostID.Validate(); err != nil {
		return nil, invalidArgument(err.Error())
	}
	host, err := h.store.GetHost(ctx, hostID)
	if err != nil {
		return nil, err
	}
	return host, nil
}

func (h *Handler) requireJob(ctx context.Context, jobID domain.JobID) (*domain.Job, error) {
	if err := jobID.Validate(); err != nil {
		return nil, invalidArgument(err.Error())
	}
	job, err := h.store.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return job, nil
}

func (h *Handler) requireRun(ctx context.Context, runID domain.RunID) (*domain.Run, error) {
	if err := runID.Validate(); err != nil {
		return nil, invalidArgument(err.Error())
	}
	run, err := h.store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	return run, nil
}

func (h *Handler) requireCommand(ctx context.Context, commandID domain.CommandID) (*domain.Command, error) {
	if err := commandID.Validate(); err != nil {
		return nil, invalidArgument(err.Error())
	}
	command, err := h.store.GetCommand(ctx, commandID)
	if err != nil {
		return nil, err
	}
	return command, nil
}

func (h *Handler) requireJobAndRun(ctx context.Context, jobID domain.JobID) (*domain.Job, *domain.Run, error) {
	job, err := h.requireJob(ctx, jobID)
	if err != nil {
		return nil, nil, err
	}
	run, err := h.store.GetLatestRunByJob(ctx, job.JobID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, nil, &apiError{
				Status:  http.StatusNotFound,
				Code:    "not_found",
				Message: "run not found",
			}
		}
		return nil, nil, err
	}
	return job, run, nil
}

func (h *Handler) writeOK(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, envelope{OK: true, Data: data})
}

func (h *Handler) writeErr(w http.ResponseWriter, err error) {
	var apiErr *apiError
	switch {
	case errors.As(err, &apiErr):
		writeJSON(w, apiErr.Status, envelope{
			OK: false,
			Error: &errorBody{
				Code:    apiErr.Code,
				Message: apiErr.Message,
				Details: apiErr.Details,
			},
		})
	case errors.Is(err, db.ErrNotFound):
		writeJSON(w, http.StatusNotFound, envelope{
			OK:    false,
			Error: &errorBody{Code: "not_found", Message: "resource not found"},
		})
	case errors.Is(err, db.ErrConflict):
		writeJSON(w, http.StatusConflict, envelope{
			OK:    false,
			Error: &errorBody{Code: "conflict", Message: "resource conflict"},
		})
	default:
		h.logger.Error("request failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, envelope{
			OK:    false,
			Error: &errorBody{Code: "internal_error", Message: "internal server error"},
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return invalidArgument("request body is required")
	}
	defer func() {
		_ = r.Body.Close()
	}()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return invalidArgument("invalid JSON body")
	}
	if dec.More() {
		return invalidArgument("request body must contain a single JSON object")
	}
	return nil
}

func decodeJSONAllowEmpty(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	defer func() {
		_ = r.Body.Close()
	}()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		if strings.Contains(err.Error(), "EOF") {
			return nil
		}
		return invalidArgument("invalid JSON body")
	}
	if dec.More() {
		return invalidArgument("request body must contain a single JSON object")
	}
	return nil
}

func validateRegisterHostRequest(req registerHostRequest) error {
	if err := req.HostID.Validate(); err != nil {
		return invalidArgument(err.Error())
	}
	if strings.TrimSpace(req.Hostname) == "" {
		return invalidArgument("hostname is required")
	}
	for _, root := range req.AllowedRepoRoots {
		if !filepath.IsAbs(root) {
			return invalidArgument("allowed_repo_roots must be absolute")
		}
	}
	if req.Labels == nil {
		req.Labels = map[string]string{}
	}
	return nil
}

func validateJobAgainstHost(req domain.JobCreateRequest, host domain.Host) error {
	if !hostSupportsAgent(host, req.Agent) {
		return &apiError{
			Status:  http.StatusConflict,
			Code:    "conflict",
			Message: "host does not support requested agent",
		}
	}
	if !pathWithinRoots(req.RepoPath, host.AllowedRepoRoots) {
		return invalidArgument("repo_path must be inside an allowed repo root")
	}
	if !pathWithinRoots(req.Workdir, host.AllowedRepoRoots) {
		return invalidArgument("workdir must be inside an allowed repo root")
	}
	return nil
}

func sameJobRequest(req domain.JobCreateRequest, job domain.Job) bool {
	return req.Agent == job.Agent &&
		req.HostID == job.HostID &&
		req.RepoPath == job.RepoPath &&
		req.Workdir == job.Workdir &&
		req.Goal == job.Goal &&
		req.Priority == job.Priority &&
		req.MaxDuration == job.MaxDuration &&
		mapsEqual(req.Metadata, job.Metadata)
}

func validateCommandRequest(commandType domain.CommandType, req createCommandRequest) error {
	if strings.TrimSpace(req.IdempotencyKey) == "" && req.IdempotencyKey != "" {
		return invalidArgument("idempotency_key must be non-empty when provided")
	}
	if commandType == domain.CommandTypeSend && strings.TrimSpace(req.Text) == "" {
		return invalidArgument("text is required")
	}
	return nil
}

func validateCommandAllowed(commandType domain.CommandType, run domain.Run) error {
	switch commandType {
	case domain.CommandTypeSend:
		if run.Status != domain.RunStatusActive && run.Status != domain.RunStatusWaiting {
			return &apiError{
				Status:  http.StatusConflict,
				Code:    "conflict",
				Message: "send is only allowed for active or waiting runs",
			}
		}
	case domain.CommandTypeInterrupt, domain.CommandTypeStop:
		if !isLiveRun(run) {
			return &apiError{
				Status:  http.StatusConflict,
				Code:    "conflict",
				Message: "command requires a live run",
			}
		}
	}
	return nil
}

func validateEventRequest(req createEventRequest) error {
	if req.EventID != "" {
		if err := req.EventID.Validate(); err != nil {
			return invalidArgument(err.Error())
		}
	}
	if strings.TrimSpace(req.Source) == "" {
		return invalidArgument("source is required")
	}
	if strings.TrimSpace(req.EventType) == "" {
		return invalidArgument("event_type is required")
	}
	return nil
}

func deriveJobStatus(run domain.Run) domain.JobStatus {
	switch run.Status {
	case domain.RunStatusPendingLaunch:
		return domain.JobStatusAssigned
	case domain.RunStatusStarting:
		return domain.JobStatusLaunching
	case domain.RunStatusActive, domain.RunStatusStopping:
		return domain.JobStatusRunning
	case domain.RunStatusWaiting:
		return domain.JobStatusWaitingForInput
	case domain.RunStatusExited, domain.RunStatusCrashed:
		switch resolveDisposition(run) {
		case domain.TerminalDispositionCompleted:
			return domain.JobStatusCompleted
		case domain.TerminalDispositionCancelled:
			return domain.JobStatusCancelled
		default:
			return domain.JobStatusFailed
		}
	default:
		return domain.JobStatusAssigned
	}
}

func resolveDisposition(run domain.Run) domain.TerminalDisposition {
	if run.TerminalDisposition != "" {
		return run.TerminalDisposition
	}
	if run.StopRequested {
		return domain.TerminalDispositionCancelled
	}
	return domain.TerminalDispositionFailed
}

func computeHostHealth(host domain.Host, now time.Time) string {
	if host.LastHeartbeatAt == nil {
		if host.Online {
			return hostHealthOnline
		}
		return hostHealthOffline
	}
	age := now.Sub(host.LastHeartbeatAt.UTC())
	switch {
	case age <= hostStaleAfter:
		return hostHealthOnline
	case age <= hostOfflineAfter:
		return hostHealthStale
	default:
		return hostHealthOffline
	}
}

func hostSupportsAgent(host domain.Host, agent domain.Agent) bool {
	want := string(agent)
	for _, capability := range host.Capabilities {
		switch {
		case capability == want:
			return true
		case agent == domain.AgentClaude && capability == "claude_code":
			return true
		}
	}
	return false
}

func pathWithinRoots(path string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		if rel == "." || (!strings.HasPrefix(rel, "..") && rel != "..") {
			return true
		}
	}
	return false
}

func normalizeWindowName(name string) string {
	if strings.TrimSpace(name) == "" {
		return defaultWindowName
	}
	return name
}

func decodePayload(payload string) any {
	if strings.TrimSpace(payload) == "" || payload == "{}" {
		return nil
	}
	var out any
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		return payload
	}
	return out
}

func isLiveRun(run domain.Run) bool {
	switch run.Status {
	case domain.RunStatusPendingLaunch, domain.RunStatusExited, domain.RunStatusCrashed:
		return false
	default:
		return true
	}
}

func invalidArgument(message string) error {
	return &apiError{
		Status:  http.StatusBadRequest,
		Code:    "invalid_argument",
		Message: message,
	}
}

func newID(prefix string) string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(buf[:])
}

func cloneMap(src map[string]string) map[string]string {
	if src == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Info("http request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				writeJSON(w, http.StatusInternalServerError, envelope{
					OK:    false,
					Error: &errorBody{Code: "internal_error", Message: "internal server error"},
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
