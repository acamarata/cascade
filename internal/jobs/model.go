package jobs

// Purpose: the jobs domain's typed records -- the seven tables' Go shapes
//
//	-- plus the two closed-vocabulary columns this ticket owns the decode
//	of: ConsequenceClass (R-21.99) and DataClass (R-21.94). RiskClass,
//	MinTaskClass and NodeRequirements stay plain strings/JSON blobs on
//	Job because their closed sets (if any) are owned by other tickets
//	(AC/S-59.T4 for risk class, K/S-22.T4 for task class per
//	pkg/provider.ModelRequest.TaskClass's own identical precedent) --
//	redeclaring a second enum here before that ticket lands would be the
//	Article-1 "capability claimed beyond this ticket's scope" violation.
//
// Inputs: none (types only). Decode functions take a stored TEXT value.
// Outputs: typed records for store_job.go/store_exec.go/store_lease.go;
//
//	*cascade.Error from the two closed-vocabulary decoders on an
//	unknown/unparseable value -- never a permissive zero value.
//
// Constraints: exactly the DECIDED field sets this ticket's contract
//
//	lists, plus the W9/W6 columns HOW steps 2b/2c add. See this ticket's
//	journal for the DataClass-vocabulary decision (a package-local type
//	rather than importing internal/policy.DataClass) and the
//	consecutive_failed_attempts DEFAULT 0 contract/DSL contradiction.
//
// SPORT: jobs/domain-schema/ADD (P1-E29-W6-S59-T1).

import "github.com/acamarata/cascade/pkg/cascade"

// Job is the R-16.37 DAG-node record: the DECIDED field set
// {capabilities[], deps[], mutable_scope, risk_class, min task class,
// node_requirements, timeout, cost_ceiling, priority} plus id/state/
// timestamps. deps[] is materialized separately as TaskDependency rows,
// never a column here (the DECIDED shape names it as an edge list, and
// this ticket's migration puts edges in their own table so referential
// integrity is store-enforced per job, not parsed out of a blob).
type Job struct {
	ID        string
	State     JobState
	CreatedAt int64 // unix seconds, injected clock
	UpdatedAt int64 // unix seconds, injected clock

	Capabilities     []string // JSON-encoded in storage
	MutableScope     string
	RiskClass        string // owned by AC/S-59.T4; stored verbatim
	MinTaskClass     string // owned by K/S-22.T4; stored verbatim
	NodeRequirements string // JSON blob, owned by the DAG planner (T4)
	TimeoutSeconds   int64
	CostCeiling      float64
	Priority         int

	// ConsecutiveFailedAttempts is R-21.84's counter. This ticket ships
	// the column NOT NULL with an application-layer default of 0 on
	// insert (see journal: the DSL has no SQL DEFAULT); the increment on
	// TASK_FAILED and the reset are AP/S-81.T3's.
	ConsecutiveFailedAttempts int64
	// ConsequenceClass is R-21.99's closed set; derivation is AQ/S-83.T4's.
	ConsequenceClass ConsequenceClass
	// DataClass is R-21.94's request floor; the lattice join is
	// AQ/S-83.T1-T4's.
	DataClass DataClass
}

// TaskDependency materializes one deps[] edge: job depends on
// depends_on_job_id.
type TaskDependency struct {
	JobID          string
	DependsOnJobID string
}

// Execution is one attempt at running Job.ID (R-16.33: attempts are
// numbered per job). PGID and HeartbeatAt are R-21.140/R-21.172's W6
// liveness columns -- recorded verbatim, never probed or reaped here
// (AC/S-59.T3 and AC/S-59.T5 own that).
type Execution struct {
	ID        string
	JobID     string
	Attempt   int
	State     ExecutionState
	StartedAt int64 // unix seconds; 0 means not yet started
	EndedAt   int64 // unix seconds; 0 means not yet ended

	// PGID is the driver process-group id recorded at spawn (R-21.140).
	// 0 means not recorded (a real pgid is always > 0 on POSIX).
	PGID int64
	// HeartbeatAt is the last liveness signal's timestamp (R-21.172).
	// 0 means no heartbeat recorded yet.
	HeartbeatAt int64
}

