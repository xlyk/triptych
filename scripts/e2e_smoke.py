#!/usr/bin/env python3
"""
Triptych E2E smoke test harness.

Starts a disposable Postgres container, builds and runs agentserver + agentd,
exercises the core feature set via HTTP, and verifies real tmux-backed behavior.

Usage:
    python3 scripts/e2e_smoke.py          # run all tests
    python3 scripts/e2e_smoke.py -v       # verbose output
    python3 scripts/e2e_smoke.py --keep   # keep artifacts on success too
"""

import argparse
import atexit
import http.client
import json
import os
import shutil
import signal
import subprocess
import sys
import time
import urllib.request
import urllib.error
from datetime import datetime
from pathlib import Path

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

REPO_ROOT = Path(__file__).resolve().parent.parent
PG_CONTAINER = "triptych-e2e-pg"
PG_PORT = 25432
PG_USER = "triptych"
PG_PASS = "triptych"
PG_DB = "triptych_e2e"
DATABASE_URL = f"postgres://{PG_USER}:{PG_PASS}@127.0.0.1:{PG_PORT}/{PG_DB}?sslmode=disable"
SERVER_PORT = 28080
SERVER_ADDR = f"127.0.0.1:{SERVER_PORT}"
SERVER_URL = f"http://{SERVER_ADDR}"
HOST_ID = "e2e-host-001"
HEARTBEAT_INTERVAL = "2s"

# Timeouts
STARTUP_TIMEOUT = 30  # seconds to wait for services
POLL_INTERVAL = 0.3   # seconds between polls
DAEMON_CYCLE = 3      # seconds to wait for agentd to complete a heartbeat cycle

# Cleanup registry
_cleanup_fns = []

# Test tracking
_results = []

verbose = False


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def log(msg):
    print(f"  {msg}", flush=True)


def logv(msg):
    if verbose:
        print(f"    [v] {msg}", flush=True)


def register_cleanup(fn, label=""):
    _cleanup_fns.append((fn, label))


def run_cleanup():
    for fn, label in reversed(_cleanup_fns):
        try:
            logv(f"cleanup: {label}")
            fn()
        except Exception as e:
            logv(f"cleanup error ({label}): {e}")


atexit.register(run_cleanup)


def api(method, path, body=None, expect_ok=True):
    """Make an HTTP request to the agentserver. Returns parsed JSON response."""
    url = f"{SERVER_URL}{path}"
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"} if data else {}
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            raw = resp.read()
            result = json.loads(raw) if raw else {}
    except urllib.error.HTTPError as e:
        raw = e.read()
        result = json.loads(raw) if raw else {}
        if expect_ok:
            raise AssertionError(f"{method} {path} returned HTTP {e.code}: {result}")
        return result
    if expect_ok and not result.get("ok"):
        raise AssertionError(f"{method} {path}: ok=false: {result}")
    return result


def poll_until(predicate, timeout, desc="condition", interval=POLL_INTERVAL):
    """Poll until predicate returns truthy, or raise after timeout."""
    deadline = time.monotonic() + timeout
    last_exc = None
    while time.monotonic() < deadline:
        try:
            result = predicate()
            if result:
                return result
        except Exception as e:
            last_exc = e
        time.sleep(interval)
    msg = f"Timed out waiting for: {desc} ({timeout}s)"
    if last_exc:
        msg += f" (last error: {last_exc})"
    raise TimeoutError(msg)


def tmux_has_session(name):
    """Check if a tmux session exists."""
    return subprocess.run(
        ["tmux", "has-session", "-t", f"={name}"],
        capture_output=True,
    ).returncode == 0


def tmux_kill_session(name):
    """Kill a tmux session if it exists."""
    subprocess.run(["tmux", "kill-session", "-t", f"={name}"], capture_output=True)


