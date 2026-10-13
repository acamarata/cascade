// Purpose: tests for `elevate-helper --enroll --presence`: flag rules, the
//
//	NO_INPUT refusal, error pass-through, the off-linux refusal, and the
//	effect-record adapter that audits an enrolment before the key file is
//	replaced (read back from the persisted store bytes).
//
// SPORT: cmd/cascade elevate-helper presence tests/ADDED (P1-SEC-25).
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// presenceDeps is newTestDeps with a counting presence enroller.
func presenceDeps(t *testing.T, env map[string]string, enroll func(context.Context) (string, error)) (elevateHelperDeps, *int) {
	t.Helper()
	deps := newTestDeps(t, &fakeCmdKeystore{}, func(k string) string { return env[k] })
	calls := new(int)
	deps.EnrollPresence = func(ctx context.Context) (string, error) { *calls++; return enroll(ctx) }
	return deps, calls
}

func TestElevateHelperPresenceEnrollPrintsFingerprint(t *testing.T) {
	deps, calls := presenceDeps(t, nil, func(ctx context.Context) (string, error) {
		if ctx == nil {
			t.Error("the enroller got a nil context")
		}
		return "abc123", nil
	})
	out, err := execElevateHelper(t, deps, "--enroll", "--presence")
	if err != nil || *calls != 1 {
		t.Fatalf("--enroll --presence: err=%v calls=%d", err, *calls)
	}
	if !strings.Contains(out, "fingerprint sha256:abc123") || !strings.Contains(out, "weaker than hardware") {
		t.Fatalf("output %q must name the fingerprint and that the tier is weaker than hardware", out)
	}
}

func TestElevateHelperPresenceFlagRules(t *testing.T) {
	for _, args := range [][]string{{"--presence"}, {"--presence", "--sign", "r", "h", "n"}, {"--enroll", "--presence", "--replace-file-tier"}} {
		deps, calls := presenceDeps(t, nil, func(context.Context) (string, error) { return "x", nil })
		_, err := execElevateHelper(t, deps, args...)
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput || *calls != 0 {
			t.Errorf("%v: kind=%v err=%v enroller calls=%d; want KindInvalidInput and 0 calls", args, kind, err, *calls)
		}
	}
}

func TestElevateHelperPresenceNoInputRefusesBeforeEnroller(t *testing.T) {
	deps, calls := presenceDeps(t, map[string]string{"CASCADE_NO_INPUT": "1"}, func(context.Context) (string, error) { return "x", nil })
	out, err := execElevateHelper(t, deps, "--enroll", "--presence")
	if err == nil || err.Error() != elevation.ErrNoInput().Error() || *calls != 0 || strings.Contains(out, "fingerprint") {
		t.Fatalf("NO_INPUT: err=%v calls=%d out=%q", err, *calls, out)
	}
}

func TestElevateHelperPresenceEnrollerRefusalPassesThrough(t *testing.T) {
	deps, _ := presenceDeps(t, nil, func(context.Context) (string, error) { return "", elevation.ErrAuthFailed(nil) })
	out, err := execElevateHelper(t, deps, "--enroll", "--presence")
	if err == nil || err.Error() != elevation.ErrAuthFailed(nil).Error() || strings.Contains(out, "fingerprint") {
		t.Fatalf("a refusal must pass through unchanged: err=%v out=%q", err, out)
	}
}

func TestElevateHelperPresenceRefusedOffLinux(t *testing.T) {
	if goruntime.GOOS == "linux" {
		t.Skip("the presence tier exists on linux")
	}
	deps := newTestDeps(t, &fakeCmdKeystore{}, nil)
	deps.EnrollPresence = func(context.Context) (string, error) {
		return elevation.EnrollPresence(deps.DataDir(), deps.TrustBackend(), deps.Clock, nil)
	}
	_, err := execElevateHelper(t, deps, "--enroll", "--presence")
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("off linux: kind=%v err=%v, want KindUnsupported", kind, err)
	}
}

// presenceFingerprintForTest is a real sha256 key fingerprint: 64 hex characters.
func presenceFingerprintForTest(t *testing.T) string {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	fp, err := elevation.Fingerprint(base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

// effectsOver wires presenceEffects over a memory store and the redacting log.
func effectsOver(t *testing.T, paths runtime.PathProvider) (presenceEffects, func() (string, int)) {
	t.Helper()
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(auditTestEpoch)
	open := func(context.Context) (audit.EffectWriter, func(), error) {
		log, err := openEffectLog(store, clock, nil, paths)
		return log, func() {}, err
	}
	return presenceEffects{ctx: context.Background(), open: open}, func() (string, int) { return storedAuditRecords(t, store) }
}

func TestElevateHelperPresenceAuditRecordsFingerprintBeforeReplace(t *testing.T) {
	fp := presenceFingerprintForTest(t)
	effects, stored := effectsOver(t, auditTestPaths(t))
	finish, err := effects.Begin(fp, "rotation")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	intent, n := stored()
	if n != 1 || !strings.Contains(intent, "elevation.presence.enroll") || !strings.Contains(intent, fp) || !strings.Contains(intent, "rotation") {
		t.Fatalf("after Begin, %d records; the intent must carry the action, kind and the unredacted fingerprint: %s", n, intent)
	}
	finish(nil)
	if _, n := stored(); n != 2 {
		t.Fatalf("after finish there are %d records, want the intent plus the confirmation", n)
	}
	if _, err := effects.Begin(fp, "rotation"); err == nil || !strings.Contains(err.Error(), "already begun") {
		t.Fatalf("a second Begin for the same key = %v, want ErrEffectExists", err)
	}
}

func TestElevateHelperPresenceAuditFailureRefuses(t *testing.T) {
	paths, vault := auditVaultFixture(t)
	vault.listErr = errors.New("vault unreadable")
	effects, stored := effectsOver(t, paths)
	finish, err := effects.Begin(presenceFingerprintForTest(t), "fresh")
	if err == nil || finish != nil {
		t.Fatalf("Begin with an unreadable vault = %v / finish=%v, want a refusal", err, finish != nil)
	}
	if _, n := stored(); n != 0 {
		t.Fatalf("%d records were written despite the refusal", n)
	}
}
