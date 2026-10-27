// Purpose: tests for the elevation_custody doctor check: the tier names, the
//
//	ADR-0005 presence residual and the audited-enrolment fingerprint rules.
//
// Constraints: custody sources and the enrolled fingerprint are injected, so
//
//	no helper binary, keychain or presence key file is touched, and nothing
//	depends on darwin or linux behaviour.
//
// SPORT: cmd/cascade doctor security mounts/ADDED (tests, P1-SEC-12).
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/runtime"
)

// fakeDoctorKeystore is a keystore that only reports a public key.
type fakeDoctorKeystore struct{}

func (fakeDoctorKeystore) GenerateKey() error { return nil }
func (fakeDoctorKeystore) PubKeyB64() (string, error) {
	return base64.StdEncoding.EncodeToString(make([]byte, 32)), nil
}
func (fakeDoctorKeystore) Sign([]byte) ([]byte, error) { return nil, os.ErrInvalid }
func (fakeDoctorKeystore) IsAvailable() bool           { return true }
func (fakeDoctorKeystore) Tier() elevation.StorageTier { return elevation.TierOSKeychain }

const presenceTier = elevation.CustodyPresence

// withElevationSource makes the check see exactly one custody source of the
// given tier; CustodyNone means no source at all.
func withElevationSource(t *testing.T, tier elevation.CustodyTier) {
	t.Helper()
	previous := doctorSelectCustody
	doctorSelectCustody = func(dir string) elevation.Custody {
		var sources []elevation.CustodySource
		if tier != elevation.CustodyNone {
			sources = []elevation.CustodySource{{Tier: tier, Name: "fake-" + string(tier), Open: func(string) (elevation.ElevationKeystore, bool) {
				if tier == elevation.CustodyFile {
					return nil, true
				}
				return fakeDoctorKeystore{}, true
			}}}
		}
		return elevation.Selector{DataDir: dir, Sources: sources}.Select()
	}
	t.Cleanup(func() { doctorSelectCustody = previous })
}

// useFakeEnrolment makes the enrolled, key-bound fingerprint fp.
func useFakeEnrolment(t *testing.T, fp string) {
	t.Helper()
	previous := doctorBoundFingerprint
	doctorBoundFingerprint = func(elevation.Custody, string, runtime.Clock) (string, error) { return fp, nil }
	t.Cleanup(func() { doctorBoundFingerprint = previous })
}

// seedPresenceEnrolment audits one presence enrolment the way elevate-helper
// does: the intent first, then the outcome ("confirmed", "failed" or none).
func seedPresenceEnrolment(t *testing.T, log *audit.Log, fp, kind, outcome string) {
	t.Helper()
	ctx := context.Background()
	effects, err := audit.NewEffectLog(log)
	if err != nil {
		t.Fatalf("effect log: %v", err)
	}
	explain, _ := json.Marshal(map[string]string{"fingerprint": fp, "kind": kind})
	h, err := effects.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindExternalEffect, Actor: presenceEnrolActor,
		Action: presenceEnrolAction, Key: presenceEnrolAction + ":" + fp, ParamsHash: audit.HashParams([]byte(fp)), Explain: explain})
	if err != nil {
		t.Fatalf("begin enrolment %s: %v", fp, err)
	}
	switch outcome {
	case "confirmed":
		_, err = effects.ConfirmEffect(ctx, h, "enrolled")
	case "failed":
		_, err = effects.FailEffect(ctx, h, "the enrolment did not complete")
	}
	if err != nil {
		t.Fatalf("%s enrolment %s: %v", outcome, fp, err)
	}
}

