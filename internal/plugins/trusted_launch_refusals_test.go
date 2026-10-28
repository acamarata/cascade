//go:build !windows

// Purpose: a launch is refused before its intent is begun when the recorder,
// the owner file or the store is unusable, plus trusted-record, nil-handle and
// kind-name edge cases.
// SPORT: internal/plugins trusted launch tests (P1-PLG-03).
package plugins

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// TestLaunchRefusesWithoutARecorder: with no recorder lock primitive, or a
// lock directory that vanished, a launch is refused before its intent is
// begun, so neither an audit row nor a running child exists.
func TestLaunchRefusesWithoutARecorder(t *testing.T) {
	f := newLaunchFix(t)
	exe := f.installMarker(t, "trustdemo")
	f.grant(t, "trustdemo", trustManifest)
	ctx := context.Background()
	before := len(f.allRecords(t))

	restore := SetRecorderLock(nil)
	h, err := LaunchInstalled(ctx, "trustdemo")
	restore()
	if h != nil || !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "no recorder lock is available") {
		t.Fatalf("LaunchInstalled without a lock primitive = %v, %v; want the unarmed refusal", h, err)
	}
	if err := os.RemoveAll(f.lockDir); err != nil {
		t.Fatal(err)
	}
	h, err = LaunchInstalled(ctx, "trustdemo")
	if h != nil || !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "recorder lock") {
		t.Fatalf("LaunchInstalled with a vanished lock directory = %v, %v; want the lock refusal", h, err)
	}
	if _, serr := os.Stat(exe + ".ran"); !errors.Is(serr, os.ErrNotExist) {
		t.Fatal("the executable ran although no recorder could be armed")
	}
	if after := len(f.allRecords(t)); after != before {
		t.Fatalf("refused launches wrote %d audit records, want none", after-before)
	}
}

// TestLaunchRefusesWhenTheOwnerFileCannotBeWritten: the owner file is
// written before the intent; when it cannot be, no intent is recorded.
func TestLaunchRefusesWhenTheOwnerFileCannotBeWritten(t *testing.T) {
	f := newLaunchFix(t)
	liveGitHub(t, f) // arms and holds the recorder lock for this directory
	ctx := context.Background()
	before := len(f.allRecords(t))
	if err := os.RemoveAll(f.lockDir); err != nil {
		t.Fatal(err)
	}
	h, err := LaunchInstalled(ctx, cascadeGitHubPluginID)
	if h != nil || !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "recording the launch's recorder") {
		t.Fatalf("LaunchInstalled with no owner-file directory = %v, %v; want the owner-file refusal", h, err)
	}
	if after := len(f.allRecords(t)); after != before {
		t.Fatalf("the refused launch wrote %d audit records, want none", after-before)
	}
	if n := len(launchedHandles()); n != 1 {
		t.Fatalf("%d handles tracked, want only the first launch", n)
	}
}

// TestLaunchRefusesWhenTheStoreCannotBeRead: a store that errs is not
// "not installed": the read error surfaces and nothing starts.
func TestLaunchRefusesWhenTheStoreCannotBeRead(t *testing.T) {
	f := newLaunchFix(t)
	exe := f.installMarker(t, "trustdemo")
	f.grant(t, "trustdemo", trustManifest)
	closeStore(t, f.store)
	h, err := LaunchInstalled(context.Background(), "trustdemo")
	if h != nil || err == nil {
		t.Fatalf("LaunchInstalled over a closed store = %v, %v; want a refusal", h, err)
	}
	if cascade.HasKind(err, cascade.KindNotFound) || cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("a read failure was reported as a trust verdict: %v", err)
	}
	if _, serr := os.Stat(exe + ".ran"); !errors.Is(serr, os.ErrNotExist) {
		t.Fatal("the executable ran although its record could not be read")
	}
}

