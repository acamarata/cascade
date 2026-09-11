package conformance

// Purpose: Suite — the driver-agnostic AgentProvider conformance harness
//
//	(AD/S-61.T1). Any implementation of pkg/provider.AgentProvider,
//	including one outside this repo, passes it by satisfying the
//	CONTRACT, never by matching an in-tree implementation detail.
//
// Inputs: a Factory that constructs a fresh AgentProvider per case.
// Outputs: t.Fatal/t.Error failures through the supplied *testing.T, the
//
//	standard Go test idiom (cf. internal/storage/storetest, the shape
//	this package follows).
//
// Constraints: this package is a test-helper LIBRARY (imported by each
//
//	driver's own _test.go, per storetest's precedent) so it is not itself
//	suffixed _test.go, though it imports "testing". It asserts only the
//	contract: never a type switch on the concrete AgentProvider, never a
//	field read outside the interface. No driver implementation ships from
//	this ticket; a driver appends its own factory entry to the sibling
//	_test.go files (append-only, so concurrent driver tickets do not
//	collide). Split across suite.go (this file, the ten original cases)
//	and cases_lifecycle.go/cases_security.go (the Round-35 amendment
//	cases) to stay under the 300-line cap per file.
//
// SPORT: providers.agents.conformance/ADD (P1-E30-W6-S61-T1).

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// Factory constructs a fresh AgentProvider for one case. The suite never
// assumes a shared or pre-seeded provider across cases.
type Factory func() (provider.AgentProvider, error)

// Suite is the conformance harness. New is required; Worktree names a
// helper directory the worktree-scoped cases (PreSpawnScan et al.) may
// use — callers typically pass t.TempDir().
type Suite struct {
	New      Factory
	Worktree string
}

// newProvider constructs a fresh provider via s.New, skipping the case
// when s.New is nil (no driver wired to this suite instance yet) and
// failing it when the factory itself errors.
func (s *Suite) newProvider(t *testing.T) provider.AgentProvider {
	t.Helper()
	if s.New == nil {
		t.Skip("conformance: no factory wired")
	}
	p, err := s.New()
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	return p
}

// testContext returns a background context bounded by a generous per-case
// deadline, so a provider that deadlocks fails the case instead of
// hanging the suite forever.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// defaultSpec returns a minimal, valid AgentJobSpec every case can spawn
// without asserting anything about job content.
func defaultSpec() provider.AgentJobSpec {
	return provider.AgentJobSpec{Prompt: "conformance", DataClass: provider.DataClassInternal}
}

// requireNoError fails the case with msg and err's detail if err is
// non-nil.
func requireNoError(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
}

// pollAttempts bounds pollUntilTerminal's busy-poll loop. internal/build's
// no-sleep hygiene gate (Art.7.3) denies time.Sleep outside a narrow
// allowlist this ticket's files are not on, so this helper polls Status
// in a tight, iteration-bounded loop rather than sleeping between checks
// — acceptable for a test helper with a hard iteration cap, never a
// production synchronization primitive.
const pollAttempts = 20000

// pollUntilTerminal polls Status up to pollAttempts times, stopping early
// once it reports a terminal AgentRunState, and returns the last observed
// state. It never reads the wall clock and never sleeps.
func pollUntilTerminal(t *testing.T, p provider.AgentProvider, id provider.AgentJobID) provider.AgentRunState {
	t.Helper()
	ctx := testContext(t)
	var last provider.AgentRunState
	for i := 0; i < pollAttempts; i++ {
		s, err := p.Status(ctx, id)
		if err != nil {
			t.Fatalf("Status while polling: %v", err)
		}
		last = s
		if s.Terminal() {
			return s
		}
	}
	return last
}

// TestSpawnHappyPath asserts Spawn returns a usable SpawnResult: a
// non-empty JobID and a non-negative ProcessGroupID.
func (s *Suite) TestSpawnHappyPath(t *testing.T) {
	p := s.newProvider(t)
	res, err := p.Spawn(testContext(t), defaultSpec())
	requireNoError(t, err, "Spawn")
	if res.JobID == "" {
		t.Fatal("Spawn returned an empty JobID")
	}
	if res.ProcessGroupID < 0 {
		t.Fatalf("Spawn returned a negative ProcessGroupID %d", res.ProcessGroupID)
	}
}

// TestSpawnErrEntitlement asserts Spawn's entitlement refusal is
// internally consistent with the same provider's declared
// CompliancePosture: refuses with ErrEntitlement when not entitled,
// succeeds when it is. A provider that is always entitled cannot be
// forced into the refusal branch generically; this case still fails a
// provider that ignores its own declared posture in either direction.
func (s *Suite) TestSpawnErrEntitlement(t *testing.T) {
	p := s.newProvider(t)
	ctx := testContext(t)
	caps, err := p.Capabilities(ctx, "")
	requireNoError(t, err, "Capabilities")
	_, spawnErr := p.Spawn(ctx, defaultSpec())
	if caps.CompliancePosture.ProgrammaticEntitlement {
		if spawnErr != nil {
			t.Fatalf("Spawn with declared entitlement returned %v, want nil", spawnErr)
		}
		return
	}
	if spawnErr != provider.ErrEntitlement {
		t.Fatalf("Spawn without declared entitlement returned %v, want ErrEntitlement", spawnErr)
	}
}

