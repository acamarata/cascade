package antigravity_test

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/procgroup"
	"github.com/acamarata/cascade/pkg/provider"
	ag "github.com/acamarata/cascade/providers/agents/antigravity"
)

// fixtureNow is the injected clock instant every kit envelope is minted at.
var fixtureNow = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)

// TestAntigravityPrintTimeoutCancels: a child that outlives its print
// timeout is killed with its whole group and the job ends failed with the
// timeout error, withholding its output.
func TestAntigravityPrintTimeoutCancels(t *testing.T) {
	requireProcessGroups(t)
	_, spec := kit(t)
	d := newKitDriver(t, spec.Env.Home, func(c *ag.Config) { c.Defaults.ApprovalTimeout = 200 * time.Millisecond })
	spec.Prompt = "hold"
	ctx := context.Background()
	start := time.Now()
	r, err := d.Spawn(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	cr, err := d.Collect(ctx, r.JobID)
	sentinel(t, err, ag.ErrPrintTimeout)
	if kind, _ := cascade.KindOf(err); kind != cascade.KindTimeout || cr.Output != "" {
		t.Fatalf("Collect = %+v, kind %v", cr, kind)
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatal("the job ended before its print timeout")
	}
	if st, _ := d.Status(ctx, r.JobID); st != provider.AgentRunFailed {
		t.Fatalf("Status = %q, want failed", st)
	}
	if !procgroup.Empty(r.ProcessGroupID) {
		t.Fatal("the process group outlived the print timeout")
	}
	if err := d.Cancel(ctx, r.JobID); err != nil {
		t.Fatalf("Cancel of a timed-out job = %v", err)
	}
}

// TestAntigravityDataClassJoined: the job's tier is the join of the spec's
// data class and the envelope's sensitivity, on Collect and on every event.
func TestAntigravityDataClassJoined(t *testing.T) {
	requireProcessGroups(t)
	d, spec := kit(t)
	ctx := context.Background()
	for _, env := range []provider.SensitivityTier{provider.SensitivityRestricted, provider.SensitivityInternal} {
		spec.DataClass, spec.Envelope.Sensitivity = provider.DataClassPublic, env
		want := provider.JoinSensitivity(provider.DataClassPublic, env)
		r, err := d.Spawn(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		cr, err := d.Collect(ctx, r.JobID)
		if err != nil || cr.DataClass != want || want != env {
			t.Fatalf("Collect tier %q (%v), want %q", cr.DataClass, err, want)
		}
		evs, err := d.Events(r.JobID)
		if err != nil || len(evs) < 3 {
			t.Fatalf("Events = %d, %v; want init, result and done", len(evs), err)
		}
		for _, e := range evs {
			if e.DataClass != want {
				t.Fatalf("event %d tier %q, want %q", e.Seq, e.DataClass, want)
			}
		}
	}
	_, err := d.Events("missing")
	sentinel(t, err, provider.ErrJobNotFound)
}

// TestAntigravityCancelConfirmsExit: Cancel returns nil only after the
// child exited; the group is gone and the job is cancelled at once.
func TestAntigravityCancelConfirmsExit(t *testing.T) {
	requireProcessGroups(t)
	d, spec := kit(t)
	spec.Prompt = "hold"
	ctx := context.Background()
	r, err := d.Spawn(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Cancel(ctx, r.JobID); err != nil {
		t.Fatal(err)
	}
	if st, _ := d.Status(ctx, r.JobID); st != provider.AgentRunCancelled {
		t.Fatalf("Status right after Cancel = %q, want cancelled", st)
	}
	if !procgroup.Empty(r.ProcessGroupID) {
		t.Fatal("Cancel returned while the process group was alive")
	}
	_, err = d.Collect(ctx, r.JobID)
	sentinel(t, err, ag.ErrRunCancelled)
	evs, err := d.Events(r.JobID)
	if err != nil || len(evs) == 0 || evs[len(evs)-1].Err != ag.ErrRunCancelled {
		t.Fatalf("last event does not record the cancel: %v", err)
	}
	if err := d.Cancel(ctx, r.JobID); err != nil {
		t.Fatalf("Cancel of a finished job = %v", err)
	}
}
