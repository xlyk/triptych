package domain

import "testing"

func TestIDValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{name: "host", err: HostID("client-a-mbp").Validate()},
		{name: "job", err: JobID("job_123").Validate()},
		{name: "run", err: RunID("run_123").Validate()},
		{name: "command", err: CommandID("cmd_123").Validate()},
		{name: "event", err: EventID("evt_123").Validate()},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.err != nil {
				t.Fatalf("Validate() error = %v", tt.err)
			}
		})
	}

	if err := JobID("   ").Validate(); err == nil {
		t.Fatal("expected blank job id to fail validation")
	}
}

func TestJobStatusValidate(t *testing.T) {
	t.Parallel()

	valid := []JobStatus{
		JobStatusQueued,
		JobStatusAssigned,
		JobStatusLaunching,
		JobStatusRunning,
		JobStatusWaitingForInput,
		JobStatusBlocked,
		JobStatusCompleted,
		JobStatusFailed,
		JobStatusCancelled,
		JobStatusArchived,
	}

	for _, status := range valid {
		if err := status.Validate(); err != nil {
			t.Fatalf("status %q should validate: %v", status, err)
		}
	}

	if err := JobStatus("bogus").Validate(); err == nil {
		t.Fatal("expected invalid job status to fail")
	}
}

func TestRunStatusValidate(t *testing.T) {
	t.Parallel()

	valid := []RunStatus{
		RunStatusPendingLaunch,
		RunStatusStarting,
		RunStatusActive,
		RunStatusWaiting,
		RunStatusStopping,
		RunStatusExited,
		RunStatusCrashed,
	}

	for _, status := range valid {
		if err := status.Validate(); err != nil {
			t.Fatalf("status %q should validate: %v", status, err)
		}
	}

	if err := RunStatus("bogus").Validate(); err == nil {
		t.Fatal("expected invalid run status to fail")
	}
}

func TestJobCreateRequestValidateAndNormalize(t *testing.T) {
	t.Parallel()

	req := JobCreateRequest{
		Agent:          AgentCodex,
		HostID:         HostID("client-a-mbp"),
		RepoPath:       "/Users/kyle/work/client-a/repo",
		Goal:           "  Fix the flaky webhook retry tests.  ",
		IdempotencyKey: "req-123",
	}

	if err := ValidateJobCreateRequest(req); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	got := req.Normalize()
	if got.Goal != "Fix the flaky webhook retry tests." {
		t.Fatalf("Normalize() goal = %q", got.Goal)
	}
	if got.Workdir != req.RepoPath {
		t.Fatalf("Normalize() workdir = %q", got.Workdir)
	}
	if got.Priority != PriorityNormal {
		t.Fatalf("Normalize() priority = %q", got.Priority)
	}
	if got.MaxDuration != "4h" {
		t.Fatalf("Normalize() max_duration = %q", got.MaxDuration)
	}
	if got.Metadata == nil {
		t.Fatal("Normalize() should default metadata")
	}
}

func TestJobCreateRequestValidateRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	req := JobCreateRequest{
		Agent:          Agent("bad"),
		HostID:         HostID(""),
		RepoPath:       "relative/path",
		Goal:           "   ",
		Workdir:        "tmp",
		Priority:       Priority("urgent"),
		MaxDuration:    "-1s",
		IdempotencyKey: "   ",
	}

	if err := req.Validate(); err == nil {
		t.Fatal("expected invalid request to fail validation")
	}
}

func TestMutatingCommandRequestIdentityValidate(t *testing.T) {
	t.Parallel()

	valid := MutatingCommandRequestIdentity{
		RunID:                 RunID("run_123"),
		CommandType:           CommandTypeSend,
		RequestIdempotencyKey: "req-123",
		PayloadFingerprint:    "sha256:abc123",
	}

	if err := ValidateMutatingCommandRequestIdentity(valid); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	invalid := MutatingCommandRequestIdentity{
		RunID:                 RunID(""),
		CommandType:           CommandType("nope"),
		RequestIdempotencyKey: " ",
		PayloadFingerprint:    "",
	}

	if err := invalid.Validate(); err == nil {
		t.Fatal("expected invalid command identity to fail validation")
	}
}
