package antigravity_test

// Purpose: the present driver's non-run surface and the job lookups that
// refuse an unknown id, plus cancellation of a held job.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
	ag "github.com/acamarata/cascade/providers/agents/antigravity"
)

// TestAntigravityPresentSurface: a present driver reports its posture and
// the pinned protocol range, negotiates against it, and refuses model calls.
func TestAntigravityPresentSurface(t *testing.T) {
	d, _ := kit(t)
	ctx := context.Background()
	caps, err := d.Capabilities(ctx, "")
	if err != nil || !caps.CompliancePosture.ProgrammaticEntitlement {
		t.Fatalf("Capabilities = %+v, %v", caps, err)
	}
	if got := d.SupportedProtocols(); got != ag.SupportedProtocols() {
		t.Fatalf("SupportedProtocols = %+v, want %+v", got, ag.SupportedProtocols())
	}
	if _, err := d.Negotiate(ctx, ag.SupportedProtocols()); err != nil {
		t.Fatalf("Negotiate: %v", err)
	}
	if _, err := d.Chat(ctx, provider.ChatRequest{}); err == nil {
		t.Fatal("Chat succeeded on the agent surface")
	}
}

// TestAntigravityUnknownJobRefuses: every lookup of an id the driver never
// spawned is ErrJobNotFound.
func TestAntigravityUnknownJobRefuses(t *testing.T) {
	d, _ := kit(t)
	ctx := context.Background()
	if _, err := d.Status(ctx, "missing"); !errors.Is(err, provider.ErrJobNotFound) {
		t.Errorf("Status = %v", err)
	}
	if err := d.Cancel(ctx, "missing"); !errors.Is(err, provider.ErrJobNotFound) {
		t.Errorf("Cancel = %v", err)
	}
	if _, err := d.Collect(ctx, "missing"); !errors.Is(err, provider.ErrJobNotFound) {
		t.Errorf("Collect = %v", err)
	}
	if _, err := d.Artifacts(ctx, "missing"); !errors.Is(err, provider.ErrJobNotFound) {
		t.Errorf("Artifacts = %v", err)
	}
}

// TestAntigravityGeneratesJobIDAndReportsTerminal: a spec without a job id
// gets a generated one; after Collect the job is accepted, has no
// artifacts, and a late Cancel is a no-op.
func TestAntigravityGeneratesJobIDAndReportsTerminal(t *testing.T) {
	requireProcessGroups(t)
	d, spec := kit(t)
	spec.JobID = ""
	ctx := context.Background()
	r, err := spawnWithin(ctx, d, spec, spawnLimit(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(r.JobID), "antigravity-") || len(r.JobID) != len("antigravity-")+32 {
		t.Fatalf("generated job id = %q", r.JobID)
	}
	if _, err := d.Collect(ctx, r.JobID); err != nil {
		t.Fatal(err)
	}
	if st, err := d.Status(ctx, r.JobID); err != nil || st != provider.AgentRunAccepted {
		t.Fatalf("Status = %q, %v", st, err)
	}
	if paths, err := d.Artifacts(ctx, r.JobID); err != nil || paths == nil || len(paths) != 0 {
		t.Fatalf("Artifacts = %v, %v", paths, err)
	}
	if err := d.Cancel(ctx, r.JobID); err != nil {
		t.Fatalf("Cancel after terminal = %v", err)
	}
}

// TestAntigravityCancelHeldJob: Collect under a canceled context returns
// ErrRunCancelled without waiting; Cancel kills the held child and the job
// ends cancelled.
func TestAntigravityCancelHeldJob(t *testing.T) {
	requireProcessGroups(t)
	d, spec := kit(t)
	spec.Prompt = "hold"
	ctx := context.Background()
	r, err := d.Spawn(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(spec.Env.Home, "release"), nil, 0o600) })
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.Collect(canceled, r.JobID); !errors.Is(err, ag.ErrRunCancelled) {
		t.Fatalf("Collect(canceled) = %v, want ErrRunCancelled", err)
	}
	if err := d.Cancel(ctx, r.JobID); err != nil {
		t.Fatalf("Cancel = %v", err)
	}
	if st, err := d.Status(ctx, r.JobID); err != nil || st != provider.AgentRunCancelled {
		t.Fatalf("Status after Cancel = %q, %v", st, err)
	}
}

// fixedClock is the driver's injected clock in these tests.
type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

// sentinel asserts an error by identity and message.
func sentinel(t *testing.T, got, want error) {
	t.Helper()
	if got != want || got == nil || got.Error() != want.Error() {
		t.Fatalf("error = %v, want %v", got, want)
	}
}

// requireProcessGroups skips tests that spawn the child: the driver runs it
// in a native process group, which only unix provides.
func requireProcessGroups(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("native process groups require unix")
	}
}

// spawnWithin retries a Spawn that failed to start its child (fork or exec
// refused under machine load) until limit passes; any other error returns at once.
func spawnWithin(ctx context.Context, d *ag.Driver, spec provider.AgentJobSpec, limit time.Duration) (provider.SpawnResult, error) {
	end := time.Now().Add(limit)
	for {
		r, err := d.Spawn(ctx, spec)
		if !errors.Is(err, ag.ErrStart) || time.Now().After(end) {
			return r, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// spawnLimit stretches base by CASCADE_TEST_DEADLINE_SCALE. This package's
// tests may not import an internal/ path (noInternalImports), so it cannot
// use internal/testkit.Deadline and mirrors only its environment scale.
func spawnLimit(base time.Duration) time.Duration {
	if v, err := strconv.ParseFloat(os.Getenv("CASCADE_TEST_DEADLINE_SCALE"), 64); err == nil && v > 1 {
		return time.Duration(float64(base) * v)
	}
	return base
}