// TestTrustedRecordRefusals: an unknown id is NotFound; a record that is
// disabled, untrusted, not a process record, unpinned or named otherwise is
// policy-denied and names the command that fixes it.
func TestTrustedRecordRefusals(t *testing.T) {
	f := newLaunchFix(t)
	ctx := context.Background()
	env, err := ResolveLaunchEnv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := trustedRecord(ctx, env, "ghost"); !cascade.HasKind(err, cascade.KindNotFound) ||
		!strings.Contains(err.Error(), "cascade plugin add ghost") {
		t.Fatalf("trustedRecord of an unknown id = %v, want NotFound naming the add command", err)
	}
	good := PluginMetadata{Name: "demo", Enabled: true, TrustTier: TrustTrusted, RuntimeMode: plugin.RuntimeProcess, ExecDigest: "abc"}
	if err := SaveMetadata(ctx, f.store, good); err != nil {
		t.Fatal(err)
	}
	rec, err := trustedRecord(ctx, env, "demo")
	if err != nil || rec.ExecDigest != "abc" {
		t.Fatalf("trustedRecord of a trusted record = %+v, %v", rec, err)
	}
	for name, mutate := range map[string]func(*PluginMetadata){
		"disabled":  func(m *PluginMetadata) { m.Enabled = false },
		"untrusted": func(m *PluginMetadata) { m.TrustTier = "" },
		"builtin":   func(m *PluginMetadata) { m.RuntimeMode = plugin.RuntimeBuiltin },
		"unpinned":  func(m *PluginMetadata) { m.ExecDigest = "" },
	} {
		bad := good
		mutate(&bad)
		if err := SaveMetadata(ctx, f.store, bad); err != nil {
			t.Fatal(err)
		}
		if _, err := trustedRecord(ctx, env, "demo"); !cascade.HasKind(err, cascade.KindPolicyDenied) ||
			!strings.Contains(err.Error(), "cascade plugin add demo") {
			t.Fatalf("trustedRecord of a %s record = %v, want a policy refusal naming the add command", name, err)
		}
	}
}

// TestNilAndUnlaunchedHandles: a handle that never launched refuses calls
// and closes as a no-op.
func TestNilAndUnlaunchedHandles(t *testing.T) {
	for name, h := range map[string]*TrustedHandle{"nil": nil, "zero": {}} {
		if _, err := h.Call(context.Background(), helloMethod, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("Call on a %s handle = %v, want KindInvalidInput", name, err)
		}
		if err := h.Close(context.Background()); err != nil {
			t.Fatalf("Close on a %s handle = %v, want nil", name, err)
		}
	}
}

// TestKindNameAndOutcomeUnknown: a failed record names the error's Kind
// (or "error" for a foreign error), and only a timeout or a lost transport
// leaves a call's outcome unknown, never a typed refusal.
func TestKindNameAndOutcomeUnknown(t *testing.T) {
	if got := kindName(errors.New("plain")); got != "error" {
		t.Fatalf("kindName of a foreign error = %q, want error", got)
	}
	if got, want := kindName(cascade.New(cascade.KindPolicyDenied, "x")), cascade.KindPolicyDenied.String(); got != want || got == "error" {
		t.Fatalf("kindName of a policy error = %q, want %q", got, want)
	}
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"timeout":     {cascade.New(cascade.KindTimeout, "t"), true},
		"unavailable": {cascade.New(cascade.KindUnavailable, "u"), true},
		"deadline":    {context.DeadlineExceeded, true},
		"canceled":    {context.Canceled, true},
		"policy":      {cascade.New(cascade.KindPolicyDenied, "p"), false},
		"invalid":     {cascade.New(cascade.KindInvalidInput, "i"), false},
		"plain":       {errors.New("typed reply"), false},
	} {
		if got := outcomeUnknown(tc.err); got != tc.want {
			t.Fatalf("outcomeUnknown(%s) = %v, want %v", name, got, tc.want)
		}
	}
}
