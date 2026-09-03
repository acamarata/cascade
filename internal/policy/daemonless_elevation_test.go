// Purpose: proves §D-24 daemonless elevation fails closed, and — the
//   requirement this file exists to satisfy — that IsDaemonlessElevationAllowed
//   cannot drift from internal/rpc's canonical elevated-verb table because
//   it is DERIVED from rpc.IsElevated, not a second transcription. Every
//   case below checks agreement between the two functions directly; a
//   future edit that reintroduces a hand-copied list here would only pass
//   this file by accident, and TestElevationClassificationMatchesRPCTable
//   specifically would catch any divergence introduced by a refactor that
//   stopped calling rpc.IsElevated.
// SPORT: internal/policy DaemonlessElevationGuard/ADDED (tests).
package policy

import (
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// sampleVerbs mixes §5.14 "always" verbs, §5.14 conditional verbs (which,
// with nil params, IsElevated's fail-closed unparseableParams path
// classifies as elevated too — see daemonless_elevation.go's doc comment),
// and ordinary non-elevated verbs, so every branch of
// IsDaemonlessElevationAllowed is reached.
var sampleVerbs = []string{
	"vault.get", "vault.rotate", "backup.restore", "node.enroll",
	"uninstall.purge_data", "plugin.add", "policy.set", "sensitivity.set",
	"status.get", "config.get", "not.a.real.verb",
}

func TestDaemonlessElevationClassificationMatchesRPCTable(t *testing.T) {
	for _, verb := range sampleVerbs {
		wantElevated := rpc.IsElevated(verb, nil)
		allowedWithNothing := IsDaemonlessElevationAllowed(verb, false, false)
		if wantElevated && allowedWithNothing {
			t.Errorf("verb %q: rpc.IsElevated=true but IsDaemonlessElevationAllowed(false,false)=true; a verb elevated in one table and not enforced in the other is exactly the bypass this ticket must prevent", verb)
		}
		if !wantElevated && !allowedWithNothing {
			t.Errorf("verb %q: rpc.IsElevated=false but IsDaemonlessElevationAllowed(false,false)=false; a non-elevated verb must never be refused", verb)
		}
	}
}

func TestIsDaemonlessElevationAllowed_FailsClosed(t *testing.T) {
	cases := []struct {
		name                   string
		verb                   string
		helperEnrolled         bool
		authenticatorAvailable bool
		want                   bool
	}{
		{"not elevated, nothing available", "status.get", false, false, true},
		{"elevated, nothing available", "vault.get", false, false, false},
		{"elevated, helper only", "vault.rotate", true, false, false},
		{"elevated, authenticator only", "backup.restore", false, true, false},
		{"elevated, both available", "node.enroll", true, true, true},
		{"cgo-free build: keystore always unavailable", "uninstall.purge_data", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsDaemonlessElevationAllowed(tc.verb, tc.helperEnrolled, tc.authenticatorAvailable)
			if got != tc.want {
				t.Errorf("IsDaemonlessElevationAllowed(%q, %v, %v) = %v, want %v",
					tc.verb, tc.helperEnrolled, tc.authenticatorAvailable, got, tc.want)
			}
		})
	}
}

// TestEverySampleElevatedVerbRefusesOnBothAxes is the literal §5.14 AC:
// every elevated verb tested with helperEnrolled=false AND with
// authenticatorAvailable=false, each returning refusal, no verb succeeding
// silently in either condition.
func TestEverySampleElevatedVerbRefusesOnBothAxes(t *testing.T) {
	for _, verb := range sampleVerbs {
		if !rpc.IsElevated(verb, nil) {
			continue
		}
		if IsDaemonlessElevationAllowed(verb, false, true) {
			t.Errorf("verb %q: allowed with helperEnrolled=false", verb)
		}
		if IsDaemonlessElevationAllowed(verb, true, false) {
			t.Errorf("verb %q: allowed with authenticatorAvailable=false", verb)
		}
	}
}

func TestErrElevationRequired_TypedKind(t *testing.T) {
	cases := []struct {
		name                   string
		helperEnrolled         bool
		authenticatorAvailable bool
	}{
		{"neither available", false, false},
		{"authenticator only", false, true},
		{"helper only", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ErrElevationRequired("vault.get", tc.helperEnrolled, tc.authenticatorAvailable)
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindElevationRequired {
				t.Fatalf("Kind = %v, %v; want KindElevationRequired, true", kind, ok)
			}
			if err == nil || err.Error() == "" {
				t.Fatal("ErrElevationRequired returned an empty error")
			}
		})
	}
}
