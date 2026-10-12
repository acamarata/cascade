package ci

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// wantMergeRefusal is the error MergeOnGreen owes a refused merge here: the
// platform gate's own refusal where it refuses before any check (Windows
// tier-2), else the data-class denial. The comparison is by identity and
// text, never errors.Is (a cascade.Error's Is compares Kind only).
func wantMergeRefusal(refusal error) error {
	if refusal != nil {
		return refusal
	}
	return policy.ErrDataClassDenied
}

// assertDataClassMerges runs the ceiling check under the given platform
// refusal: LocalOnly and Restricted are denied with no merge call; Internal
// and Public merge, unless the platform refuses every merge first.
func assertDataClassMerges(t *testing.T, refusal error) {
	t.Helper()
	for _, tier := range []provider.SensitivityTier{provider.SensitivityLocalOnly, provider.SensitivityRestricted, provider.SensitivityInternal, provider.SensitivityPublic} {
		f, opts := grantedFixture(t)
		opts.DataClass = new(tier)
		f.caller.resp = []byte(`{"sha":"cafef00d","merged":true,"message":""}`)
		_, err := MergeOnGreen(context.Background(), f.deps(), opts, passedWait(opts.Owner, opts.Repo, opts.Ref))
		if refusal != nil || tier == provider.SensitivityLocalOnly || tier == provider.SensitivityRestricted {
			want := wantMergeRefusal(refusal)
			if err != want || err.Error() != want.Error() || len(f.caller.calls) != 0 {
				t.Fatalf("deny = %v, calls = %d", err, len(f.caller.calls))
			}
		} else if err != nil || len(f.caller.calls) != 1 {
			t.Fatalf("pass = %v, calls = %d", err, len(f.caller.calls))
		}
	}
}

// assertUnsetDataClassMerge feeds MergeOnGreen a nil data class: it is
// refused with no merge call.
func assertUnsetDataClassMerge(t *testing.T, refusal error) {
	t.Helper()
	f, opts := grantedFixture(t)
	opts.DataClass = nil
	_, err := MergeOnGreen(context.Background(), f.deps(), opts, passedWait(opts.Owner, opts.Repo, opts.Ref))
	want := wantMergeRefusal(refusal)
	if err != want || err.Error() != want.Error() || len(f.caller.calls) != 0 {
		t.Fatalf("unset merge = %v", err)
	}
}

// platformRefusal returns the gate's refusal on this platform, asserting it is
// the Windows tier-2 refusal on windows and none elsewhere, so the tests
// below never accept an arbitrary refusal.
func platformRefusal(t *testing.T) error {
	t.Helper()
	refusal := waitPlatformRefusal()
	if (refusal != nil) != (runtime.GOOS == "windows") || (refusal != nil && !strings.Contains(refusal.Error(), "requires the daemon (Windows tier-2)")) {
		t.Fatalf("platform refusal on %s = %v", runtime.GOOS, refusal)
	}
	return refusal
}

func TestMergeOnGreen_SecretAndConfidentialDeniedUnderInternalCeiling(t *testing.T) {
	assertDataClassMerges(t, platformRefusal(t))
}

func TestUnsetDataClassFailsClosed(t *testing.T) {
	if got := (MergeOptions{}).dataClass(); got == nil || *got != provider.SensitivityLocalOnly {
		t.Fatalf("unset = %v", got)
	}
	assertUnsetDataClassMerge(t, platformRefusal(t))
	got := (MergeOptions{DataClass: new(provider.SensitivityRestricted)}).dataClass()
	if got == nil || *got != provider.SensitivityRestricted {
		t.Fatal("restricted class changed")
	}
}

// TestDataClassMergeUnderPlatformRefusal proves both legs on every host by
// swapping the platform gate: under a tier-2 style refusal every class gets
// exactly that refusal and no merge call; with no refusal (the gate open, as
// off Windows) the data-class denial itself holds, Windows included.
func TestDataClassMergeUnderPlatformRefusal(t *testing.T) {
	restore := waitPlatformRefusal
	t.Cleanup(func() { waitPlatformRefusal = restore })
	for _, refusal := range []error{cascade.New(cascade.KindUnsupported, "ci: synthetic tier-2 refusal"), nil} {
		waitPlatformRefusal = func() error { return refusal }
		assertDataClassMerges(t, refusal)
		assertUnsetDataClassMerge(t, refusal)
	}
}
