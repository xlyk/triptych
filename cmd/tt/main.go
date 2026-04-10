package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/xlyk/triptych/internal/client"
)

const defaultServerURL = "http://127.0.0.1:8080"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	jsonFlag := false
	filtered := args[:0:0]
	for _, a := range args {
		if a == "--json" {
			jsonFlag = true
		} else {
			filtered = append(filtered, a)
		}
	}
	args = filtered

	if len(args) < 2 {
		if err := writeLine(stderr, usage()); err != nil {
			return 1
		}
		return 1
	}

	resource := args[0]
	action := args[1]
	rest := args[2:]

	baseURL := os.Getenv("TRIPTYCH_SERVER_URL")
	if baseURL == "" {
		baseURL = defaultServerURL
	}
	c := client.New(baseURL)

	var (
		data json.RawMessage
		err  error
	)

	switch resource + " " + action {
	case "hosts list":
		data, err = c.Get("/v1/hosts")
	case "hosts get":
		if len(rest) < 1 {
			err = usageError("usage: tt hosts get <host-id>")
			break
		}
		data, err = c.Get("/v1/hosts/" + rest[0])
	case "jobs list":
		data, err = c.Get("/v1/jobs")
	case "jobs get":
		if len(rest) < 1 {
			err = usageError("usage: tt jobs get <job-id>")
			break
		}
		data, err = c.Get("/v1/jobs/" + rest[0])
	case "jobs tail":
		if len(rest) < 1 {
			err = usageError("usage: tt jobs tail <job-id>")
			break
		}
		data, err = c.Get("/v1/jobs/" + rest[0] + "/tail")
	case "jobs attach":
		if len(rest) < 1 {
			err = usageError("usage: tt jobs attach <job-id>")
			break
		}
		data, err = c.Get("/v1/jobs/" + rest[0] + "/attach")
	case "jobs create":
		data, err = runJobsCreate(c, rest)
	case "jobs send":
		data, err = runJobsSend(c, rest)
	case "jobs interrupt":
		data, err = runJobsInterrupt(c, rest)
	case "jobs stop":
		data, err = runJobsStop(c, rest)
	default:
		err = usageError(usage())
	}

	if err != nil {
		msg := err.Error()
		if isUsageError(err) && msg == usage() {
			if writeErr := writeLine(stderr, msg); writeErr != nil {
				return 1
			}
			return 1
		}
		if isUsageError(err) {
			if writeErr := writeLine(stderr, msg); writeErr != nil {
				return 1
			}
			return 1
		}
		if writeErr := writef(stderr, "error: %v\n", err); writeErr != nil {
			return 1
		}
		return 1
	}

	if jsonFlag {
		pretty, pErr := prettyJSON(data)
		if pErr != nil {
			if err := writeLine(stdout, string(data)); err != nil {
				return 1
			}
		} else {
			if err := writeLine(stdout, pretty); err != nil {
				return 1
			}
		}
		return 0
	}

	cmd := resource + " " + action
	if fmtErr := formatHuman(stdout, cmd, data); fmtErr != nil {
		if err := writef(stderr, "error formatting output: %v\n", fmtErr); err != nil {
			return 1
		}
		return 1
	}
	return 0
}

func runJobsCreate(c *client.Client, args []string) (json.RawMessage, error) {
	opts, pos, err := parseOptions(args, map[string]*string{
		"agent":           nil,
		"host":            nil,
		"repo":            nil,
		"goal":            nil,
		"workdir":         nil,
		"priority":        nil,
		"max-duration":    nil,
		"idempotency-key": nil,
	})
	if err != nil {
		return nil, usageError("usage: tt jobs create --host <host-id> --agent <agent> --repo <repo-path> --goal <goal> [--workdir <workdir>] [--priority <priority>] [--max-duration <duration>] [--idempotency-key <key>]")
	}
	if len(pos) != 0 {
		return nil, usageError("usage: tt jobs create --host <host-id> --agent <agent> --repo <repo-path> --goal <goal> [--workdir <workdir>] [--priority <priority>] [--max-duration <duration>] [--idempotency-key <key>]")
	}
	if opts["host"] == "" || opts["agent"] == "" || opts["repo"] == "" || opts["goal"] == "" {
		return nil, usageError("usage: tt jobs create --host <host-id> --agent <agent> --repo <repo-path> --goal <goal> [--workdir <workdir>] [--priority <priority>] [--max-duration <duration>] [--idempotency-key <key>]")
	}

	req := map[string]any{
		"host_id":   opts["host"],
		"agent":     opts["agent"],
		"repo_path": opts["repo"],
		"goal":      opts["goal"],
	}
	if opts["workdir"] != "" {
		req["workdir"] = opts["workdir"]
	}
	if opts["priority"] != "" {
		req["priority"] = opts["priority"]
	}
	if opts["max-duration"] != "" {
		req["max_duration"] = opts["max-duration"]
	}
	if opts["idempotency-key"] != "" {
		req["idempotency_key"] = opts["idempotency-key"]
	}

	return c.Post("/v1/jobs", req)
}

