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

On each tick, `agentd` also captures output snapshots for all live runs (active/waiting/stopping) that have a tmux session. It uses `tmux capture-pane` to read the last 200 lines from the pane and uploads the result via `POST /v1/runs/{run_id}/snapshot`. This makes `tt jobs tail <job-id>` return real terminal output.

Environment variables:
- `TRIPTYCH_SERVER_URL` default `http://127.0.0.1:8080`
- `TRIPTYCH_HOST_ID` required
- `TRIPTYCH_HOSTNAME` default `os.Hostname()`
- `TRIPTYCH_CAPABILITIES` optional comma-separated list
- `TRIPTYCH_ALLOWED_REPO_ROOTS` optional comma-separated absolute paths
- `TRIPTYCH_LABELS` optional comma-separated `key=value` pairs
- `TRIPTYCH_HEARTBEAT_INTERVAL` default `15s`

## E2E Smoke Tests

Run the full end-to-end smoke test suite:

```
make e2e
```

Prerequisites: Docker (for disposable Postgres), tmux, Python 3, Go toolchain.

The harness starts a Postgres container, builds and runs agentserver + agentd,
exercises host registration, heartbeat, job creation, tmux-backed launch,
output snapshot capture, `tt jobs tail`, send/interrupt/stop commands, and
reconciliation. All resources are cleaned up
automatically. On failure, logs are saved to `.artifacts/e2e/<timestamp>/`.

Pass `-v` for verbose output or `--keep` to retain artifacts on success:

```
python3 scripts/e2e_smoke.py -v --keep
```