// ExecutionResult is one execution's outcome record.
type ExecutionResult struct {
	ExecutionID   string
	OutputSummary string
	// ErrorKind/ErrorMessage are empty when the execution succeeded.
	// ErrorKind, when set, is one of pkg/cascade's frozen 14 kind names.
	ErrorKind    string
	ErrorMessage string
	// ArtifactRefs is the ordered list of Artifact.ID values this
	// execution produced. JSON-encoded in storage.
	ArtifactRefs []string
}

// Artifact is one produced file/blob, content-addressed through the
// B/S-02.T1 BlobStore's BLAKE3 key. DataClass is IMMUTABLE once written
// (R-21.94); store_exec.go refuses any update that changes it.
type Artifact struct {
	ID          string
	JobID       string
	ExecutionID string
	Kind        string
	BlobKey     string // BLAKE3 content-addressed key
	Path        string

	ConsequenceClass ConsequenceClass
	DataClass        DataClass
}

// ResourceLease is the DECIDED lease record verbatim: {repo id,
// scope_glob, holder (job id), issued, ttl, renew_count, journal ref},
// plus R-21.139's Epoch/State fencing columns. (RepoID, ScopeGlob) is the
// natural key -- "one mutable writer per lease scope" means no
// surrogate id is needed or DECIDED.
type ResourceLease struct {
	RepoID     string
	ScopeGlob  string
	Holder     string // job id
	IssuedAt   int64  // unix seconds
	TTLSeconds int64
	RenewCount int64
	JournalRef string

	// Epoch is R-21.139's monotonic fence, per (RepoID, ScopeGlob). The
	// fence CHECK on every mutation is AC/S-59.T2's; this ticket only
	// stores the column and refuses a lowering UPDATE.
	Epoch int64
	// State is R-21.139/R-21.177's closed lease-state set.
	State LeaseState
}

// Worktree is one job's checked-out working tree, bound to the
// ResourceLease that authorizes writes into it. The R-16.37 path/branch
// naming constants (<repo>/.cascade/worktrees/job-<id>, job/<id>) are
// APPLIED by T3; this ticket only stores whatever path/branch the caller
// supplies.
//
// LeaseRepoID/LeaseScopeGlob reference ResourceLease's composite natural
// key. The migrate DSL's ForeignKeyDef supports only a single-column
// reference (see migration.go's doc comment), so this binding is
// store-enforced in store_lease.go, not a DB-level composite FOREIGN KEY.
type Worktree struct {
	Path           string // PK: one worktree per filesystem path
	LeaseRepoID    string
	LeaseScopeGlob string
	Repo           string
	Branch         string
}

// ConsequenceClass is R-21.99's closed set over job/artifact metadata.
// The zero value is deliberately not a member (fail-closed default).
type ConsequenceClass string

// The closed ConsequenceClass vocabulary.
const (
	ConsequenceTrivial       ConsequenceClass = "trivial"
	ConsequenceNormal        ConsequenceClass = "normal"
	ConsequenceConsequential ConsequenceClass = "consequential"
)

// Valid reports whether c is one of the three closed values.
func (c ConsequenceClass) Valid() bool {
	switch c {
	case ConsequenceTrivial, ConsequenceNormal, ConsequenceConsequential:
		return true
	}
	return false
}

// DecodeConsequenceClass parses a stored TEXT value into a
// ConsequenceClass. An unknown or empty value returns a typed A-T7 error
// -- there is no permissive zero value.
func DecodeConsequenceClass(raw string) (ConsequenceClass, error) {
	c := ConsequenceClass(raw)
	if !c.Valid() {
		return "", cascade.Newf(cascade.KindInvalidInput,
			"jobs: unknown consequence_class %q: must be one of trivial, normal, consequential", raw)
	}
	return c, nil
}

