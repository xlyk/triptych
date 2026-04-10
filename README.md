# Triptych

Triptych is a tmux-backed control plane for running agentic CI/infrastructure tasks. Operators use the `tt` CLI to create, monitor, and interact with jobs; `agentd` runs on each execution host and launches agent sessions in tmux; `agentserver` is the HTTP API server backing both.

**Components:**
- `agentserver` — HTTP API server (Postgres-backed)
- `agentd` — per-host daemon; registers the host, polls for work, launches tmux sessions
- `tt` — operator CLI; no daemon, connects to agentserver over HTTP

**Domain model:** `Host` (execution machine) → `Job` (requested task) → `Run` (one execution attempt of a job) → `Command` (operator input: send/interrupt/stop) + `OutputSnapshot` (captured terminal output)

## Quickstart: one local host

This is the shortest useful local operator loop.

Prerequisites:
- Postgres reachable from this machine
- `tmux` installed
- Go toolchain installed
- `claude` or `codex` on `PATH` for real runs

Build the binaries:

```sh
go build -o bin/agentserver ./cmd/agentserver
go build -o bin/agentd ./cmd/agentd
go build -o bin/tt ./cmd/tt
```

Terminal 1 — start `agentserver`:

```sh
TRIPTYCH_DATABASE_URL='postgres://triptych:triptych@127.0.0.1:5432/triptych?sslmode=disable' ./bin/agentserver
```

Terminal 2 — start `agentd` for one local worker:

```sh
TRIPTYCH_SERVER_URL='http://127.0.0.1:8080' \
TRIPTYCH_HOST_ID='host-local-1' \
TRIPTYCH_HOSTNAME='local-dev' \
TRIPTYCH_CAPABILITIES='claude,codex,tmux' \
TRIPTYCH_ALLOWED_REPO_ROOTS='/absolute/path/you/want/agentd/to-use' \
./bin/agentd
```

Terminal 3 — operator flow with `tt`:

```sh
export TRIPTYCH_SERVER_URL='http://127.0.0.1:8080'
./bin/tt hosts list
./bin/tt jobs create --host host-local-1 --agent codex --repo /absolute/repo --goal 'Inspect the repo and propose the next change'
./bin/tt jobs get <job-id>
./bin/tt jobs tail <job-id>
./bin/tt jobs attach <job-id>
```

If you want a deterministic smoke path instead of a real agent runtime, run `make e2e`.

## tt CLI

Basic operator commands for querying the Triptych control plane and pushing job actions.

```
tt [--json] <resource> <action> [args...]

Commands:
  hosts list                  List all registered hosts
  hosts get <host-id>         Show details for a host
  jobs  list [--status <status>]  List jobs, optionally filtered by status
  jobs  get <job-id>          Show job state, host health, and next-step guidance
  jobs  tail <job-id>         Show latest output snapshot with operator metadata
  jobs  attach <job-id>       Show tmux attach info and next-step guidance
  jobs  create --host <host-id> --agent <agent> --repo <repo-path> --goal <goal>
                              Create a job on a host
  jobs  send <job-id> <text>  Queue input text for a job
  jobs  interrupt <job-id>    Queue Ctrl-C for a job
  jobs  stop <job-id>         Queue stop for a job
```

Set `TRIPTYCH_SERVER_URL` to point at the server (default: `http://127.0.0.1:8080`).
Use `--json` to get raw API data as pretty-printed JSON.

Typical operator loop:

```
tt jobs list --status running
tt jobs create --host host-1 --agent codex --repo /abs/path/to/repo --goal "Fix the failing tests"
tt jobs get <job-id>
tt jobs tail <job-id>
tt jobs attach <job-id>
tt jobs send <job-id> "continue with the refactor"
tt jobs interrupt <job-id>
tt jobs stop <job-id>
```

Recommended interpretation:
- `tt jobs list --status running|failed|queued` is the fast triage view
- `tt jobs list` now shows job status, latest run status, host health, agent, and goal
- `tt jobs get` gives the control-plane state view plus host health and the next recommended checks
- `tt jobs tail` gives the latest bounded snapshot with freshness/line metadata
- `tt jobs attach` gives the live tmux attach path plus a reminder to inspect the snapshot first
- command acknowledgements (`send`, `interrupt`, `stop`) should send you back to `tt jobs get` and `tt jobs tail`

Live-stack note:
- when you change Triptych server/daemon code locally, an already-running live stack will keep serving the old behavior until you restart the managed binaries

## agentserver

