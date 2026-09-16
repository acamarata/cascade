package tools

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the argument refusals every tool makes BEFORE any
//   egress. Call's doc comment states the order as its contract — "a
//   request that cannot be built never reaches the network, so a typo in an
//   owner name costs nothing and a malformed merge never touches a branch"
//   — and this is the file that holds it to that.
// Constraints: no net/http; the Doer seam records whether it was reached.
// SPORT: plugins/github/tools tests (ADD) — P1-E25-W5-S51-T1.

// badArgs is one refusal case: a tool, the arguments that should be
// refused, and what makes them wrong.
type badArgs struct {
	tool string
	args Args
	why  string
}

// ok is a fully-populated argument set every builder accepts, so each case
// below can vary exactly ONE field and prove that field is what was
// refused.
func ok() Args {
	return Args{
		Owner: "acamarata", Repo: "cascade", Number: 7, State: "open",
		Title: "a title", Body: "a body", Head: "feature", Base: "main",
		MergeMethod: "squash", Reviewers: []string{"contributor-1"},
	}
}

// with returns ok() after applying mutate — the one-field-wrong builder.
func with(mutate func(*Args)) Args {
	a := ok()
	mutate(&a)
	return a
}

// badArgumentCases is every refusal the tool surface owes its caller, one
// wrong field at a time. Kept separate from the assertion loop so the table
// can grow with the tool groups without the test outgrowing the 50-line cap.
func badArgumentCases() []badArgs {
	return []badArgs{
		// An empty or traversing path segment reaches the URL directly.
		{"repos.list", with(func(a *Args) { a.Owner = "" }), "an empty owner"},
		{"repos.list", with(func(a *Args) { a.Owner = "../admin" }), "a traversing owner"},
		{"repos.get", with(func(a *Args) { a.Repo = "" }), "an empty repo"},
		{"repos.get", with(func(a *Args) { a.Repo = "a/b" }), "a repo carrying a separator"},

		{"issues.list", with(func(a *Args) { a.Owner = "" }), "an empty owner"},
		{"issues.list", with(func(a *Args) { a.State = "hibernating" }), "a state GitHub does not define"},
		{"issues.get", with(func(a *Args) { a.Owner = "" }), "an empty owner"},
		{"issues.get", with(func(a *Args) { a.Number = 0 }), "no issue number"},
		{"issues.create", with(func(a *Args) { a.Owner = "" }), "an empty owner"},
		{"issues.create", with(func(a *Args) { a.Title = "   " }), "a blank title"},
		{"issues.comment", with(func(a *Args) { a.Owner = "" }), "an empty owner"},
		{"issues.comment", with(func(a *Args) { a.Number = 0 }), "no issue number"},
		{"issues.comment", with(func(a *Args) { a.Body = "  " }), "a blank comment"},
		{"issues.close", with(func(a *Args) { a.Owner = "" }), "an empty owner"},
		{"issues.close", with(func(a *Args) { a.Number = -1 }), "a negative issue number"},

		{"prs.list", with(func(a *Args) { a.Owner = "" }), "an empty owner"},
		{"prs.list", with(func(a *Args) { a.State = "hibernating" }), "a state GitHub does not define"},
		{"prs.get", with(func(a *Args) { a.Owner = "" }), "an empty owner"},
		{"prs.get", with(func(a *Args) { a.Number = 0 }), "no pull-request number"},
		{"prs.create", with(func(a *Args) { a.Owner = "" }), "an empty owner"},
		{"prs.create", with(func(a *Args) { a.Title = "" }), "no title"},
		{"prs.create", with(func(a *Args) { a.Head = "" }), "no head branch"},
		{"prs.create", with(func(a *Args) { a.Base = "" }), "no base branch"},
		{"prs.create", with(func(a *Args) { a.Base = a.Head }), "head and base being the same branch"},
		{"prs.merge", with(func(a *Args) { a.Owner = "" }), "an empty owner"},
		{"prs.merge", with(func(a *Args) { a.Number = 0 }), "no pull-request number"},
		{"prs.merge", with(func(a *Args) { a.MergeMethod = "fast-forward" }), "a merge method GitHub does not accept"},
		{"prs.merge", with(func(a *Args) { a.MergeMethod = "" }), "no merge method"},
		{"prs.review_request", with(func(a *Args) { a.Owner = "" }), "an empty owner"},
		{"prs.review_request", with(func(a *Args) { a.Number = 0 }), "no pull-request number"},
		{"prs.review_request", with(func(a *Args) { a.Reviewers = nil }), "no reviewer and no team"},

		{"repos.nonesuch", ok(), "a tool this plugin does not expose"},
	}
}

// TestEveryToolRefusesBadArgumentsWithoutEgress is the whole contract in
// one loop: every case must produce a typed refusal AND must never reach
// the transport. The second half is the part that matters — a merge or a
// create that is validated by GitHub instead of here has already been sent.
func TestEveryToolRefusesBadArgumentsWithoutEgress(t *testing.T) {
	for _, tc := range badArgumentCases() {
		doer := &recordingDoer{body: []byte(`{}`)}
		client := Client{Doer: doer}

		_, err := client.Call(context.Background(), tc.tool, tc.args)
		if err == nil {
			t.Errorf("%s accepted %s", tc.tool, tc.why)
			continue
		}
		if len(doer.calls) != 0 {
			t.Errorf("%s sent a request despite %s: %+v", tc.tool, tc.why, doer.calls[0])
		}
		kind, typed := cascade.KindOf(err)
		if !typed {
			t.Errorf("%s refused %s with an untyped error: %v", tc.tool, tc.why, err)
			continue
		}
		if kind != cascade.KindInvalidInput && kind != cascade.KindNotFound {
			t.Errorf("%s refused %s as %v, want KindInvalidInput or KindNotFound", tc.tool, tc.why, kind)
		}
	}
}

// TestTheHappyArgumentsAreActuallyAccepted is the guard that keeps the
// table above honest. Every case varies one field of ok(); if ok() itself
// were refused, all thirty-one cases would pass for the wrong reason and
// assert nothing at all.
func TestTheHappyArgumentsAreActuallyAccepted(t *testing.T) {
	for tool := range decoders {
		if _, err := BuildRequest(tool, ok()); err != nil {
			t.Errorf("%s refused the baseline arguments, so every refusal case for it is vacuous: %v", tool, err)
		}
	}
}

// TestACallWithNoTransportIsRefused proves a half-built client fails as a
// typed error rather than a nil-pointer panic mid-call.
func TestACallWithNoTransportIsRefused(t *testing.T) {
	if _, err := (Client{}).Call(context.Background(), "repos.get", ok()); err == nil {
		t.Fatal("a client with no transport performed a call")
	}
}
