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
			if err := writeLine(stderr, "usage: tt hosts get <host-id>"); err != nil {
				return 1
			}
			return 1
		}
		data, err = c.Get("/v1/hosts/" + rest[0])
	case "jobs list":
		data, err = c.Get("/v1/jobs")
	case "jobs get":
		if len(rest) < 1 {
			if err := writeLine(stderr, "usage: tt jobs get <job-id>"); err != nil {
				return 1
			}
			return 1
		}
		data, err = c.Get("/v1/jobs/" + rest[0])
	case "jobs tail":
		if len(rest) < 1 {
			if err := writeLine(stderr, "usage: tt jobs tail <job-id>"); err != nil {
				return 1
			}
			return 1
		}
		data, err = c.Get("/v1/jobs/" + rest[0] + "/tail")
	case "jobs attach":
		if len(rest) < 1 {
			if err := writeLine(stderr, "usage: tt jobs attach <job-id>"); err != nil {
				return 1
			}
			return 1
		}
		data, err = c.Get("/v1/jobs/" + rest[0] + "/attach")
	default:
		if err := writeLine(stderr, usage()); err != nil {
			return 1
		}
		return 1
	}

	if err != nil {
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
	default:
		return writeLine(w, string(data))
	}
}

// host types matching server response shapes.
type hostEntry struct {
	HostID          string `json:"host_id"`
	Hostname        string `json:"hostname"`
	Online          bool   `json:"online"`
	Health          string `json:"health"`
	LastHeartbeatAt string `json:"last_heartbeat_at"`
}

func formatHostsList(w io.Writer, data json.RawMessage) error {
	var hosts []hostEntry
	if err := json.Unmarshal(data, &hosts); err != nil {
		return err
	}
	if len(hosts) == 0 {
		return writeLine(w, "No hosts registered.")
	}
	if err := writef(w, "%-20s %-20s %s\n", "HOST ID", "HOSTNAME", "HEALTH"); err != nil {
		return err
	}
	for _, h := range hosts {
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
	var h hostEntry
	if err := json.Unmarshal(data, &h); err != nil {
		return err
	}
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

type jobEntry struct {
	Job struct {
		JobID    string `json:"job_id"`
		HostID   string `json:"host_id"`
		Agent    string `json:"agent"`
		Status   string `json:"status"`
		Goal     string `json:"goal"`
		RepoPath string `json:"repo_path"`
	} `json:"job"`
	HostHealth string `json:"host_health"`
}

func formatJobsList(w io.Writer, data json.RawMessage) error {
	var jobs []jobEntry
	if err := json.Unmarshal(data, &jobs); err != nil {
		return err
	}
	if len(jobs) == 0 {
		return writeLine(w, "No jobs found.")
	}
	if err := writef(w, "%-20s %-12s %-10s %s\n", "JOB ID", "STATUS", "AGENT", "GOAL"); err != nil {
		return err
	}
	for _, j := range jobs {
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

type tailEntry struct {
	JobID    string `json:"job_id"`
	Snapshot struct {
		OutputText string `json:"output"`
		LineCount  int    `json:"line_count"`
		Stale      bool   `json:"stale"`
	} `json:"snapshot"`
}

func formatJobTail(w io.Writer, data json.RawMessage) error {
	var t tailEntry
	if err := json.Unmarshal(data, &t); err != nil {
		return err
	}
	if t.Snapshot.Stale {
		if err := writef(w, "[stale snapshot, %d lines]\n", t.Snapshot.LineCount); err != nil {
			return err
		}
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
	if err := writef(w, "Job:     %s\n", a.JobID); err != nil {
		return err
	}
	if err := writef(w, "Host:    %s (%s)\n", a.HostID, a.Attach.SSHTarget); err != nil {
		return err
	}
	if err := writef(w, "Session: %s (window: %s)\n", a.Tmux.SessionName, a.Tmux.WindowName); err != nil {
		return err
	}
	return writef(w, "\nTo attach:\n  %s\n", a.Attach.Command)
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

Flags:
  --json    Print raw API data as pretty JSON

Environment:
  TRIPTYCH_SERVER_URL   Server base URL (default: http://127.0.0.1:8080)`
}