func runJobsSend(c *client.Client, args []string) (json.RawMessage, error) {
	opts, pos, err := parseOptions(args, map[string]*string{"idempotency-key": nil})
	if err != nil || len(pos) < 2 {
		return nil, usageError("usage: tt jobs send [--idempotency-key <key>] <job-id> <text>")
	}
	req := map[string]any{"text": strings.Join(pos[1:], " ")}
	if opts["idempotency-key"] != "" {
		req["idempotency_key"] = opts["idempotency-key"]
	}
	return c.Post("/v1/jobs/"+pos[0]+"/commands/send", req)
}

func runJobsInterrupt(c *client.Client, args []string) (json.RawMessage, error) {
	return runCommandWithoutText(c, args, "interrupt")
}

func runJobsStop(c *client.Client, args []string) (json.RawMessage, error) {
	return runCommandWithoutText(c, args, "stop")
}

func runCommandWithoutText(c *client.Client, args []string, action string) (json.RawMessage, error) {
	opts, pos, err := parseOptions(args, map[string]*string{"idempotency-key": nil})
	if err != nil || len(pos) != 1 {
		return nil, usageError(fmt.Sprintf("usage: tt jobs %s [--idempotency-key <key>] <job-id>", action))
	}
	var req map[string]any
	if opts["idempotency-key"] != "" {
		req = map[string]any{"idempotency_key": opts["idempotency-key"]}
	}
	return c.Post("/v1/jobs/"+pos[0]+"/commands/"+action, req)
}

func parseOptions(args []string, known map[string]*string) (map[string]string, []string, error) {
	values := make(map[string]string, len(known))
	positionals := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") || arg == "--" {
			positionals = append(positionals, arg)
			continue
		}
		nameValue := strings.TrimPrefix(arg, "--")
		name, value, hasValue := strings.Cut(nameValue, "=")
		if _, ok := known[name]; !ok {
			return nil, nil, fmt.Errorf("unknown flag: --%s", name)
		}
		if !hasValue {
			i++
			if i >= len(args) {
				return nil, nil, fmt.Errorf("missing value for --%s", name)
			}
			value = args[i]
		}
		values[name] = value
	}
	return values, positionals, nil
}

type usageErr struct{ msg string }

func (e usageErr) Error() string { return e.msg }

func usageError(msg string) error { return usageErr{msg: msg} }

func isUsageError(err error) bool {
	_, ok := err.(usageErr)
	return ok
}

func prettyJSON(raw json.RawMessage) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func formatHuman(w io.Writer, cmd string, data json.RawMessage) error {
	switch cmd {
	case "hosts list":
		return formatHostsList(w, data)
	case "hosts get":
		return formatHostGet(w, data)
	case "jobs list":
		return formatJobsList(w, data)
	case "jobs get":
		return formatJobGet(w, data)
	case "jobs tail":
		return formatJobTail(w, data)
	case "jobs attach":
		return formatJobAttach(w, data)
	case "jobs create":
		return formatJobCreate(w, data)
	case "jobs send", "jobs interrupt", "jobs stop":
		return formatCommandCreate(w, cmd, data)
	default:
		return writeLine(w, string(data))
	}
}

type hostEntry struct {
	HostID          string `json:"host_id"`
	Hostname        string `json:"hostname"`
	Online          bool   `json:"online"`
	Health          string `json:"health"`
	LastHeartbeatAt string `json:"last_heartbeat_at"`
}

