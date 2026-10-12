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
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
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
		envGetenv(nil), runtime.NewFixedClock(time.Unix(0, 0)), testEngineFactory(t))
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
	newEngine := testEngineFactory(t)
	if _, err := WaitDepsFromEnv(newDoer, nil, clock, newEngine); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil getenv: err = %v, want KindInvalidInput", err)
	}
	if _, err := WaitDepsFromEnv(newDoer, env, nil, newEngine); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil clock: err = %v, want KindInvalidInput", err)
	}
	if _, err := WaitDepsFromEnv(nil, env, clock, newEngine); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil Doer constructor: err = %v, want KindInvalidInput", err)
	}
	// A constructor that hands back no Doer is refused rather than
	// building a Client that would nil-panic on its first poll.
	if _, err := WaitDepsFromEnv(func(string) Doer { return nil }, env, clock, newEngine); !cascade.HasKind(err, cascade.KindInvalidInput) {
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
			runtime.NewFixedClock(time.Unix(0, 0)), testEngineFactory(t))
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

// testEngineFactory is the engine constructor these tests inject: the
// package's own testEngine, which is what the composition root's process
// firewall stands in for here.
func testEngineFactory(t *testing.T) func(*egress.Registry) (*egress.Engine, error) {
	t.Helper()
	engine := testEngine(t)
	return func(*egress.Registry) (*egress.Engine, error) { return engine, nil }
}

func TestWaitDepsFromEnv_RefusesWithoutAnEngineConstructor(t *testing.T) {
	_, err := WaitDepsFromEnv((&recordingTokenDoer{doer: &fakeDoer{}}).new,
		envGetenv(map[string]string{"GITHUB_TOKEN": "t"}), runtime.NewFixedClock(time.Unix(0, 0)), nil)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("nil engine constructor: err = %v, want KindUnavailable", err)
	}
}

func TestWaitDepsFromEnv_PropagatesTheEngineFailure(t *testing.T) {
	setProcessFactoryForTest(t, nil)
	_, err := WaitDepsFromEnv((&recordingTokenDoer{doer: &fakeDoer{}}).new,
		envGetenv(map[string]string{"GITHUB_TOKEN": "t"}), runtime.NewFixedClock(time.Unix(0, 0)), egress.ProcessEngine)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("no process firewall: err = %v, want KindUnavailable", err)
	}
}

// setProcessFactoryForTest installs f as the process firewall factory
// for one test and detaches it afterwards (nothing in this package's test
// binary installs one otherwise).
func setProcessFactoryForTest(t *testing.T, f func(*egress.Registry) (*egress.Engine, error)) {
	t.Helper()
	egress.SetProcessEngineFactory(f)
	t.Cleanup(func() { egress.SetProcessEngineFactory(nil) })
}

// waitCanaryName and waitCanaryValue are the stored secret the redaction
// test plants. The value is at least eight bytes and has no credential
// shape, so only the exact-value pass can catch it.
const (
	waitCanaryName  = "WAIT_CANARY"
	waitCanaryValue = "plain-canary-9f"
)

// TestWaitMergeEgressRedactsLowEntropyVaultValue: the engine wait-on-green
// polls through is the process firewall's, so a stored value with no
// credential shape leaves only as its vault reference. The empty vault this
// path used to bind would let it through verbatim.
func TestWaitMergeEgressRedactsLowEntropyVaultValue(t *testing.T) {
	ctx := context.Background()
	custody, err := secrets.SelectCustody(secrets.Config{
		Service: "cascade-ci-canary-test", Dir: t.TempDir(), ForceFileVault: true, Passphrase: "ci-canary-pass",
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("test: no platform keychain helper")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Set(ctx, waitCanaryName, []byte(waitCanaryValue), secrets.SetUpdate); err != nil {
		t.Fatal(err)
	}
	vault, err := secrets.NewEgressVault(broker)
	if err != nil {
		t.Fatal(err)
	}
	detector, err := secrets.ProcessDetector()
	if err != nil {
		t.Fatal(err)
	}
	setProcessFactoryForTest(t, func(r *egress.Registry) (*egress.Engine, error) {
		return egress.NewEngine(r, vault, detector)
	})
	deps, err := WaitDepsFromEnv((&recordingTokenDoer{doer: &fakeDoer{}}).new,
		envGetenv(map[string]string{"GITHUB_TOKEN": "t"}), runtime.NewFixedClock(time.Unix(0, 0)), egress.ProcessEngine)
	if err != nil {
		t.Fatalf("WaitDepsFromEnv: %v", err)
	}
	payload := []byte(`{"ref":"main","note":"` + waitCanaryValue + `"}`)
	out, err := deps.Client.engine.InterceptClass(ctx, egress.EgressClassCIPoll, egress.TierInternal, payload)
	if err != nil {
		t.Fatalf("InterceptClass: %v", err)
	}
	if bytes.Contains(out, []byte(waitCanaryValue)) {
		t.Fatalf("the stored value crossed the ci-poll class verbatim: %s", out)
	}
	if !bytes.Contains(out, []byte(waitCanaryName)) {
		t.Fatalf("the stored value was not replaced by its vault reference: %s", out)
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