`agentserver` is the HTTP API server. It connects to Postgres, runs migrations on startup, and serves the control-plane endpoints used by both `tt` and `agentd`.

```sh
# Build
go build -o bin/agentserver ./cmd/agentserver

# Run (required database URL)
TRIPTYCH_DATABASE_URL='postgres://triptych:triptych@127.0.0.1:5432/triptych?sslmode=disable' ./bin/agentserver

# Override listen address
TRIPTYCH_SERVER_ADDR=':9090' TRIPTYCH_DATABASE_URL='postgres://triptych:triptych@127.0.0.1:5432/triptych?sslmode=disable' ./bin/agentserver
```

Environment variables:
- `TRIPTYCH_DATABASE_URL` — required Postgres DSN
- `TRIPTYCH_SERVER_ADDR` — listen address (default `:8080`)

## agentd

`agentd` registers the host, sends periodic heartbeats, polls for work, and launches agent processes in detached tmux sessions. On each poll tick, the daemon reconciles active runs against tmux reality: if a run's tmux session has disappeared, the daemon repairs the run state on the server (crashed/failed if unexpected, exited/cancelled if a stop was requested or the run was already stopping).

### Interactive agent runtime

When a job is assigned, `agentd` launches a **long-lived interactive agent session** in tmux and injects the job goal:

1. Create a detached tmux session running the interactive agent CLI:
   - **`agent=claude`** → `sh -lc 'exec claude --verbose ...'`
   - **`agent=codex`** → `sh -lc 'exec codex --quiet ...'`
2. Optionally for Claude, send an initial `Enter` to accept the default workspace-trust prompt when it appears.
3. Inject the job goal into the session via `tmux send-keys -l "<goal>" Enter`.

Both commands run through `sh -lc`, and `exec` replaces the shell with the agent process so signals and exit codes propagate cleanly. The goal is sent as literal text (`-l` flag) followed by Enter, so tmux buffers the keystrokes until the CLI is ready to accept input. Claude's startup handshake is now explicit, not implicit: enable it only when needed for an environment where the interactive trust prompt is still appearing.

Triptych can also pre-seed trust / approval settings for these interactive CLIs so first-run prompts do not block a detached tmux launch:

- **Claude Code**
  - `TRIPTYCH_CLAUDE_SETTINGS_FILE=/abs/path/settings.json` passes `--settings /abs/path/settings.json`
  - `TRIPTYCH_CLAUDE_SETTINGS_JSON='{"trustedDirectories":["/repo"]}'` passes inline `--settings` JSON
  - `TRIPTYCH_CLAUDE_TRUSTED_DIRECTORIES=/repo,/another/repo` synthesizes or augments inline settings JSON with `trustedDirectories`
  - `TRIPTYCH_CLAUDE_PERMISSION_MODE=dontAsk` adds `--permission-mode dontAsk`
  - `TRIPTYCH_CLAUDE_STARTUP_HANDSHAKE=true` opts into the initial `Enter` bootstrap for environments where Claude still shows the trust prompt
- **Codex CLI**
  - `TRIPTYCH_CODEX_APPROVAL_POLICY=never` adds `--ask-for-approval never`
  - `TRIPTYCH_CODEX_SANDBOX_MODE=workspace-write` adds `--sandbox workspace-write`
  - `TRIPTYCH_CODEX_CONFIG_PROFILE=triptych` adds `--profile triptych`
  - `TRIPTYCH_CODEX_TRUST_PROJECT=true` adds a `--config projects."<workdir>".trust_level="trusted"` override for the job workdir

This keeps interactive workers attachable while still letting operators pre-approve the workspace or command policy up front. For Claude specifically, treat these settings as best-effort hints rather than a guaranteed prompt suppressor; when the prompt still appears in a given environment, enable `TRIPTYCH_CLAUDE_STARTUP_HANDSHAKE=true` for that host instead of relying on an unconditional bootstrap.

Because the sessions are interactive and long-lived, operators can attach to a running session via `tt jobs attach <job-id>` and interact freely via `tt jobs send`. When the agent finishes or the session exits, reconciliation repairs the run state on the server.

### Placeholder / test mode

Set `TRIPTYCH_LAUNCH_MODE=placeholder` to replace real agent CLIs with a lightweight process that prints run metadata and sleeps (`tail -f /dev/null`). This is used by the E2E harness for fast, deterministic testing without requiring `claude` or `codex` to be installed.

The default (unset or empty) is real mode.