func formatHostsList(w io.Writer, data json.RawMessage) error {
	var resp struct {
		Hosts []hostEntry `json:"hosts"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return err
	}
	if len(resp.Hosts) == 0 {
		return writeLine(w, "No hosts registered.")
	}
	if err := writef(w, "%-20s %-20s %s\n", "HOST ID", "HOSTNAME", "HEALTH"); err != nil {
		return err
	}
	for _, h := range resp.Hosts {
		health := h.Health
		if health == "" {
			if h.Online {
				health = "online"
			} else {
				health = "offline"
			}
		}
		if err := writef(w, "%-20s %-20s %s\n", h.HostID, h.Hostname, health); err != nil {
			return err
		}
	}
	return nil
}

func formatHostGet(w io.Writer, data json.RawMessage) error {
	var resp struct {
		Host hostEntry `json:"host"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return err
	}
	h := resp.Host
	health := h.Health
	if health == "" {
		if h.Online {
			health = "online"
		} else {
			health = "offline"
		}
	}
	if err := writef(w, "Host:     %s\n", h.HostID); err != nil {
		return err
	}
	if err := writef(w, "Hostname: %s\n", h.Hostname); err != nil {
		return err
	}
	return writef(w, "Health:   %s\n", health)
}

type runEntry struct {
	RunID         string `json:"run_id"`
	Status        string `json:"status"`
	StopRequested bool   `json:"stop_requested"`
}

type job struct {
	JobID          string `json:"job_id"`
	HostID         string `json:"host_id"`
	Agent          string `json:"agent"`
	Status         string `json:"status"`
	Goal           string `json:"goal"`
	RepoPath       string `json:"repo_path"`
	Workdir        string `json:"workdir"`
	Priority       string `json:"priority"`
	MaxDuration    string `json:"max_duration"`
	IdempotencyKey string `json:"idempotency_key"`
}

type jobEntry struct {
	Job        job       `json:"job"`
	Run        *runEntry `json:"run,omitempty"`
	HostHealth string    `json:"host_health"`
}

func formatJobsList(w io.Writer, data json.RawMessage) error {
	var resp struct {
		Jobs []jobEntry `json:"jobs"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return err
	}
	if len(resp.Jobs) == 0 {
		return writeLine(w, "No jobs found.")
	}
	if err := writef(w, "%-20s %-12s %-10s %s\n", "JOB ID", "STATUS", "AGENT", "GOAL"); err != nil {
		return err
	}
	for _, j := range resp.Jobs {
		goal := truncateStr(j.Job.Goal, 50)
		if err := writef(w, "%-20s %-12s %-10s %s\n", j.Job.JobID, j.Job.Status, j.Job.Agent, goal); err != nil {
			return err
		}
	}
	return nil
}

func formatJobGet(w io.Writer, data json.RawMessage) error {
	var resp struct {
		Job jobEntry `json:"job"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return err
	}
	j := resp.Job
	if err := writef(w, "Job:      %s\n", j.Job.JobID); err != nil {
		return err
	}
	if err := writef(w, "Status:   %s\n", j.Job.Status); err != nil {
		return err
	}
	if j.Run != nil {
		if err := writef(w, "Run:      %s (%s)\n", j.Run.RunID, j.Run.Status); err != nil {
			return err
		}
	}
	if err := writef(w, "Agent:    %s\n", j.Job.Agent); err != nil {
		return err
	}
	if err := writef(w, "Host:     %s\n", j.Job.HostID); err != nil {
		return err
	}
	if err := writef(w, "Repo:     %s\n", j.Job.RepoPath); err != nil {
		return err
	}
	return writef(w, "Goal:     %s\n", j.Job.Goal)
}

type createJobEntry struct {
	Job job `json:"job"`
	Run struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	} `json:"run"`
}

func formatJobCreate(w io.Writer, data json.RawMessage) error {
	var resp createJobEntry
	if err := json.Unmarshal(data, &resp); err != nil {
		return err
	}
	if err := writef(w, "Created job %s on %s (%s)\n", resp.Job.JobID, resp.Job.HostID, resp.Job.Agent); err != nil {
		return err
	}
	return writef(w, "Run: %s %s\n", resp.Run.RunID, resp.Run.Status)
}

type tailEntry struct {
	JobID    string `json:"job_id"`
	Snapshot struct {
		RunID      string `json:"run_id"`
		HostID     string `json:"host_id"`
		CapturedAt string `json:"captured_at"`
		LineCount  int    `json:"line_count"`
		Stale      bool   `json:"stale"`
		OutputText string `json:"output"`
		UpdatedAt  string `json:"updated_at"`
	} `json:"snapshot"`
}

