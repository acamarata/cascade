//go:build !windows

// Purpose: the CLI `review --pr` fetcher's mode choice (P1-PLG-14,
// acceptance [0]-[2]): daemon when one answers, in-process only when the
// socket cannot be reached at all, and a daemon refusal returned unchanged.
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

const fakePRDiff = "diff --git a/README b/README\n+fake line\n"

// prDiffFunc adapts a function to reviewplugin.PRDiffFetcher.
type prDiffFunc func(ctx context.Context, owner, repo string, number int) ([]byte, error)

func (f prDiffFunc) FetchPRDiff(ctx context.Context, owner, repo string, number int) ([]byte, error) {
	return f(ctx, owner, repo, number)
}

// countingPRDiff returns a fetcher answering diff and a pointer to its calls.
func countingPRDiff(diff []byte, err error) (prDiffFunc, *[]reviewPRDiffParams) {
	var calls []reviewPRDiffParams
	return func(_ context.Context, owner, repo string, number int) ([]byte, error) {
		calls = append(calls, reviewPRDiffParams{Owner: owner, Repo: repo, Number: number})
		return diff, err
	}, &calls
}

// isolatePRDiffTest points every home at t.TempDir(), drops any principal
// token, and counts launch-environment (store) opens in this process.
func isolatePRDiffTest(t *testing.T) *int {
	t.Helper()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	t.Setenv(principal.EnvToken, "")
	_ = os.Unsetenv(principal.EnvToken)
	opens := 0
	restore := plugins.SetLaunchEnvSource(func(context.Context) (plugins.LaunchEnv, error) {
		opens++
		return plugins.LaunchEnv{}, cascade.New(cascade.KindConflict, "test: the CLI opened the runtime store")
	})
	t.Cleanup(restore)
	return &opens
}

// socketDial returns a dial seam over sock that counts its uses.
func socketDial(sock string, dials *int) func() (gateCall, error) {
	return func() (gateCall, error) {
		*dials++
		return client.New(sock, client.DialFunc(client.UnixDialer), 10*time.Second).Do, nil
	}
}

// servePRDiffDaemon serves the real verb over fetch on an owner socket.
func servePRDiffDaemon(t *testing.T, fetch prDiffFunc) string {
	t.Helper()
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(nil)
	reg.Register(reviewPRDiffMethod, reviewPRDiffHandler(fetch, testkit.NewFrozenClock(time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC))))
	return serveForTest(t, rpc.NewHandler(reg), rpc.ConnContext)
}

func TestReviewPRDiffFetch_DaemonRouteWhenDaemonUp(t *testing.T) {
	opens := isolatePRDiffTest(t)
	daemonSide, sent := countingPRDiff([]byte(fakePRDiff), nil)
	sock := servePRDiffDaemon(t, daemonSide)
	local, localCalls := countingPRDiff(nil, errors.New("test: the in-process fetcher ran"))
	dials := 0
	f := &daemonOrLocalPRDiffFetcher{dial: socketDial(sock, &dials), local: local}
	for i := range 2 {
		diff, err := f.FetchPRDiff(t.Context(), "octocat", "Hello-World", 1)
		if err != nil || string(diff) != fakePRDiff {
			t.Fatalf("call %d: diff %q err %v, want the daemon's diff", i, diff, err)
		}
	}
	want := reviewPRDiffParams{Owner: "octocat", Repo: "Hello-World", Number: 1}
	if len(*sent) != 2 || (*sent)[0] != want || (*sent)[1] != want {
		t.Fatalf("daemon received %+v, want two %+v", *sent, want)
	}
	if *opens != 0 || len(*localCalls) != 0 {
		t.Fatalf("CLI store opens %d, in-process fetches %d; want 0 and 0 with a daemon answering", *opens, len(*localCalls))
	}
	if dials != 1 || f.mode != gateModeDaemon {
		t.Fatalf("dials %d mode %d, want one dial and the daemon mode kept", dials, f.mode)
	}
}

