// Purpose: tests for the envelope conformance case: the suite passes a
//
//	driver that refuses every incomplete envelope and fails (non-vacuity)
//	a double that spawns anyway, one that ignores a single rule, and one
//	that refuses with an error that only looks like the sentinel.
//
// SPORT: providers.agents.conformance tests (ADD) - P1-AGT-02.
package conformance

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// envelopeDouble is a conformingProvider whose Spawn is replaced.
type envelopeDouble struct {
	*conformingProvider
	spawn func(*conformingProvider, context.Context, provider.AgentJobSpec) (provider.SpawnResult, error)
}

func (d envelopeDouble) Spawn(ctx context.Context, spec provider.AgentJobSpec) (provider.SpawnResult, error) {
	return d.spawn(d.conformingProvider, ctx, spec)
}

// envelopeSuite runs the case against a double built by spawn.
func envelopeSuite(t *testing.T, spawn func(*conformingProvider, context.Context, provider.AgentJobSpec) (provider.SpawnResult, error)) func(*testing.T) {
	t.Helper()
	s := &Suite{Worktree: t.TempDir(), New: func() (provider.AgentProvider, error) {
		return envelopeDouble{conformingProvider: newConformingProvider(), spawn: spawn}, nil
	}}
	return s.TestSpawnRefusesIncompleteEnvelope
}

func TestSuiteRefusesIncompleteEnvelopeCase(t *testing.T) {
	spawnAnyway := func(p *conformingProvider, ctx context.Context, spec provider.AgentJobSpec) (provider.SpawnResult, error) {
		spec.WorktreeRoot = "" // the in-process shape: the double skips its own check
		return p.Spawn(ctx, spec)
	}
	ignoresExpiry := func(p *conformingProvider, ctx context.Context, spec provider.AgentJobSpec) (provider.SpawnResult, error) {
		spec.Envelope.ExpiresAt = completeEnvelopeExpiry
		return p.Spawn(ctx, spec)
	}
	jobThenRefuse := func(p *conformingProvider, ctx context.Context, spec provider.AgentJobSpec) (provider.SpawnResult, error) {
		res, err := p.Spawn(ctx, spec)
		if err != nil {
			res = provider.SpawnResult{JobID: "started-anyway", ProcessGroupID: 1}
		}
		return res, err
	}
	lookAlike := func(*conformingProvider, context.Context, provider.AgentJobSpec) (provider.SpawnResult, error) {
		return provider.SpawnResult{}, cascade.New(cascade.KindPolicyDenied, provider.ErrEnvelopeIncomplete.Error())
	}
	honest := func(p *conformingProvider, ctx context.Context, spec provider.AgentJobSpec) (provider.SpawnResult, error) {
		return p.Spawn(ctx, spec)
	}
	for name, c := range map[string]struct {
		spawn func(*conformingProvider, context.Context, provider.AgentJobSpec) (provider.SpawnResult, error)
		fails bool
	}{
		"spawns despite an incomplete envelope": {spawnAnyway, true},
		"ignores the expiry rule":               {ignoresExpiry, true},
		"refuses with a look-alike error":       {lookAlike, true},
		"starts a job, then refuses":            {jobThenRefuse, true},
		"refuses every variant":                 {honest, false},
	} {
		if got := runIsolated(envelopeSuite(t, c.spawn)); got != c.fails {
			t.Errorf("%s: case failed = %v, want %v", name, got, c.fails)
		}
	}
}

func TestCompleteEnvelopePassesCheck(t *testing.T) {
	root := t.TempDir()
	if err := completeEnvelope(root).Check(root, doubleNow); err != nil {
		t.Fatalf("the suite's complete envelope fails Check: %v", err)
	}
	if err := completeEnvelope("").Check(root, doubleNow); err == nil {
		t.Fatal("an envelope with no worktree has roots")
	}
	if len(envelopeRefusals) < 6 {
		t.Fatalf("only %d refusal variants", len(envelopeRefusals))
	}
	for _, v := range envelopeRefusals {
		e := completeEnvelope(root)
		v.edit(&e, root)
		if e.Check(root, doubleNow) == nil {
			t.Errorf("variant %q is not actually incomplete", v.name)
		}
	}
}