// presenceFixture seeds the enrolments in order and runs the check.
func presenceFixture(t *testing.T, enrolled string, enrolments [][3]string) doctor.CheckResult {
	t.Helper()
	paths := securityPaths(t)
	withElevationSource(t, presenceTier)
	useFakeEnrolment(t, enrolled)
	drv, log := seedAudit(t, paths, 1)
	for _, e := range enrolments {
		seedPresenceEnrolment(t, log, e[0], e[1], e[2])
	}
	closeStoreClean(t, drv, paths)
	before := dataDigest(t, paths.Root())
	res := runSecurityCheck(t, newElevationCustodyDoctorCheck(paths, doctorTestClock()))
	if after := dataDigest(t, paths.Root()); after != before {
		t.Fatalf("the elevation check changed the data directory:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	return res
}

func TestDoctorElevationCustodyNamesTier(t *testing.T) {
	cases := []struct {
		tier   elevation.CustodyTier
		status doctor.Status
		want   []string
	}{
		{elevation.CustodyPlatform, doctor.StatusOK, []string{"tier=platform"}},
		{elevation.CustodyPresence, doctor.StatusWarn, []string{"tier=presence", "weaker than hardware"}},
		{elevation.CustodyFile, doctor.StatusWarn, []string{"tier=file", "retired tier: elevated verbs refuse", "cascade elevate-helper --enroll --replace-file-tier"}},
		{elevation.CustodyNone, doctor.StatusWarn, []string{"tier=none", "no protected source available"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.tier), func(t *testing.T) {
			paths := securityPaths(t)
			withElevationSource(t, tc.tier)
			useFakeEnrolment(t, "unused-fake-fingerprint")
			res := runSecurityCheck(t, newElevationCustodyDoctorCheck(paths, doctorTestClock()))
			if res.Status != tc.status {
				t.Fatalf("status = %s (%q), want %s", res.Status, res.Message, tc.status)
			}
			for _, want := range tc.want {
				if !strings.Contains(res.Message, want) {
					t.Errorf("message %q lacks %q", res.Message, want)
				}
			}
		})
	}
}

func TestDoctorElevationCustodyPresenceResidual(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "elevation-presence.md"))
	if err != nil {
		t.Fatalf("read the presence doc: %v", err)
	}
	if !strings.Contains(string(doc), presenceResidualSentence) {
		t.Fatalf("docs/elevation-presence.md no longer holds the residual sentence the check prints:\n%s", presenceResidualSentence)
	}
	res := presenceFixture(t, "aaaa-fake-fingerprint", [][3]string{{"aaaa-fake-fingerprint", "fresh", "confirmed"}})
	if res.Status != doctor.StatusWarn || !strings.Contains(res.Message, presenceResidualSentence) {
		t.Fatalf("presence row: status=%s message=%q, want a warning carrying the residual sentence", res.Status, res.Message)
	}
}

func TestDoctorPresenceFingerprintChangedWarns(t *testing.T) {
	res := presenceFixture(t, "bbbb-fake-enrolled", [][3]string{{"aaaa-fake-audited", "fresh", "confirmed"}})
	for _, want := range []string{"bbbb-fake-enrolled", "aaaa-fake-audited", "differs"} {
		if res.Status != doctor.StatusWarn || !strings.Contains(res.Message, want) {
			t.Fatalf("changed fingerprint: status=%s message=%q, want a warning naming %q", res.Status, res.Message, want)
		}
	}
}

func TestDoctorPresenceFreshEnrolmentAfterEarlierWarns(t *testing.T) {
	// The enrolled fingerprint equals the last audited one: only the history gives it away.
	res := presenceFixture(t, "bbbb-fake-second", [][3]string{
		{"aaaa-fake-first", "fresh", "confirmed"}, {"bbbb-fake-second", "fresh", "confirmed"}})
	for _, want := range []string{"fresh enrolment bbbb-fake-second", "earlier enrolment aaaa-fake-first"} {
		if res.Status != doctor.StatusWarn || !strings.Contains(res.Message, want) {
			t.Fatalf("delete-and-re-enrol: status=%s message=%q, want %q", res.Status, res.Message, want)
		}
	}
}