// TestMessageAfterSpawn asserts Message succeeds against a job that
// exists and returns ErrJobNotFound against one that does not.
func (s *Suite) TestMessageAfterSpawn(t *testing.T) {
	p := s.newProvider(t)
	ctx := testContext(t)
	res, err := p.Spawn(ctx, defaultSpec())
	requireNoError(t, err, "Spawn")
	requireNoError(t, p.Message(ctx, res.JobID, "next turn"), "Message")
	if err := p.Message(ctx, provider.AgentJobID("conformance-missing-job"), "x"); err != provider.ErrJobNotFound {
		t.Fatalf("Message(missing job) = %v, want ErrJobNotFound", err)
	}
}

// TestStatusReturnsKnownState asserts Status returns a member of the
// closed AgentRunState vocabulary for a freshly spawned job.
func (s *Suite) TestStatusReturnsKnownState(t *testing.T) {
	p := s.newProvider(t)
	ctx := testContext(t)
	res, err := p.Spawn(ctx, defaultSpec())
	requireNoError(t, err, "Spawn")
	state, err := p.Status(ctx, res.JobID)
	requireNoError(t, err, "Status")
	if !state.Valid() {
		t.Fatalf("Status returned %q, not a member of the closed AgentRunState vocabulary", state)
	}
}

// TestCancelRunning asserts Cancel against a live job succeeds (or
// reports the honest ErrCancelUnconfirmed) and the job never silently
// stays running forever.
func (s *Suite) TestCancelRunning(t *testing.T) {
	p := s.newProvider(t)
	ctx := testContext(t)
	res, err := p.Spawn(ctx, defaultSpec())
	requireNoError(t, err, "Spawn")
	cancelErr := p.Cancel(ctx, res.JobID)
	if cancelErr != nil && cancelErr != provider.ErrCancelUnconfirmed {
		t.Fatalf("Cancel = %v, want nil or ErrCancelUnconfirmed", cancelErr)
	}
	final := pollUntilTerminal(t, p, res.JobID)
	if final == provider.AgentRunRunning {
		t.Fatal("job is still running after Cancel and a bounded poll")
	}
}

// TestCancelNonExistent asserts Cancel of an unknown job id returns
// ErrJobNotFound.
func (s *Suite) TestCancelNonExistent(t *testing.T) {
	p := s.newProvider(t)
	if err := p.Cancel(testContext(t), provider.AgentJobID("conformance-missing-job")); err != provider.ErrJobNotFound {
		t.Fatalf("Cancel(missing) = %v, want ErrJobNotFound", err)
	}
}

// TestCollectBlocksUntilDone asserts Collect returns without hanging the
// case's bounded context and yields a non-nil error only for a job that
// never reaches a terminal state within the deadline.
func (s *Suite) TestCollectBlocksUntilDone(t *testing.T) {
	p := s.newProvider(t)
	ctx := testContext(t)
	res, err := p.Spawn(ctx, defaultSpec())
	requireNoError(t, err, "Spawn")
	if _, err := p.Collect(ctx, res.JobID); err != nil {
		t.Fatalf("Collect: %v", err)
	}
}

// TestCollectBeforeSpawn asserts Collect against a job id that was never
// spawned returns a non-nil typed error.
func (s *Suite) TestCollectBeforeSpawn(t *testing.T) {
	p := s.newProvider(t)
	if _, err := p.Collect(testContext(t), provider.AgentJobID("conformance-never-spawned")); err == nil {
		t.Fatal("Collect(never spawned) returned nil error")
	}
}

// TestArtifactsEmptyNotNil asserts Artifacts returns a non-nil slice for
// a job with no artifacts.
func (s *Suite) TestArtifactsEmptyNotNil(t *testing.T) {
	p := s.newProvider(t)
	ctx := testContext(t)
	res, err := p.Spawn(ctx, defaultSpec())
	requireNoError(t, err, "Spawn")
	artifacts, err := p.Artifacts(ctx, res.JobID)
	requireNoError(t, err, "Artifacts")
	if artifacts == nil {
		t.Fatal("Artifacts returned a nil slice, want non-nil (possibly empty)")
	}
}

// TestNormalizeEventRoundTrip asserts provider.NormalizeEvent's contract
// independent of any one driver: every known DriverEventKind normalizes
// without error and an unknown one refuses.
func (s *Suite) TestNormalizeEventRoundTrip(t *testing.T) {
	for _, k := range []provider.DriverEventKind{
		provider.DriverEventStdout, provider.DriverEventStatus,
		provider.DriverEventError, provider.DriverEventDone,
	} {
		if _, err := provider.NormalizeEvent(provider.DriverEvent{Kind: k, DataClass: provider.DataClassPublic}); err != nil {
			t.Fatalf("NormalizeEvent(%v): %v", k, err)
		}
	}
	if _, err := provider.NormalizeEvent(provider.DriverEvent{Kind: provider.DriverEventUnknown}); err == nil {
		t.Fatal("NormalizeEvent(unknown) returned nil error")
	}
}
