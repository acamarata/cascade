package conformance

// Purpose: the spawn-envelope conformance case (P1-AGT-02) and the complete
//
//	test envelope Suite.newSpec fills: an external driver must refuse a
//	Spawn whose SpawnEnvelope is incomplete, by ErrEnvelopeIncomplete
//	identity, and must not return a job.
//
// Constraints: no clock is read here; the complete envelope expires at a
//
//	fixed far-future instant, so it is complete under any driver clock.
//
// SPORT: providers.agents.conformance/ADD (P1-AGT-02).

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// completeEnvelopeExpiry is the fixed far-future expiry of the test envelope.
var completeEnvelopeExpiry = time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)

// completeEnvelope returns an envelope that passes SpawnEnvelope.Check for a
// spawn in worktree: one root equal to it, a well-formed digest, a principal
// and a far-future expiry. An empty worktree yields no roots.
func completeEnvelope(worktree string) provider.SpawnEnvelope {
	env := provider.SpawnEnvelope{
		Digest:      strings.Repeat("c0", 32),
		Principal:   "conformance-principal",
		Sensitivity: provider.SensitivityInternal,
		ExpiresAt:   completeEnvelopeExpiry,
	}
	if worktree != "" {
		env.Roots = []string{filepath.ToSlash(worktree)}
	}
	return env
}

// envelopeRefusal is one incomplete-envelope variant of a complete one.
type envelopeRefusal struct {
	name string
	edit func(e *provider.SpawnEnvelope, worktree string)
}

// envelopeRefusals are the variants TestSpawnRefusesIncompleteEnvelope tries:
// one broken rule each, plus the zero envelope.
var envelopeRefusals = []envelopeRefusal{
	{"zero envelope", func(e *provider.SpawnEnvelope, _ string) { *e = provider.SpawnEnvelope{} }},
	{"digest not hex", func(e *provider.SpawnEnvelope, _ string) { e.Digest = "not-a-digest" }},
	{"empty principal", func(e *provider.SpawnEnvelope, _ string) { e.Principal = "" }},
	{"no roots", func(e *provider.SpawnEnvelope, _ string) { e.Roots = nil }},
	{"worktree outside roots", func(e *provider.SpawnEnvelope, w string) { e.Roots = []string{filepath.ToSlash(w) + "-other"} }},
	{"expired", func(e *provider.SpawnEnvelope, _ string) { e.ExpiresAt = time.Unix(1, 0).UTC() }},
	{"relative root", func(e *provider.SpawnEnvelope, _ string) { e.Roots = []string{"relative/root"} }},
	{"unclean root", func(e *provider.SpawnEnvelope, w string) { e.Roots = []string{filepath.ToSlash(w) + "/x/.."} }},
	// A string prefix of the worktree, not a path-segment prefix.
	{"sibling prefix", func(e *provider.SpawnEnvelope, w string) {
		e.Roots = []string{filepath.ToSlash(w)[:len(filepath.ToSlash(w))-1]}
	}},
}

// TestSpawnRefusesIncompleteEnvelope asserts Spawn refuses each incomplete
// envelope with an error that wraps provider.ErrEnvelopeIncomplete by
// identity (errors.Is would also pass any policy-denied error) and a zero
// SpawnResult: a driver that started a job and then refused fails too.
func (s *Suite) TestSpawnRefusesIncompleteEnvelope(t *testing.T) {
	root := s.Worktree
	if root == "" {
		root = t.TempDir()
	}
	for _, v := range envelopeRefusals {
		p := s.newProvider(t)
		spec := s.newSpec()
		spec.WorktreeRoot, spec.Envelope = root, completeEnvelope(root)
		v.edit(&spec.Envelope, root)
		res, err := p.Spawn(testContext(t), spec)
		if err == nil {
			t.Fatalf("%s: Spawn succeeded (job %q) with an incomplete envelope", v.name, res.JobID)
		}
		if !wrapsByIdentity(err, provider.ErrEnvelopeIncomplete) {
			t.Fatalf("%s: Spawn error %v does not wrap provider.ErrEnvelopeIncomplete", v.name, err)
		}
		if res != (provider.SpawnResult{}) {
			t.Fatalf("%s: Spawn refused but returned a job %+v", v.name, res)
		}
	}
}

// wrapsByIdentity reports whether target is err or any error err wraps,
// compared by identity (cascade.Error.Is compares Kind only).
func wrapsByIdentity(err, target error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err == target {
			return true
		}
	}
	return false
}