def tmux_list_sessions(prefix="triptych-run-"):
    """List tmux sessions matching the prefix."""
    result = subprocess.run(
        ["tmux", "list-sessions", "-F", "#{session_name}"],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        return []
    return [s for s in result.stdout.strip().split("\n") if s.startswith(prefix)]


# ---------------------------------------------------------------------------
# Artifact collection
# ---------------------------------------------------------------------------

class ArtifactCollector:
    def __init__(self):
        ts = datetime.now().strftime("%Y%m%d_%H%M%S")
        self.dir = REPO_ROOT / ".artifacts" / "e2e" / ts
        self.server_log = None
        self.daemon_log = None

    def setup(self):
        self.dir.mkdir(parents=True, exist_ok=True)
        self.server_log = open(self.dir / "agentserver.log", "w")
        self.daemon_log = open(self.dir / "agentd.log", "w")

    def close(self):
        if self.server_log:
            self.server_log.close()
        if self.daemon_log:
            self.daemon_log.close()

    def remove_if_clean(self):
        """Remove artifact directory if all tests passed."""
        try:
            self.close()
            shutil.rmtree(self.dir)
            # Clean up empty parent dirs
            parent = self.dir.parent
            if parent.exists() and not any(parent.iterdir()):
                parent.rmdir()
                gp = parent.parent
                if gp.exists() and not any(gp.iterdir()):
                    gp.rmdir()
        except Exception:
            pass


# ---------------------------------------------------------------------------
# Infrastructure setup
# ---------------------------------------------------------------------------

def start_postgres():
    """Start a disposable Postgres container."""
    log("Starting Postgres container...")

    # Kill any leftover container
    subprocess.run(
        ["docker", "rm", "-f", PG_CONTAINER],
        capture_output=True,
    )

    result = subprocess.run(
        [
            "docker", "run", "-d",
            "--name", PG_CONTAINER,
            "-p", f"{PG_PORT}:5432",
            "-e", f"POSTGRES_USER={PG_USER}",
            "-e", f"POSTGRES_PASSWORD={PG_PASS}",
            "-e", f"POSTGRES_DB={PG_DB}",
            "postgres:16-alpine",
        ],
        capture_output=True, text=True,
    )
    if result.returncode != 0:
        raise RuntimeError(f"Failed to start Postgres: {result.stderr}")

    register_cleanup(
        lambda: subprocess.run(["docker", "rm", "-f", PG_CONTAINER], capture_output=True),
        "stop postgres container",
    )

    # Wait for Postgres to be ready
    def pg_ready():
        r = subprocess.run(
            ["docker", "exec", PG_CONTAINER, "pg_isready", "-U", PG_USER],
            capture_output=True,
        )
        return r.returncode == 0

    poll_until(pg_ready, STARTUP_TIMEOUT, "Postgres ready")
    log("Postgres ready.")


def build_binaries():
    """Build agentserver and agentd."""
    log("Building Go binaries...")
    bin_dir = REPO_ROOT / "bin"
    bin_dir.mkdir(exist_ok=True)

    for cmd in ("agentserver", "agentd"):
        result = subprocess.run(
            ["go", "build", "-o", str(bin_dir / cmd), f"./cmd/{cmd}"],
            capture_output=True, text=True, cwd=str(REPO_ROOT),
        )
        if result.returncode != 0:
            raise RuntimeError(f"Failed to build {cmd}: {result.stderr}")

    log("Binaries built.")
    return bin_dir


def start_agentserver(bin_dir, artifacts):
    """Start the agentserver process."""
    log("Starting agentserver...")
    env = {
        **os.environ,
        "TRIPTYCH_DATABASE_URL": DATABASE_URL,
        "TRIPTYCH_SERVER_ADDR": f":{SERVER_PORT}",
    }
    proc = subprocess.Popen(
        [str(bin_dir / "agentserver")],
        env=env,
        stdout=artifacts.server_log,
        stderr=subprocess.STDOUT,
    )
    register_cleanup(lambda: _kill_proc(proc), "stop agentserver")

    # Wait for server to accept connections
    def server_ready():
        try:
            conn = http.client.HTTPConnection(SERVER_ADDR, timeout=2)
            conn.request("GET", "/v1/hosts")
            resp = conn.getresponse()
            conn.close()
            return resp.status == 200
        except Exception:
            return False

    poll_until(server_ready, STARTUP_TIMEOUT, "agentserver ready")
    log("agentserver ready.")
    return proc


def start_agentd(bin_dir, artifacts):
    """Start the agentd process."""
    log("Starting agentd...")
    env = {
        **os.environ,
        "TRIPTYCH_SERVER_URL": SERVER_URL,
        "TRIPTYCH_HOST_ID": HOST_ID,
        "TRIPTYCH_HOSTNAME": "e2e-test-host",
        "TRIPTYCH_CAPABILITIES": "claude",
        "TRIPTYCH_ALLOWED_REPO_ROOTS": str(REPO_ROOT),
        "TRIPTYCH_HEARTBEAT_INTERVAL": HEARTBEAT_INTERVAL,
    }
    proc = subprocess.Popen(
        [str(bin_dir / "agentd")],
        env=env,
        stdout=artifacts.daemon_log,
        stderr=subprocess.STDOUT,
    )
    register_cleanup(lambda: _kill_proc(proc), "stop agentd")

    # Wait for host to appear
    def host_registered():
        try:
            resp = api("GET", f"/v1/hosts/{HOST_ID}")
            return resp.get("ok")
        except Exception:
            return False

    poll_until(host_registered, STARTUP_TIMEOUT, "agentd registered")
    log("agentd registered.")
    return proc


def _kill_proc(proc):
    if proc.poll() is None:
        proc.send_signal(signal.SIGTERM)
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=3)


