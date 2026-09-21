// Purpose: waitmerge_merge.go tests, over the REAL policy.Engine (a
// sqlite-backed StoreGrants + MemoryRegistry + Controller + Evaluate),
// never a re-derived stand-in for the classify/deny-list/grant decision
// (Art.2). MergeCaller is faked -- that seam is the plugin-host RPC
// transport itself, proven separately by
// internal/plugins/ci_waitmerge_wiring_test.go's *process.Handle
// satisfaction assertion. The L3, data-class, audit-order and moved-head
// gates have their own file (waitmerge_merge_gates_test.go).
//
// SPORT: internal.ci.MergeOnGreen/TESTED (P1-E25-W5-S51-T3).
package ci

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// fakeMergeCaller is MergeCaller's test double: the plugin-host transport
// itself is proven real elsewhere (see this file's header comment).
type fakeMergeCaller struct {
	calls []struct {
		method string
		params []byte
	}
	resp []byte
	err  error
	// onCall runs INSIDE Call, before it returns. It is what lets a test
	// observe the audit log at the moment of dispatch rather than only
	// after it, which is the only way to prove the decision was recorded
	// BEFORE the irreversible side effect (D6).
	onCall func()
}

func (f *fakeMergeCaller) Call(_ context.Context, method string, params []byte) ([]byte, error) {
	f.calls = append(f.calls, struct {
		method string
		params []byte
	}{method, params})
	if f.onCall != nil {
		f.onCall()
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// mergeFixture builds a REAL policy.Engine over a temp-file sqlite store
// (Art.2/Art.7.1: never an in-memory double), plus a real audit.Log over
// the same store.
type mergeFixture struct {
	reg    *policy.MemoryRegistry
	grants *policy.StoreGrants
	engine *policy.Engine
	audit  *audit.Log
	caller *fakeMergeCaller
	// headSHA is what the injected HeadSHAFetcher reports. It defaults to
	// the SHA passedWait resolves for, so the TOCTOU guard passes unless a
	// test moves it.
	headSHA    string
	headSHAErr error
}

// newMergeFixture registers both GitHub merge capabilities at the class the
// production composition root registers them at (cmd/cascade's
// githubCICapabilities), so this fixture cannot be more permissive than the
// shipped registry.
func newMergeFixture(t *testing.T) *mergeFixture {
	return newMergeFixtureAtClass(t, policy.ClassExternalSideEffect)
}

func newMergeFixtureAtClass(t *testing.T, class policy.ActionClass) *mergeFixture {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("opening the real sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	clock := runtime.NewFixedClock(time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC))
	reg := policy.NewMemoryRegistry()
	for _, c := range []policy.Capability{
		{Name: MergeCapability, Desc: "merge a pull request on GitHub", DefaultPolicy: class},
		{Name: MergeOnGreenCapability, Desc: "merge a PR unattended once its checks are green", DefaultPolicy: class},
	} {
		if err := reg.Add(context.Background(), c); err != nil {
			t.Fatalf("registering %s: %v", c.Name, err)
		}
	}
	grants, err := policy.NewStoreGrants(db, reg, clock)
	if err != nil {
		t.Fatalf("NewStoreGrants: %v", err)
	}
	engine, err := policy.NewEngine(reg, grants, policy.NewController(nil))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return &mergeFixture{
		reg: reg, grants: grants, engine: engine, audit: audit.New(db, clock, nil),
		caller: &fakeMergeCaller{}, headSHA: "deadbeef",
	}
}

// deps renders the fixture as MergeDeps, with a HeadSHAFetcher reporting
// whatever the fixture's headSHA/headSHAErr currently say.
func (f *mergeFixture) deps() MergeDeps {
	return MergeDeps{
		Engine: f.engine, Caller: f.caller, Audit: f.audit,
		HeadSHA: func(context.Context, string, string, string) (string, error) {
			return f.headSHA, f.headSHAErr
		},
	}
}

// grant writes a real grant for capability through the fixture's own real
// StoreGrants.
func (f *mergeFixture) grant(t *testing.T, subject policy.Subject, capability string) {
	t.Helper()
	if err := f.grants.Grant(context.Background(), policy.Grant{
		Subject: subject, Capability: capability, ScopeClass: corpus.VisibilityTeam,
	}); err != nil {
		t.Fatalf("Grant(%s): %v", capability, err)
	}
}

func testSubjectForMerge() policy.Subject {
	return policy.Subject{Kind: policy.SubjectAgent, ID: "lane-a"}
}

func passedWait(owner, repo, ref string) WaitResult {
	return WaitResult{Owner: owner, Repo: repo, Ref: ref, RunID: 501, HeadSHA: "deadbeef", Passed: true}
}

func baseMergeOptions() MergeOptions {
	return MergeOptions{Owner: "acamarata", Repo: "cascade", PR: 42, Ref: "deadbeef", MergeMethod: "squash", Subject: testSubjectForMerge()}
}

// grantedFixture is newMergeFixture plus a written merge-on-green grant.
func grantedFixture(t *testing.T) (*mergeFixture, MergeOptions) {
	t.Helper()
	f := newMergeFixture(t)
	subject := testSubjectForMerge()
	f.grant(t, subject, MergeOnGreenCapability)
	opts := baseMergeOptions()
	opts.Subject = subject
	return f, opts
}

// TestMergeOnGreen_RefusesWithoutGrant proves acceptance criterion 4:
// absent an explicit merge-on-green grant, the merge is refused with a
// logged policy-deny audit event, and the plugin host is NEVER called.
// MUTATION TARGET: deleting authorize's `outcome.Verdict != VerdictAllow`
// check makes this test call fakeMergeCaller.Call and turn red.
func TestMergeOnGreen_RefusesWithoutGrant(t *testing.T) {
	f := newMergeFixture(t)
	_, err := MergeOnGreen(context.Background(), f.deps(), baseMergeOptions(), passedWait("acamarata", "cascade", "deadbeef"))
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("err = %v, want KindPolicyDenied", err)
	}
	if len(f.caller.calls) != 0 {
		t.Fatalf("caller.calls = %d, want 0 -- the plugin host must never be reached without a grant", len(f.caller.calls))
	}
	assertPolicyDenyAudited(t, f)
}

// TestMergeOnGreen_SucceedsWithGrant proves the positive path.
func TestMergeOnGreen_SucceedsWithGrant(t *testing.T) {
	f, opts := grantedFixture(t)
	f.caller.resp = []byte(`{"sha":"cafef00d","merged":true,"message":""}`)

	result, err := MergeOnGreen(context.Background(), f.deps(), opts, passedWait(opts.Owner, opts.Repo, opts.Ref))
	if err != nil {
		t.Fatalf("MergeOnGreen: %v", err)
	}
	if !result.Merged || result.SHA != "cafef00d" {
		t.Fatalf("result = %+v, want Merged=true SHA=cafef00d", result)
	}
	if len(f.caller.calls) != 1 || f.caller.calls[0].method != mergeToolMethod {
		t.Fatalf("caller.calls = %+v, want exactly one call to %s", f.caller.calls, mergeToolMethod)
	}
	var params mergeWireParams
	if err := json.Unmarshal(f.caller.calls[0].params, &params); err != nil {
		t.Fatalf("decoding params: %v", err)
	}
	if params.Owner != "acamarata" || params.Repo != "cascade" || params.Number != 42 || params.MergeMethod != "squash" {
		t.Fatalf("params = %+v, want the real Owner/Repo/Number/MergeMethod", params)
	}
}

// TestMergeOnGreen_RebindRefusesStaleRef proves the stale-success guard.
// MUTATION TARGET: deleting rebindCheck's ref-mismatch branch turns this
// red -- MergeOnGreen would proceed to Evaluate using a wait that resolved
// for a different revision.
func TestMergeOnGreen_RebindRefusesStaleRef(t *testing.T) {
	f := newMergeFixture(t)
	_, err := MergeOnGreen(context.Background(), f.deps(), baseMergeOptions(), passedWait("acamarata", "cascade", "some-other-sha"))
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err = %v, want KindConflict", err)
	}
	if len(f.caller.calls) != 0 {
		t.Fatalf("caller.calls = %d, want 0", len(f.caller.calls))
	}
}

// TestMergeOnGreen_RefusesUnresolvedOrHeadlessWait proves a WaitResult that
// never resolved green, and one carrying no head SHA to re-check against,
// are both refused before any evaluation.
func TestMergeOnGreen_RefusesUnresolvedOrHeadlessWait(t *testing.T) {
	f := newMergeFixture(t)
	cases := []WaitResult{
		{Owner: "acamarata", Repo: "cascade", Ref: "deadbeef"},
		{Owner: "acamarata", Repo: "cascade", Ref: "deadbeef", Passed: true},
	}
	for i, wait := range cases {
		_, err := MergeOnGreen(context.Background(), f.deps(), baseMergeOptions(), wait)
		if !cascade.HasKind(err, cascade.KindConflict) {
			t.Fatalf("case %d: err = %v, want KindConflict", i, err)
		}
	}
}

// TestMergeOnGreen_CallerErrorPropagates proves a plugin-host transport
// failure surfaces as a typed error rather than a discarded/silent one.
func TestMergeOnGreen_CallerErrorPropagates(t *testing.T) {
	f, opts := grantedFixture(t)
	f.caller.err = cascade.New(cascade.KindUnavailable, "transport down")

	_, err := MergeOnGreen(context.Background(), f.deps(), opts, passedWait(opts.Owner, opts.Repo, opts.Ref))
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
}

// TestMergeOnGreen_NotMergedRefuses proves a {"merged":false} response
// (GitHub declined the merge) is refused, never reported as success.
func TestMergeOnGreen_NotMergedRefuses(t *testing.T) {
	f, opts := grantedFixture(t)
	f.caller.resp = []byte(`{"sha":"","merged":false,"message":"the branch moved"}`)

	_, err := MergeOnGreen(context.Background(), f.deps(), opts, passedWait(opts.Owner, opts.Repo, opts.Ref))
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err = %v, want KindConflict", err)
	}
}