func formatJobTail(w io.Writer, data json.RawMessage) error {
	var t tailEntry
	if err := json.Unmarshal(data, &t); err != nil {
		return err
	}
	freshness := "fresh"
	if t.Snapshot.Stale {
		freshness = "stale"
	}
	if err := writef(w, "Job:      %s\n", t.JobID); err != nil {
		return err
	}
	if t.Snapshot.RunID != "" {
		if err := writef(w, "Run:      %s\n", t.Snapshot.RunID); err != nil {
			return err
		}
	}
	if t.Snapshot.HostID != "" {
		if err := writef(w, "Host:     %s\n", t.Snapshot.HostID); err != nil {
			return err
		}
	}
	if err := writef(w, "Snapshot: %s, %d lines\n", freshness, t.Snapshot.LineCount); err != nil {
		return err
	}
	if t.Snapshot.CapturedAt != "" {
		if err := writef(w, "Captured: %s\n", t.Snapshot.CapturedAt); err != nil {
			return err
		}
	}
	if t.Snapshot.UpdatedAt != "" {
		if err := writef(w, "Updated:  %s\n", t.Snapshot.UpdatedAt); err != nil {
			return err
		}
	}
	if err := writeLine(w, ""); err != nil {
		return err
	}
	if err := writeLine(w, "--- tail ---"); err != nil {
		return err
	}
	text := strings.TrimRight(t.Snapshot.OutputText, "\n")
	if text != "" {
		return writeLine(w, text)
	}
	return writeLine(w, "(no output)")
}

type attachEntry struct {
	JobID  string `json:"job_id"`
	HostID string `json:"host_id"`
	Tmux   struct {
		SessionName string `json:"session_name"`
		WindowName  string `json:"window_name"`
	} `json:"tmux"`
	Attach struct {
		SSHTarget string `json:"ssh_target"`
		Command   string `json:"command"`
	} `json:"attach"`
}

func formatJobAttach(w io.Writer, data json.RawMessage) error {
	var a attachEntry
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	if err := writef(w, "Job:      %s\n", a.JobID); err != nil {
		return err
	}
	if err := writef(w, "Host:     %s\n", a.HostID); err != nil {
		return err
	}
	if a.Attach.SSHTarget != "" {
		if err := writef(w, "SSH:      %s\n", a.Attach.SSHTarget); err != nil {
			return err
		}
	}
	if err := writef(w, "Session:  %s\n", a.Tmux.SessionName); err != nil {
		return err
	}
	if err := writef(w, "Window:   %s\n", a.Tmux.WindowName); err != nil {
		return err
	}
	if err := writeLine(w, ""); err != nil {
		return err
	}
	if err := writef(w, "Check snapshot:\n  tt jobs tail %s\n", a.JobID); err != nil {
		return err
	}
	return writef(w, "Attach live session:\n  %s\n", a.Attach.Command)
}

type commandEntry struct {
	Command struct {
		CommandID   string `json:"command_id"`
		JobID       string `json:"job_id"`
		CommandType string `json:"command_type"`
		State       string `json:"state"`
	} `json:"command"`
}

func formatCommandCreate(w io.Writer, cmd string, data json.RawMessage) error {
	var resp commandEntry
	if err := json.Unmarshal(data, &resp); err != nil {
		return err
	}
	action := strings.TrimPrefix(cmd, "jobs ")
	return writef(w, "Queued %s %s for job %s (%s)\n", action, resp.Command.CommandID, resp.Command.JobID, resp.Command.State)
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func writeLine(w io.Writer, s string) error {
	_, err := fmt.Fprintln(w, s)
	return err
}

func writef(w io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(w, format, args...)
	return err
}

func usage() string {
	return `tt - Triptych CLI

Usage: tt [--json] <resource> <action> [args...]

Commands:
  hosts list                  List all registered hosts
  hosts get <host-id>         Show details for a host
  jobs  list                  List all jobs
  jobs  get <job-id>          Show details for a job
  jobs  tail <job-id>         Show latest output snapshot for a job
  jobs  attach <job-id>       Show attach info (tmux session) for a job
  jobs  create --host <host-id> --agent <agent> --repo <repo-path> --goal <goal>
                              Create a job on a host
  jobs  send <job-id> <text>  Queue input text for a job
  jobs  interrupt <job-id>    Queue Ctrl-C for a job
  jobs  stop <job-id>         Queue stop for a job

Flags:
  --json    Print raw API data as pretty JSON

Environment:
  TRIPTYCH_SERVER_URL   Server base URL (default: http://127.0.0.1:8080)`
}
