// Purpose: proves §D-24 daemonless elevation fails closed, and — the
//
//	requirement this file exists to satisfy — that CanAttemptDaemonlessElevation
//	cannot drift from internal/rpc's canonical elevated-verb table because
//	it is DERIVED from rpc.IsElevated, not a second transcription. Every
//	case below checks agreement between the two functions directly; a
//	future edit that reintroduces a hand-copied list here would only pass
//	this file by accident, and TestElevationClassificationMatchesRPCTable
//	specifically would catch any divergence introduced by a refactor that
//	stopped calling rpc.IsElevated.
//
// SPORT: internal/policy DaemonlessElevationGuard/ADDED (tests).
package policy

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// sampleVerbs mixes §5.14 "always" verbs, §5.14 conditional verbs (which,
// with nil params, IsElevated's fail-closed unparseableParams path
// classifies as elevated too — see daemonless_elevation.go's doc comment),
// and ordinary non-elevated verbs, so every branch of
// CanAttemptDaemonlessElevation is reached.
var sampleVerbs = []string{
	"vault.get", "vault.rotate", "backup.restore", "node.enroll",
	"uninstall.purge_data", "plugin.add", "policy.set", "sensitivity.set",
	"status.get", "config.get", "not.a.real.verb",
}

func TestDaemonlessElevationClassificationMatchesRPCTable(t *testing.T) {
	elevated, plain := 0, 0
	for _, verb := range sampleVerbs {
		wantElevated := rpc.IsElevated(verb, nil)
		if wantElevated {
			elevated++
		} else {
			plain++
		}
		if got := CanAttemptDaemonlessElevation(verb, true, true); got != wantElevated {
			t.Errorf("verb %q: rpc.IsElevated=%v but CanAttemptDaemonlessElevation(true,true)=%v; the local gate must attempt exactly the table's verbs", verb, wantElevated, got)
		}
		if CanAttemptDaemonlessElevation(verb, false, false) {
			t.Errorf("verb %q: attempt allowed with no enrolment and no authenticator", verb)
		}
	}
	// Both branches must be exercised, or the loop above proves nothing.
	if elevated == 0 || plain == 0 {
		t.Fatalf("sample covers elevated=%d plain=%d verbs; both must be non-zero", elevated, plain)
	}
}

func TestCanAttemptDaemonlessElevation_FailsClosed(t *testing.T) {
	cases := []struct {
		name                   string
		verb                   string
		helperEnrolled         bool
		authenticatorAvailable bool
		want                   bool
	}{
		{"not elevated, nothing available", "status.get", false, false, false},
		{"not elevated, both available", "status.get", true, true, false},
		{"local-prefixed verb is not the table verb", "local.vault.get", true, true, false},
		{"unknown node verb", "node.git_gate_install", true, true, false},
		{"elevated, nothing available", "vault.get", false, false, false},
		{"elevated, helper only", "vault.rotate", true, false, false},
		{"elevated, authenticator only", "backup.restore", false, true, false},
		{"elevated, both available", "node.enroll", true, true, true},
		{"cgo-free build: keystore always unavailable", "uninstall.purge_data", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CanAttemptDaemonlessElevation(tc.verb, tc.helperEnrolled, tc.authenticatorAvailable)
			if got != tc.want {
				t.Errorf("CanAttemptDaemonlessElevation(%q, %v, %v) = %v, want %v",
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
	checked := 0
	for _, verb := range sampleVerbs {
		if !rpc.IsElevated(verb, nil) {
			continue
		}
		checked++
		if CanAttemptDaemonlessElevation(verb, false, true) {
			t.Errorf("verb %q: allowed with helperEnrolled=false", verb)
		}
		if CanAttemptDaemonlessElevation(verb, true, false) {
			t.Errorf("verb %q: allowed with authenticatorAvailable=false", verb)
		}
	}
	if checked == 0 {
		t.Fatal("no sample verb is elevated; the axes were never checked")
	}
}

// TestErrElevationRequiredNamesANonElevatedVerb pins the refusal text the
// local gate's callers (and its tests) use to tell "not a table verb" from
// a missing precondition.
func TestErrElevationRequiredNamesANonElevatedVerb(t *testing.T) {
	err := ErrElevationRequired("local.vault.get", true, true)
	if !cascade.HasKind(err, cascade.KindElevationRequired) || !strings.Contains(err.Error(), "not an elevated verb") {
		t.Fatalf("ErrElevationRequired(local.vault.get) = %v", err)
	}
	if strings.Contains(ErrElevationRequired("vault.get", false, false).Error(), "not an elevated verb") {
		t.Fatal("a table verb was described as not elevated")
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