// TestDoctorPresenceHistoryOnlyWarnsForReplacement: a rotation after the
// first enrolment, and a failed enrolment that never replaced the key, are
// not replacement signs.
func TestDoctorPresenceHistoryOnlyWarnsForReplacement(t *testing.T) {
	res := presenceFixture(t, "cccc-fake-third", [][3]string{
		{"aaaa-fake-first", "fresh", "confirmed"}, {"bbbb-fake-failed", "fresh", "failed"}, {"cccc-fake-third", "rotation", "confirmed"}})
	for _, bad := range []string{"differs", "fresh enrolment", "no audited"} {
		if strings.Contains(res.Message, bad) {
			t.Fatalf("a legitimate history raised %q: %s", bad, res.Message)
		}
	}
	if !strings.Contains(res.Message, "fingerprint=cccc-fake-third") {
		t.Fatalf("message %q does not name the enrolled fingerprint", res.Message)
	}
	if none := presenceFixture(t, "dddd-fake-unaudited", nil); !strings.Contains(none.Message, "no audited enrolment") {
		t.Fatalf("a key with no audited enrolment must warn, got %q", none.Message)
	}
}

// TestDoctorPresenceUnknownKindCountsAsFresh: an enrolment whose kind is not
// "rotation" is a fresh one, so a later unknown kind cannot hide a re-create.
func TestDoctorPresenceUnknownKindCountsAsFresh(t *testing.T) {
	res := presenceFixture(t, "bbbb-fake-second", [][3]string{{"aaaa-fake-first", "fresh", "confirmed"}, {"bbbb-fake-second", "", "confirmed"}})
	if !strings.Contains(res.Message, "fresh enrolment bbbb-fake-second") {
		t.Fatalf("an enrolment of unknown kind after an earlier one must warn as fresh, got %q", res.Message)
	}
}

// TestDoctorElevationCustodyPlatformNeedsEnrolment: a platform source with no
// enrolled key bound to it (real BoundTrust, empty trust record) is a warning
// that says elevated verbs refuse, not "hardware-backed custody" OK.
func TestDoctorElevationCustodyPlatformNeedsEnrolment(t *testing.T) {
	paths := securityPaths(t)
	withElevationSource(t, elevation.CustodyPlatform)
	res := runSecurityCheck(t, newElevationCustodyDoctorCheck(paths, doctorTestClock()))
	if res.Status != doctor.StatusWarn || !strings.Contains(res.Message, "tier=platform") || !strings.Contains(res.Message, "no enrolled key") || res.Remediation == "" {
		t.Fatalf("unenrolled platform tier: status=%s message=%q remedy=%q, want a warning naming the missing enrolment", res.Status, res.Message, res.Remediation)
	}
	useFakeEnrolment(t, "aaaa-fake-fingerprint")
	if ok := runSecurityCheck(t, newElevationCustodyDoctorCheck(paths, doctorTestClock())); ok.Status != doctor.StatusOK || !strings.Contains(ok.Message, "enrolled and bound") {
		t.Fatalf("enrolled platform tier: status=%s message=%q, want OK", ok.Status, ok.Message)
	}
}

// TestDoctorPresenceUnboundKeyWarns: a presence key whose trust record is not
// bound to it is the key-replacement signal; it must stay a warning that names
// the binding error and never reads as a healthy enrolment.
func TestDoctorPresenceUnboundKeyWarns(t *testing.T) {
	paths := securityPaths(t)
	withElevationSource(t, presenceTier)
	previous := doctorBoundFingerprint
	doctorBoundFingerprint = func(elevation.Custody, string, runtime.Clock) (string, error) {
		return "", errors.New("trust record belongs to another key")
	}
	t.Cleanup(func() { doctorBoundFingerprint = previous })
	res := runSecurityCheck(t, newElevationCustodyDoctorCheck(paths, doctorTestClock()))
	if res.Status != doctor.StatusWarn || !strings.Contains(res.Message, "not bound to the presence key") || !strings.Contains(res.Message, "another key") || strings.Contains(res.Message, "fingerprint=") {
		t.Fatalf("unbound presence key: status=%s message=%q, want a warning naming the binding error", res.Status, res.Message)
	}
}

// TestProductionDoctorSelectIsReadOnly: the production selector (TestMain
// swaps the seam, so call it directly) ranks sources and writes nothing.
// Residual: nothing proves the seam's default IS this function.
func TestProductionDoctorSelectIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	before := dataDigest(t, dir)
	_ = productionDoctorSelect(dir)
	if dataDigest(t, dir) != before {
		t.Fatal("the production custody selector changed the data directory")
	}
}
