# Triptych

Minimal initial Go skeleton for the Triptych control plane.

Current scope:
- `tt` CLI with read-only commands against the control-plane server
- `agentd` daemon entrypoint
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