func TestReviewPRDiffFetch_DaemonlessUsesLocalFetcher(t *testing.T) {
	isolatePRDiffTest(t)
	absent := filepath.Join(shortTempDir(t), "absent.sock")
	local, localCalls := countingPRDiff([]byte(fakePRDiff), nil)
	dials := 0
	f := &daemonOrLocalPRDiffFetcher{dial: socketDial(absent, &dials), local: local}
	for i := range 2 {
		diff, err := f.FetchPRDiff(t.Context(), "octocat", "Hello-World", 1)
		if err != nil || string(diff) != fakePRDiff {
			t.Fatalf("call %d: diff %q err %v, want the in-process diff", i, diff, err)
		}
		if len(*localCalls) != i+1 {
			t.Fatalf("call %d: in-process fetches %d, want %d", i, len(*localCalls), i+1)
		}
	}
	if dials != 1 || f.mode != gateModeDirect {
		t.Fatalf("dials %d mode %d, want one dial and the direct mode kept", dials, f.mode)
	}
}

func TestReviewPRDiffFetch_DaemonErrorRefusesNoFallback(t *testing.T) {
	opens := isolatePRDiffTest(t)
	refusals := []error{
		cascade.New(cascade.KindPolicyDenied, "test: the daemon refused"),
		cascade.New(cascade.KindPermissionDenied, "test: caller not permitted"),
		cascade.New(cascade.KindUnavailable, "test: the daemon could not launch the plugin"),
	}
	for _, refusal := range refusals {
		local, localCalls := countingPRDiff([]byte(fakePRDiff), nil)
		dials := 0
		f := &daemonOrLocalPRDiffFetcher{local: local, dial: func() (gateCall, error) {
			dials++
			return func(context.Context, string, any, any) error { return refusal }, nil
		}}
		for i := range 2 {
			if _, err := f.FetchPRDiff(t.Context(), "octocat", "Hello-World", 1); err != refusal { //nolint:errorlint // unchanged means the same value
				t.Fatalf("%v call %d: err %v, want the daemon refusal unchanged", refusal, i, err)
			}
		}
		if len(*localCalls) != 0 || *opens != 0 || f.mode != gateModeUnset || dials != 2 {
			t.Fatalf("%v: in-process %d opens %d mode %d dials %d; want no fallback and the mode unset",
				refusal, len(*localCalls), *opens, f.mode, dials)
		}
	}
	// The same through a real daemon whose verb refuses: the kind survives
	// the wire and nothing falls back.
	daemonSide, _ := countingPRDiff(nil, cascade.New(cascade.KindUnavailable, "test: no cascade-github"))
	sock := servePRDiffDaemon(t, daemonSide)
	local, localCalls := countingPRDiff([]byte(fakePRDiff), nil)
	dials := 0
	f := &daemonOrLocalPRDiffFetcher{dial: socketDial(sock, &dials), local: local}
	_, err := f.FetchPRDiff(t.Context(), "octocat", "Hello-World", 1)
	if k, _ := cascade.KindOf(err); k != cascade.KindUnavailable || len(*localCalls) != 0 || f.mode != gateModeUnset {
		t.Fatalf("daemon refusal: err %v in-process %d mode %d; want unavailable, no fallback", err, len(*localCalls), f.mode)
	}
}

// TestReviewPRDiffFetchValidatesBeforeDial: a reference outside the grammar
// is refused before the daemon is tried or the store is opened.
func TestReviewPRDiffFetchValidatesBeforeDial(t *testing.T) {
	opens := isolatePRDiffTest(t)
	local, localCalls := countingPRDiff([]byte(fakePRDiff), nil)
	dials := 0
	f := &daemonOrLocalPRDiffFetcher{local: local, dial: func() (gateCall, error) { dials++; return nil, errors.New("test: dialed") }}
	_, err := f.FetchPRDiff(t.Context(), "octo/cat", "Hello-World", 1)
	if k, _ := cascade.KindOf(err); k != cascade.KindInvalidInput || dials != 0 || *opens != 0 || len(*localCalls) != 0 {
		t.Fatalf("bad owner: err %v dials %d opens %d local %d; want invalid-input and nothing tried", err, dials, *opens, len(*localCalls))
	}
}