On each tick, `agentd` also captures output snapshots for all live runs (active/waiting/stopping) that have a tmux session. It uses `tmux capture-pane` to read the last 200 lines from the pane and uploads the result via `POST /v1/runs/{run_id}/snapshot`. This makes `tt jobs tail <job-id>` return real terminal output.

Environment variables:
- `TRIPTYCH_SERVER_URL` — agentserver URL (default `http://127.0.0.1:8080`; shared with `tt`)
- `TRIPTYCH_HOST_ID` — required host identifier
- `TRIPTYCH_HOSTNAME` — hostname reported to server (default `os.Hostname()`)
- `TRIPTYCH_CAPABILITIES` optional comma-separated list
- `TRIPTYCH_ALLOWED_REPO_ROOTS` optional comma-separated absolute paths
- `TRIPTYCH_LABELS` optional comma-separated `key=value` pairs
- `TRIPTYCH_STATE_DIR` optional local daemon state directory (default `$HOME/.triptych`)
- `TRIPTYCH_HEARTBEAT_INTERVAL` default `15s`
- `TRIPTYCH_LAUNCH_MODE` optional, `""` (real, default) or `"placeholder"` (test mode)
- `TRIPTYCH_CLAUDE_SETTINGS_FILE` optional absolute Claude settings JSON file passed via `--settings`
- `TRIPTYCH_CLAUDE_SETTINGS_JSON` optional inline Claude settings JSON passed via `--settings`
- `TRIPTYCH_CLAUDE_TRUSTED_DIRECTORIES` optional comma-separated absolute directories to add as Claude `trustedDirectories`
- `TRIPTYCH_CLAUDE_PERMISSION_MODE` optional Claude `--permission-mode` value (for example `dontAsk`)
- `TRIPTYCH_CLAUDE_STARTUP_HANDSHAKE` optional boolean; when true, sends an initial `Enter` before goal injection for Claude only
- `TRIPTYCH_CODEX_CONFIG_PROFILE` optional Codex `--profile` name
- `TRIPTYCH_CODEX_APPROVAL_POLICY` optional Codex `--ask-for-approval` value (for example `never`)
- `TRIPTYCH_CODEX_SANDBOX_MODE` optional Codex `--sandbox` value (`read-only`, `workspace-write`, or `danger-full-access`)
- `TRIPTYCH_CODEX_TRUST_PROJECT` optional boolean; when true, marks the job workdir trusted via Codex `--config`

`agentd` now keeps a tiny local command-receipt spool under `TRIPTYCH_STATE_DIR` so a command that was already applied locally will not be re-applied after a daemon restart while the daemon is still finishing ack/observe bookkeeping.

## E2E Smoke Tests

Two smoke modes are available:

- `make e2e` runs the existing deterministic placeholder path. This remains the default and is the recommended fast/stable CI smoke.
- `make e2e-real-claude` runs a smaller real-Claude smoke that launches the actual interactive Claude runtime.

```
make e2e
make e2e-real-claude
```

Prerequisites: Docker (for disposable Postgres), tmux, Python 3, Go toolchain.
The real-Claude path also requires the `claude` CLI on `PATH` with whatever auth/setup it normally needs.

The harness starts a Postgres container, builds and runs agentserver + agentd,
and then runs mode-specific assertions:

- **Placeholder mode** (`python3 scripts/e2e_smoke.py` or `--mode placeholder`): preserves the existing full deterministic smoke coverage for host registration, heartbeat, tmux-backed launch, placeholder snapshot capture, `tt jobs tail`, send/interrupt/stop commands, reconciliation, idempotent creation, and job listing.
- **Real-Claude mode** (`python3 scripts/e2e_smoke.py --mode real-claude`): verifies that a Claude-backed session launches, the tmux session exists, the captured output does not show the workspace trust prompt, and the requested `REAL_CLAUDE_SMOKE_OK` marker actually appears in output. In this mode the harness passes `TRIPTYCH_CLAUDE_TRUSTED_DIRECTORIES=<repo>`, `TRIPTYCH_CLAUDE_PERMISSION_MODE=dontAsk`, and `TRIPTYCH_CLAUDE_STARTUP_HANDSHAKE=true` to `agentd` so the environment is explicit about relying on the Claude bootstrap handshake.

All resources are cleaned up automatically. On failure, logs are saved to `.artifacts/e2e/<timestamp>/`.

Pass `-v` for verbose output or `--keep` to retain artifacts on success:

```
python3 scripts/e2e_smoke.py -v --keep
python3 scripts/e2e_smoke.py --mode real-claude -v --keep
```
