// Purpose: waitmerge_deps.go -- the PRODUCTION composition. Nothing here
// touches the network, and nothing here IMPORTS it: the concrete net/http
// adapter now lives in the composition root (cmd/cascade/github_ci_doer.go,
// where its own failure branches are covered), so this file drives
// WaitDepsFromEnv through an injected ci.NewTokenDoer and ClientHeadSHA
// through the same fakeDoer the rest of this package uses.
//
// SPORT: internal.ci.WaitDepsFromEnv/TESTED, internal.ci.ClientHeadSHA/TESTED
//
//	(P1-E25-W5-S51-T3).
package ci

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// envGetenv builds a runtime.Getenv over a fixed map, so no test reads the
// real environment.
func envGetenv(vars map[string]string) runtime.Getenv {
	return func(key string) string { return vars[key] }
}

// recordingTokenDoer is the composition root's role in these tests: it
// records the token WaitDepsFromEnv resolved and returns a Doer that never
// dials anything.
type recordingTokenDoer struct {
	token string
	doer  Doer
}

func (r *recordingTokenDoer) new(token string) Doer {
	r.token = token
	return r.doer
}

func TestWaitDepsFromEnv_RefusesWithoutAToken(t *testing.T) {
	_, err := WaitDepsFromEnv((&recordingTokenDoer{doer: &fakeDoer{}}).new,
		envGetenv(nil), runtime.NewFixedClock(time.Unix(0, 0)))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
	if err.Error() != ErrNoGitHubToken().Error() {
		t.Fatalf("err = %q, want the documented no-token refusal", err.Error())
	}
}

func TestWaitDepsFromEnv_RefusesWithoutCollaborators(t *testing.T) {
	newDoer := (&recordingTokenDoer{doer: &fakeDoer{}}).new
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	env := envGetenv(map[string]string{"GITHUB_TOKEN": "t"})
	if _, err := WaitDepsFromEnv(newDoer, nil, clock); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil getenv: err = %v, want KindInvalidInput", err)
	}
	if _, err := WaitDepsFromEnv(newDoer, env, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil clock: err = %v, want KindInvalidInput", err)
	}
	if _, err := WaitDepsFromEnv(nil, env, clock); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil Doer constructor: err = %v, want KindInvalidInput", err)
	}
	// A constructor that hands back no Doer is refused rather than
	// building a Client that would nil-panic on its first poll.
	if _, err := WaitDepsFromEnv(func(string) Doer { return nil }, env, clock); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil Doer: err = %v, want KindInvalidInput", err)
	}
}

// TestWaitDepsFromEnv_BuildsRealDepsFromEachTokenVariable proves each
// documented variable is read, in precedence order, and that the resulting
// WaitDeps passes WaitOnGreen's own validation.
func TestWaitDepsFromEnv_BuildsRealDepsFromEachTokenVariable(t *testing.T) {
	for _, name := range waitTokenEnvNames {
		injected := &fakeDoer{}
		recorder := &recordingTokenDoer{doer: injected}
		deps, err := WaitDepsFromEnv(recorder.new, envGetenv(map[string]string{name: "tok"}),
			runtime.NewFixedClock(time.Unix(0, 0)))
		if err != nil {
			t.Fatalf("%s: WaitDepsFromEnv: %v", name, err)
		}
		if err := deps.validate(); err != nil {
			t.Fatalf("%s: the built deps do not validate: %v", name, err)
		}
		if deps.Client.baseURL != "https://api.github.com" {
			t.Fatalf("%s: baseURL = %q, want the real GitHub API", name, deps.Client.baseURL)
		}
		if recorder.token != "tok" {
			t.Fatalf("%s: the Doer constructor was handed %q, want the token this variable carried", name, recorder.token)
		}
		if deps.Client.doer != Doer(injected) {
			t.Fatalf("%s: doer = %#v, want the injected one", name, deps.Client.doer)
		}
	}
}

func TestEmptyEgressVault(t *testing.T) {
	names, err := emptyEgressVault{}.List(context.Background())
	if err != nil || len(names) != 0 {
		t.Fatalf("List = %v, %v, want empty/nil", names, err)
	}
	if _, err := (emptyEgressVault{}).Get(context.Background(), "github-token"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Get err = %v, want KindNotFound", err)
	}
}

// TestClientHeadSHA_ReportsTheCurrentHead drives the production
// HeadSHAFetcher through the real Client and the injected fakeDoer.
func TestClientHeadSHA_ReportsTheCurrentHead(t *testing.T) {
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		waitRunsURL: {Status: 200, Body: waitRunsBody("completed", "success")},
	}}
	client := NewClient(doer, testEngine(t), "", 1, allowActionsRoutes)
	sha, err := ClientHeadSHA(client)(context.Background(), "acamarata", "cascade", "main")
	if err != nil {
		t.Fatalf("ClientHeadSHA: %v", err)
	}
	if sha != "deadbeef" {
		t.Fatalf("sha = %q, want deadbeef", sha)
	}
}

// TestClientHeadSHA_RefusalPaths covers the nil client, an unmatched ref
// (empty SHA, which recheckHead reads as a mismatch), a transport failure,
// and the 304 case a caller must never read as "the ref stood still".
func TestClientHeadSHA_RefusalPaths(t *testing.T) {
	if _, err := ClientHeadSHA(nil)(context.Background(), "a", "b", "c"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil client: err = %v, want KindInvalidInput", err)
	}

	matched := &fakeDoer{responses: map[string]HTTPResponse{
		waitRunsURL: {Status: 200, Body: waitRunsBody("completed", "success")},
	}}
	sha, err := ClientHeadSHA(NewClient(matched, testEngine(t), "", 1, allowActionsRoutes))(
		context.Background(), "acamarata", "cascade", "no-such-ref")
	if err != nil || sha != "" {
		t.Fatalf("unmatched ref: sha=%q err=%v, want \"\"/nil", sha, err)
	}

	failing := errDoer{err: cascade.New(cascade.KindUnavailable, "dial failed")}
	if _, err := ClientHeadSHA(NewClient(failing, testEngine(t), "", 1, allowActionsRoutes))(
		context.Background(), "acamarata", "cascade", "main"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("transport failure: err = %v, want KindUnavailable", err)
	}

	notModified := &fakeDoer{responses: map[string]HTTPResponse{waitRunsURL: {Status: 304}}}
	if _, err := ClientHeadSHA(NewClient(notModified, testEngine(t), "", 1, allowActionsRoutes))(
		context.Background(), "acamarata", "cascade", "main"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("304: err = %v, want KindUnavailable rather than a silent pass", err)
	}
}