// DataClass is this package's closed data-sensitivity floor (R-21.94).
// It mirrors internal/policy.DataClass's four-value R-21.27 ordering by
// VALUE, not by import -- see this ticket's journal for why a direct
// cross-package enum import was rejected for this ticket's inert column.
// The zero value is deliberately not a member.
type DataClass string

// The closed DataClass vocabulary, matching internal/policy.DataClass's
// names exactly so a future caller that DOES join the two (AQ/S-83) sees
// identical wire values.
const (
	DataClassPublic       DataClass = "public"
	DataClassInternal     DataClass = "internal"
	DataClassConfidential DataClass = "confidential"
	DataClassSecret       DataClass = "secret"
)

// Valid reports whether d is one of the four closed values.
func (d DataClass) Valid() bool {
	switch d {
	case DataClassPublic, DataClassInternal, DataClassConfidential, DataClassSecret:
		return true
	}
	return false
}

// DecodeDataClass parses a stored TEXT value into a DataClass. An unknown
// or empty value returns a typed A-T7 error -- there is no permissive
// zero value (the fail-closed direction here is refusal, not silently
// substituting DataClassSecret the way internal/policy's own
// safeDataClass does for a DIFFERENT, evaluation-time concern).
func DecodeDataClass(raw string) (DataClass, error) {
	d := DataClass(raw)
	if !d.Valid() {
		return "", cascade.Newf(cascade.KindInvalidInput,
			"jobs: unknown data_class %q: must be one of public, internal, confidential, secret", raw)
	}
	return d, nil
}

// ExecutionState is the execution-row lifecycle. The contract text names
// only that the set "includes abandoned" (R-21.172) without enumerating
// the rest; this ticket fills that gap with the minimal closed set an
// execution attempt needs (running/succeeded/failed/abandoned),
// documented in the journal as a contract gap filled reasonably rather
// than guessed at silently. Unknown/unparseable decodes to failed, the
// same fail-closed direction JobState uses.
type ExecutionState string

// The closed ExecutionState vocabulary.
const (
	ExecutionRunning   ExecutionState = "running"
	ExecutionSucceeded ExecutionState = "succeeded"
	ExecutionFailed    ExecutionState = "failed"
	ExecutionAbandoned ExecutionState = "abandoned"
)

// Valid reports whether s is one of the four closed values.
func (s ExecutionState) Valid() bool {
	switch s {
	case ExecutionRunning, ExecutionSucceeded, ExecutionFailed, ExecutionAbandoned:
		return true
	}
	return false
}

// DecodeExecutionState parses a stored TEXT value. Unknown/unparseable
// resolves to ExecutionFailed -- never a panic, never a permissive
// "running" default.
func DecodeExecutionState(raw string) ExecutionState {
	s := ExecutionState(raw)
	if !s.Valid() {
		return ExecutionFailed
	}
	return s
}

// LeaseState is R-21.139/R-21.177's closed lease-state set.
type LeaseState string

// The closed LeaseState vocabulary.
const (
	LeaseHeld               LeaseState = "held"
	LeaseRenewing           LeaseState = "renewing"
	LeaseExpiredUnconfirmed LeaseState = "expired_unconfirmed"
	LeaseExpiredOrphaned    LeaseState = "expired_orphaned"
	LeaseReleased           LeaseState = "released"
)

// Valid reports whether s is one of the five closed values.
func (s LeaseState) Valid() bool {
	switch s {
	case LeaseHeld, LeaseRenewing, LeaseExpiredUnconfirmed, LeaseExpiredOrphaned, LeaseReleased:
		return true
	}
	return false
}

// DecodeLeaseState parses a stored TEXT value into a LeaseState. An
// unknown or empty value returns a typed A-T7 error -- there is no
// permissive zero value.
func DecodeLeaseState(raw string) (LeaseState, error) {
	s := LeaseState(raw)
	if !s.Valid() {
		return "", cascade.Newf(cascade.KindInvalidInput,
			"jobs: unknown lease state %q: must be one of held, renewing, expired_unconfirmed, expired_orphaned, released", raw)
	}
	return s, nil
}