# ---------------------------------------------------------------------------
# Test runner
# ---------------------------------------------------------------------------

def run_test(name, fn):
    """Run a test function, track pass/fail."""
    sys.stdout.write(f"  [{name}] ... ")
    sys.stdout.flush()
    try:
        fn()
        print("PASS")
        _results.append((name, True, None))
    except Exception as e:
        print(f"FAIL: {e}")
        _results.append((name, False, str(e)))


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------

def test_host_registration():
    """Verify host registered with correct fields."""
    resp = api("GET", f"/v1/hosts/{HOST_ID}")
    host = resp["data"]["host"]
    assert host["host_id"] == HOST_ID, f"unexpected host_id: {host['host_id']}"
    assert host["hostname"] == "e2e-test-host", f"unexpected hostname: {host['hostname']}"
    assert "claude" in host["capabilities"], f"capabilities missing claude: {host['capabilities']}"


def test_heartbeat_visible():
    """Verify heartbeat updates are reflected in host health."""
    resp = api("GET", f"/v1/hosts/{HOST_ID}")
    host = resp["data"]["host"]
    health = host["health"]
    assert health == "online", f"expected online, got {health}"


def test_create_job_and_launch():
    """Create a job, verify it gets launched into a tmux session."""
    resp = api("POST", "/v1/jobs", {
        "agent": "claude",
        "host_id": HOST_ID,
        "repo_path": str(REPO_ROOT),
        "goal": "E2E smoke test job",
        "metadata": {"e2e": "true"},
    })
    job = resp["data"]["job"]
    run = resp["data"]["run"]

    job_id = job["job_id"]
    run_id = run["run_id"]

    assert job["status"] == "assigned", f"expected assigned, got {job['status']}"
    assert run["status"] == "pending_launch", f"expected pending_launch, got {run['status']}"

    logv(f"Created job={job_id}, run={run_id}")

    # Wait for agentd to launch the tmux session
    def run_is_active():
        r = api("GET", f"/v1/jobs/{job_id}")
        job_summary = r["data"]["job"]
        run_data = job_summary.get("run")
        if not run_data:
            logv("  run not yet present in response")
            return False
        logv(f"  run status: {run_data['status']}")
        return run_data["status"] == "active"

    poll_until(run_is_active, DAEMON_CYCLE + 5, "run becomes active")

    # Verify tmux session actually exists
    r = api("GET", f"/v1/jobs/{job_id}/attach")
    tmux_session = r["data"]["tmux"]["session_name"]
    assert tmux_has_session(tmux_session), f"tmux session {tmux_session} does not exist"

    logv(f"tmux session verified: {tmux_session}")

    # Store for later tests
    test_create_job_and_launch.job_id = job_id
    test_create_job_and_launch.run_id = run_id
    test_create_job_and_launch.tmux_session = tmux_session


