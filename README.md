# Triptych

Minimal initial Go skeleton for the Triptych control plane.

Current scope:
- `tt` CLI with read-only commands against the control-plane server
- `agentd` daemon that registers a host, sends periodic heartbeats, and launches placeholder tmux-backed runs for attach testing
- `agentserver` HTTP server with host/job/run management APIs
- shared domain types and request validation

## tt CLI

Read-only commands for querying the Triptych control plane.

```
tt [--json] <resource> <action> [args...]

Commands:
  hosts list                  List all registered hosts
  hosts get <host-id>         Show details for a host
  jobs  list                  List all jobs
  jobs  get <job-id>          Show details for a job
  jobs  tail <job-id>         Show latest output snapshot
  jobs  attach <job-id>       Show tmux attach info
```

Set `TRIPTYCH_SERVER_URL` to point at the server (default: `http://127.0.0.1:8080`).
Use `--json` to get raw API data as pretty-printed JSON.

## agentd

`agentd` now performs Task 6 host registration, heartbeat, work polling, and real detached tmux session launch for attach testing. On each poll tick, the daemon reconciles active runs against tmux reality: if a run's tmux session has disappeared, the daemon repairs the run state on the server (crashed/failed if unexpected, exited/cancelled if a stop was requested or the run was already stopping).

Environment variables:
- `TRIPTYCH_SERVER_URL` default `http://127.0.0.1:8080`
- `TRIPTYCH_HOST_ID` required
- `TRIPTYCH_HOSTNAME` default `os.Hostname()`
- `TRIPTYCH_CAPABILITIES` optional comma-separated list
- `TRIPTYCH_ALLOWED_REPO_ROOTS` optional comma-separated absolute paths
- `TRIPTYCH_LABELS` optional comma-separated `key=value` pairs
- `TRIPTYCH_HEARTBEAT_INTERVAL` default `15s`
