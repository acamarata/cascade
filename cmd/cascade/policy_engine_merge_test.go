//go:build !windows

// Purpose: the proof that a stored deny row refuses the REAL merge-on-green
//
//	request. The merge request carries no action text, so a row can only
//	refuse it through the github-ci engine's stand-in command; these tests
//	drive ci.MergeOnGreen itself rather than an Evaluate call with a
//	hand-written action, so they fail if the stand-in is dropped or the
//	engine is built without the stored rows.
//
// Not built on windows: ci.MergeOnGreen refuses there first (the tier-2 daemon
// absent refusal), so the control merge in every test could not succeed.
//
// SPORT: cmd/cascade/policy (ADD) -- constructor verification.
package main

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// mergeCaller records the merge calls that reach the plugin host.
type mergeCaller struct{ calls int }

func (c *mergeCaller) Call(context.Context, string, []byte) ([]byte, error) {
	c.calls++
	return []byte(`{"sha":"cafef00d","merged":true,"message":""}`), nil
}

// mergeRunner builds the github-ci engine over store once and returns a
// function that runs the real ci.MergeOnGreen over it, for a subject holding
// a merge-on-green grant. The engine is built at the call, the merge at each
// run, as the verb does: rows stored between the two must still apply.
func mergeRunner(t *testing.T, f policyEngineFixture, store provider.Store, caller *mergeCaller) (func() error, audit.Writer) {
	t.Helper()
	ctx := context.Background()
	engine, log, err := githubCIEngine(ctx, store, f.clock, f.paths)
	if err != nil {
		t.Fatalf("githubCIEngine: %v", err)
	}
	subject := policy.Subject{Kind: policy.SubjectAgent, ID: "lane-a"}
	registry := policy.NewMemoryRegistry()
	for _, c := range githubCICapabilities() {
		if err := registry.Add(ctx, c); err != nil {
			t.Fatalf("registry: %v", err)
		}
	}
	grants, err := policy.NewStoreGrants(f.store, registry, f.clock)
	if err != nil {
		t.Fatalf("grants: %v", err)
	}
	if err := grants.Grant(ctx, policy.Grant{
		Subject: subject, Capability: ci.MergeOnGreenCapability, ScopeClass: corpus.VisibilityTeam,
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	deps := githubCIMergeDeps(engine, log, caller,
		func(context.Context, string, string, string) (string, error) { return "deadbeef", nil })
	opts := ci.MergeOptions{
		DataClass: new(provider.SensitivityInternal), Owner: "example-org", Repo: "example-repo",
		PR: 42, Ref: "deadbeef", MergeMethod: "squash", Subject: subject,
	}
	wait := ci.WaitResult{
		Owner: "example-org", Repo: "example-repo", Ref: "deadbeef", RunID: 501, HeadSHA: "deadbeef", Passed: true,
	}
	return func() error {
		_, err := ci.MergeOnGreen(ctx, deps, opts, wait)
		return err
	}, log
}

// TestGithubCIMergeOnGreenAppliesStoredDenyRow: the control merges; a row for
// another command does not block it; a stored "gh pr merge *" row then
// refuses the SAME engine's next merge before the plugin host is called.
func TestGithubCIMergeOnGreenAppliesStoredDenyRow(t *testing.T) {
	f := newPolicyEngineFixture(t)
	caller := &mergeCaller{}
	merge, _ := mergeRunner(t, f, f.store, caller)
	if err := merge(); err != nil || caller.calls != 1 {
		t.Fatalf("control: merge = %v after %d host calls, want a merge through one call", err, caller.calls)
	}
	f.denyRow(t, "git push *")
	if err := merge(); err != nil || caller.calls != 2 {
		t.Fatalf("an unrelated deny row blocked the merge: %v after %d host calls", err, caller.calls)
	}
	f.denyRow(t, "gh pr merge *")
	assertMergeRefused(t, merge, caller, 2)
}

// TestGithubCIMergeOnGreenAppliesAnExactDenyRow: an operator who writes the
// row without a wildcard ("gh pr merge") refuses the merge too.
func TestGithubCIMergeOnGreenAppliesAnExactDenyRow(t *testing.T) {
	f := newPolicyEngineFixture(t)
	caller := &mergeCaller{}
	merge, _ := mergeRunner(t, f, f.store, caller)
	if err := merge(); err != nil || caller.calls != 1 {
		t.Fatalf("control: merge = %v after %d host calls", err, caller.calls)
	}
	f.denyRow(t, "gh pr merge")
	assertMergeRefused(t, merge, caller, 1)
}

// TestGithubCIMergeOnGreenAppliesAMethodDenyRow: a row naming the merge method
// the verb really uses refuses the merge too.
func TestGithubCIMergeOnGreenAppliesAMethodDenyRow(t *testing.T) {
	f := newPolicyEngineFixture(t)
	caller := &mergeCaller{}
	merge, _ := mergeRunner(t, f, f.store, caller)
	if err := merge(); err != nil || caller.calls != 1 {
		t.Fatalf("control: merge = %v after %d host calls", err, caller.calls)
	}
	f.denyRow(t, "gh pr merge --squash")
	assertMergeRefused(t, merge, caller, 1)
}

// assertMergeRefused runs merge and wants a policy-denied refusal with the
// plugin host still at wantCalls.
func assertMergeRefused(t *testing.T, merge func() error, caller *mergeCaller, wantCalls int) {
	t.Helper()
	err := merge()
	if err == nil {
		t.Fatal("a stored deny row did not refuse the merge-on-green request")
	}
	if caller.calls != wantCalls {
		t.Fatalf("the plugin host was called %d times, want %d; the refused merge must not reach it", caller.calls, wantCalls)
	}
	if kind, _ := cascade.KindOf(err); kind != cascade.KindPolicyDenied {
		t.Fatalf("refusal = %v (kind %v), want a policy-denied error", err, kind)
	}
}

// TestGithubCIMergeOnGreenFailsClosedWhenTheDenyReadFails: a deny-row read
// that fails after the engine was built refuses the real merge.
func TestGithubCIMergeOnGreenFailsClosedWhenTheDenyReadFails(t *testing.T) {
	f := newPolicyEngineFixture(t)
	store := &denyScanFailStore{Store: f.store}
	caller := &mergeCaller{}
	merge, _ := mergeRunner(t, f, store, caller)
	if err := merge(); err != nil || caller.calls != 1 {
		t.Fatalf("control: merge = %v after %d host calls", err, caller.calls)
	}
	store.fail.Store(true)
	if err := merge(); err == nil || caller.calls != 1 {
		t.Fatalf("merge = %v after %d host calls, want a refusal with no further call", err, caller.calls)
	}
}

// TestGithubCIMergeDecisionReachesTheSharedAuditLog: the merge bridge's deps
// carry the shared redacting log, so a merge decision is a record in it. A
// bridge that dropped the log would leave the log empty (or fail validation).
func TestGithubCIMergeDecisionReachesTheSharedAuditLog(t *testing.T) {
	f := newPolicyEngineFixture(t)
	merge, w := mergeRunner(t, f, f.store, &mergeCaller{})
	log, ok := w.(*audit.Log)
	if !ok {
		t.Fatalf("github-ci audits through %T, want the shared *audit.Log", w)
	}
	before, err := log.Query(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatalf("query before: %v", err)
	}
	if err := merge(); err != nil {
		t.Fatalf("merge: %v", err)
	}
	after, err := log.Query(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatalf("query after: %v", err)
	}
	if len(after.Records) <= len(before.Records) {
		t.Fatalf("the merge wrote no audit record: %d before, %d after", len(before.Records), len(after.Records))
	}
	seen := map[audit.Kind]bool{}
	for _, rec := range after.Records[len(before.Records):] {
		if rec.Action == ci.MergeOnGreenCapability {
			seen[rec.Kind] = true
		}
	}
	if !seen[audit.KindPolicyDecide] || !seen[audit.KindPolicyRoute] {
		t.Fatalf("the merge's records lack a decide and a route entry for %s: %+v", ci.MergeOnGreenCapability, after.Records[len(before.Records):])
	}
}