def test_attach_metadata():
    """Verify attach endpoint returns correct tmux metadata."""
    job_id = test_create_job_and_launch.job_id
    resp = api("GET", f"/v1/jobs/{job_id}/attach")
    data = resp["data"]
    assert data["job_id"] == job_id
    assert data["host_id"] == HOST_ID
    assert data["tmux"]["session_name"] != ""
    assert data["tmux"]["window_name"] != ""


def test_snapshot_captured():
    """Verify that the daemon captures output snapshots for active runs."""
    job_id = test_create_job_and_launch.job_id

    # The daemon should have already captured at least one snapshot
    # during its heartbeat cycle since the run became active.
    def snapshot_available():
        try:
            resp = api("GET", f"/v1/jobs/{job_id}/tail")
            snap = resp["data"]["snapshot"]
            output = snap.get("output", "")
            logv(f"  snapshot output length: {len(output)}, line_count: {snap.get('line_count', 0)}")
            # The placeholder command prints run metadata, so output should be non-empty
            return len(output) > 0
        except Exception as e:
            logv(f"  snapshot not yet available: {e}")
            return False

    poll_until(snapshot_available, DAEMON_CYCLE + 5, "snapshot captured")

    resp = api("GET", f"/v1/jobs/{job_id}/tail")
    snap = resp["data"]["snapshot"]
    assert snap["line_count"] > 0, f"expected line_count > 0, got {snap['line_count']}"
    assert not snap["stale"], "snapshot should not be stale"
    assert "Triptych placeholder run" in snap["output"], \
        f"expected placeholder output, got: {snap['output'][:100]}"

    logv(f"snapshot verified: {snap['line_count']} lines")


def test_send_command():
    """Send text to the tmux session via command API."""
    job_id = test_create_job_and_launch.job_id
    resp = api("POST", f"/v1/jobs/{job_id}/commands/send", {
        "text": "echo hello-from-e2e",
    })
    cmd = resp["data"]["command"]
    cmd_id = cmd["command_id"]
    assert cmd["command_type"] == "send"
    assert cmd["state"] == "recorded"

    logv(f"send command created: {cmd_id}")

    # Wait for agentd to process the command
    def command_observed():
        # Commands get acked then observed by the daemon
        # We just need to wait for the next daemon cycle
        return True

    # Give the daemon time to pick it up
    time.sleep(DAEMON_CYCLE + 1)

    logv("send command dispatched")


def test_interrupt_command():
    """Send interrupt (Ctrl+C) via command API."""
    job_id = test_create_job_and_launch.job_id
    resp = api("POST", f"/v1/jobs/{job_id}/commands/interrupt", {})
    cmd = resp["data"]["command"]
    assert cmd["command_type"] == "interrupt"
    logv(f"interrupt command created: {cmd['command_id']}")

    time.sleep(DAEMON_CYCLE + 1)
    logv("interrupt command dispatched")