// TestMergeOnGreen_MalformedResponseRefuses proves an undecodable response
// body is refused (KindIntegrity), never treated as a silent success.
func TestMergeOnGreen_MalformedResponseRefuses(t *testing.T) {
	f, opts := grantedFixture(t)
	f.caller.resp = []byte(`not json`)

	_, err := MergeOnGreen(context.Background(), f.deps(), opts, passedWait(opts.Owner, opts.Repo, opts.Ref))
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("err = %v, want KindIntegrity", err)
	}
}

// assertPolicyDenyAudited proves the refusal really was logged, not just
// returned as an error, by querying the real audit.Log back.
func assertPolicyDenyAudited(t *testing.T, f *mergeFixture) {
	t.Helper()
	page, err := f.audit.Query(context.Background(), audit.Filter{Kinds: []audit.Kind{audit.KindPolicyDecide}})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Records) == 0 {
		t.Fatal("no policy.decide audit record was written for the refused merge")
	}
	last := page.Records[len(page.Records)-1]
	if last.Verdict != policy.VerdictDeny.String() {
		t.Fatalf("last audit record Verdict = %q, want %q", last.Verdict, policy.VerdictDeny.String())
	}
	if last.Action != MergeOnGreenCapability {
		t.Fatalf("last audit record Action = %q, want %q", last.Action, MergeOnGreenCapability)
	}
}