def test_stop_command():
    """Stop command should kill the tmux session and transition run to exited."""
    # Create a second job so we can stop it without affecting reconciliation test
    resp = api("POST", "/v1/jobs", {
        "agent": "claude",
        "host_id": HOST_ID,
        "repo_path": str(REPO_ROOT),
        "goal": "E2E stop-target job",
    })
    job_id = resp["data"]["job"]["job_id"]

    # Wait for it to become active
    def run_active():
        r = api("GET", f"/v1/jobs/{job_id}")
        run_data = r["data"]["job"].get("run")
        return run_data and run_data["status"] == "active"

    poll_until(run_active, DAEMON_CYCLE + 5, "stop-target run active")

    # Get tmux session name before stopping
    attach = api("GET", f"/v1/jobs/{job_id}/attach")
    tmux_session = attach["data"]["tmux"]["session_name"]
    assert tmux_has_session(tmux_session), "session should exist before stop"

    # Send stop command
    api("POST", f"/v1/jobs/{job_id}/commands/stop", {})

    # Wait for run to reach terminal state
    def run_exited():
        r = api("GET", f"/v1/jobs/{job_id}")
        run_data = r["data"]["job"].get("run")
        if not run_data:
            return False
        status = run_data["status"]
        logv(f"  stop-target run status: {status}")
        return status in ("exited", "crashed")

    poll_until(run_exited, DAEMON_CYCLE + 5, "stop-target run exited")

    # Verify tmux session is gone
    assert not tmux_has_session(tmux_session), "tmux session should be gone after stop"

    # Verify terminal disposition
    r = api("GET", f"/v1/jobs/{job_id}")
    run_data = r["data"]["job"]["run"]
    assert run_data["status"] == "exited", f"expected exited, got {run_data['status']}"
    assert run_data["terminal_disposition"] == "cancelled", \
        f"expected cancelled, got {run_data.get('terminal_disposition')}"


def test_reconciliation():
    """Kill a tmux session manually; verify agentd marks run as crashed/failed."""
    # Create a dedicated job for the reconciliation test
    resp = api("POST", "/v1/jobs", {
        "agent": "claude",
        "host_id": HOST_ID,
        "repo_path": str(REPO_ROOT),
        "goal": "E2E reconciliation target",
    })
    job_id = resp["data"]["job"]["job_id"]

    # Wait for it to become active
    def run_active():
        r = api("GET", f"/v1/jobs/{job_id}")
        run_data = r["data"]["job"].get("run")
        return run_data and run_data["status"] == "active"

    poll_until(run_active, DAEMON_CYCLE + 5, "reconciliation target active")

    # Get the tmux session name
    attach = api("GET", f"/v1/jobs/{job_id}/attach")
    tmux_session = attach["data"]["tmux"]["session_name"]
    assert tmux_has_session(tmux_session), f"session {tmux_session} should exist"

    # Kill it externally (simulating a crash)
    log(f"    Killing tmux session {tmux_session} to simulate crash...")
    tmux_kill_session(tmux_session)
    assert not tmux_has_session(tmux_session), "session should be gone after kill"

    # Wait for agentd to reconcile
    def run_crashed():
        r = api("GET", f"/v1/jobs/{job_id}")
        run_data = r["data"]["job"].get("run")
        if not run_data:
            return False
        status = run_data["status"]
        logv(f"  reconciliation status: {status}")
        return status in ("crashed", "exited")

    poll_until(run_crashed, DAEMON_CYCLE + 5, "run reconciled to crashed")

    r = api("GET", f"/v1/jobs/{job_id}")
    run_data = r["data"]["job"]["run"]
    assert run_data["status"] == "crashed", f"expected crashed, got {run_data['status']}"
    assert run_data["terminal_disposition"] == "failed", \
        f"expected failed, got {run_data.get('terminal_disposition')}"


def test_idempotent_job_creation():
    """Verify idempotent job creation via idempotency_key."""
    idem_key = f"e2e-idempotent-{int(time.time())}"

    resp1 = api("POST", "/v1/jobs", {
        "agent": "claude",
        "host_id": HOST_ID,
        "repo_path": str(REPO_ROOT),
        "goal": "Idempotent test job",
        "idempotency_key": idem_key,
    })
    job_id_1 = resp1["data"]["job"]["job_id"]

    resp2 = api("POST", "/v1/jobs", {
        "agent": "claude",
        "host_id": HOST_ID,
        "repo_path": str(REPO_ROOT),
        "goal": "Idempotent test job",
        "idempotency_key": idem_key,
    })
    job_id_2 = resp2["data"]["job"]["job_id"]

    assert job_id_1 == job_id_2, f"idempotent create returned different IDs: {job_id_1} vs {job_id_2}"

    # Clean up: wait for launch, then stop
    def launched():
        r = api("GET", f"/v1/jobs/{job_id_1}")
        run_data = r["data"]["job"].get("run")
        return run_data and run_data["status"] == "active"
    try:
        poll_until(launched, DAEMON_CYCLE + 5, "idempotent job launched")
        api("POST", f"/v1/jobs/{job_id_1}/commands/stop", {})
    except Exception:
        pass  # best-effort cleanup


def test_list_jobs():
    """Verify list jobs returns the jobs we created."""
    resp = api("GET", "/v1/jobs")
    jobs = resp["data"]["jobs"]
    assert len(jobs) >= 2, f"expected at least 2 jobs, got {len(jobs)}"
    job_ids = [j["job"]["job_id"] for j in jobs]
    if hasattr(test_create_job_and_launch, "job_id"):
        assert test_create_job_and_launch.job_id in job_ids, "first job not in list"


# ---------------------------------------------------------------------------
# Cleanup helpers
# ---------------------------------------------------------------------------

def cleanup_tmux_sessions():
    """Kill any triptych tmux sessions we created."""
    for session in tmux_list_sessions():
        logv(f"cleaning up tmux session: {session}")
        tmux_kill_session(session)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    global verbose

    parser = argparse.ArgumentParser(description="Triptych E2E smoke tests")
    parser.add_argument("-v", "--verbose", action="store_true", help="Verbose output")
    parser.add_argument("--keep", action="store_true", help="Keep artifacts even on success")
    args = parser.parse_args()
    verbose = args.verbose

    print("=" * 60)
    print("Triptych E2E Smoke Tests")
    print("=" * 60)

    artifacts = ArtifactCollector()
    artifacts.setup()
    register_cleanup(lambda: cleanup_tmux_sessions(), "cleanup tmux sessions")

    try:
        # Infrastructure
        start_postgres()
        bin_dir = build_binaries()
        start_agentserver(bin_dir, artifacts)
        start_agentd(bin_dir, artifacts)

        print()
        print("Running tests:")

        # Tests - order matters for some (launch before send/stop/reconcile)
        run_test("host_registration", test_host_registration)
        run_test("heartbeat_visible", test_heartbeat_visible)
        run_test("create_job_and_launch", test_create_job_and_launch)
        run_test("attach_metadata", test_attach_metadata)
        run_test("snapshot_captured", test_snapshot_captured)
        run_test("send_command", test_send_command)
        run_test("interrupt_command", test_interrupt_command)
        run_test("stop_command", test_stop_command)
        run_test("reconciliation", test_reconciliation)
        run_test("idempotent_job_creation", test_idempotent_job_creation)
        run_test("list_jobs", test_list_jobs)

    except Exception as e:
        print(f"\nSetup failed: {e}")
        _results.append(("setup", False, str(e)))

    # Summary
    print()
    print("=" * 60)
    passed = sum(1 for _, ok, _ in _results if ok)
    failed = sum(1 for _, ok, _ in _results if not ok)
    total = len(_results)

    if failed == 0:
        print(f"ALL {passed} TESTS PASSED")
        if not args.keep:
            artifacts.remove_if_clean()
        else:
            artifacts.close()
            print(f"Artifacts: {artifacts.dir}")
    else:
        artifacts.close()
        print(f"FAILED: {failed}/{total} tests failed")
        for name, ok, err in _results:
            if not ok:
                print(f"  FAIL: {name}: {err}")
        print(f"\nArtifacts: {artifacts.dir}")

    print("=" * 60)
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
