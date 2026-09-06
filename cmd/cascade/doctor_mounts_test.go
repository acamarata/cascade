// Purpose: tests for `cascade doctor`'s composition root - that the real
//
//	productionCheckRegistry mounts each check, that the real doctor entry
//	point runs them, and that `--fix` refuses without a confirmation.
//
// Constraints: every mount test drives the REAL productionCheckRegistry
//
//	against a PathProvider rooted in t.TempDir(), so it opens a temporary
//	vault and quarantine ledger and never the operator's own (Art.7.1).
//
// SPORT: DOCTOR_SECRETS_REGISTRATION: CHANGE (tests).
package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
)

// doctorTestClock is the fixed clock every mount test uses.
func doctorTestClock() runtime.Clock {
	return runtime.NewFixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
}

// doctorTestPaths is a PathProvider rooted under t.TempDir(), so a test
// that drives the REAL productionCheckRegistry opens a temporary vault,
// quarantine ledger and config rather than the operator's own.
func doctorTestPaths(t *testing.T) runtime.PathProvider {
	t.Helper()
	root := t.TempDir()
	paths, err := runtime.NewPathProvider(func(key string) string {
		if key == "CASCADE_HOME" {
			return root
		}
		return ""
	}, func() (string, error) { return root, nil })
	if err != nil {
		t.Fatalf("building a temp path provider: %v", err)
	}
	return paths
}

// useTempCustody points the doctor's vault checks at an encrypted file
// vault under t.TempDir(). Without it the real registry would open the
// operator's own keychain.
func useTempCustody(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	previous := newDoctorCustody
	newDoctorCustody = func(string) (secrets.Custody, error) {
		return secrets.SelectCustody(secrets.Config{
			Service:    "cascade-doctor-test",
			Dir:        dir,
			Passphrase: "doctor-test-pass",
			Runner: func(context.Context, string, ...string) ([]byte, error) {
				return nil, errors.New("no platform keychain in this test")
			},
		})
	}
	t.Cleanup(func() { newDoctorCustody = previous })
}

// TestVaultServiceLabelIsPinnedToTheSecretsDomain keeps the CLI's own
// keychain service label identical to the one every other composition
// root opens the vault with. Two literals would let the CLI and the
// daemon's outbound firewall read different stores while both reported
// success.
func TestVaultServiceLabelIsPinnedToTheSecretsDomain(t *testing.T) {
	if vaultService != secrets.DefaultVaultService {
		t.Fatalf("vaultService = %q, secrets.DefaultVaultService = %q; the vault label has two homes",
			vaultService, secrets.DefaultVaultService)
	}
}

// TestProductionRegistryMountsEverySecretsCheck drives the REAL
// productionCheckRegistry and asserts each of the five named vault checks
// is present. Deleting a Register call in doctor_mounts.go turns this red.
func TestProductionRegistryMountsEverySecretsCheck(t *testing.T) {
	useTempCustody(t)
	reg, err := productionCheckRegistry(context.Background(), doctorTestPaths(t), doctorTestClock())
	if err != nil {
		t.Fatalf("productionCheckRegistry: %v", err)
	}
	mounted := map[string]bool{}
	for _, check := range reg.List() {
		mounted[check.Name()] = true
	}
	for _, want := range []string{
		"secrets/keychain-reachable",
		"secrets/keys-resolvable",
		"secrets/oauth-not-expired",
		"secrets/patterns-loaded",
		"secrets/quarantine-depth",
		"retrieval_fusion_default",
	} {
		if !mounted[want] {
			t.Fatalf("%s is not registered on the real productionCheckRegistry; mounted: %v", want, mounted)
		}
	}
}

// TestDoctorRunsTheMountedSecretsChecks drives the real `cascade doctor`
// entry point over the real registry and asserts the vault checks
// actually reported. A registry that mounts a check the runner never
// reaches would pass the test above and fail this one.
func TestDoctorRunsTheMountedSecretsChecks(t *testing.T) {
	useTempCustody(t)
	paths := doctorTestPaths(t)
	deps := doctorDeps{
		Registry: func(ctx context.Context) (*doctor.CheckRegistry, error) {
			return productionCheckRegistry(ctx, paths, doctorTestClock())
		},
		Paths:      paths,
		Getenv:     func(string) string { return "" },
		Environ:    func() []string { return nil },
		Clock:      doctorTestClock(),
		ConfirmFix: func(*cobra.Command) (bool, error) { return true, nil },
		BundleDir:  t.TempDir(),
	}
	out, _ := execRootDoctor(t, deps, "doctor")
	for _, want := range []string{"secrets/patterns-loaded", "secrets/quarantine-depth"} {
		if !strings.Contains(out, want) {
			t.Fatalf("`cascade doctor` did not report %s; output:\n%s", want, out)
		}
	}
}

// TestDoctorFixRefusesUnderNoInput pins the non-interactive refusal: --fix
// under CASCADE_NO_INPUT=1 is a hard error, and it fires before any check
// runs so nothing is mutated on the way to it.
func TestDoctorFixRefusesUnderNoInput(t *testing.T) {
	deps := testDoctorDeps(t, &fakeCheck{name: "x", status: doctor.StatusWarn, message: "warn", fixable: true})
	deps.Getenv = func(key string) string {
		if key == "CASCADE_NO_INPUT" {
			return "1"
		}
		return ""
	}
	_, err := execRootDoctor(t, deps, "doctor", "--fix")
	if err == nil || !strings.Contains(err.Error(), "CASCADE_NO_INPUT") {
		t.Fatalf("doctor --fix under CASCADE_NO_INPUT = %v, want a hard refusal", err)
	}
}

// TestDoctorFixRefusesWithoutConfirmation pins the ask: a "no" changes
// nothing and the command reports why.
func TestDoctorFixRefusesWithoutConfirmation(t *testing.T) {
	check := &fakeCheck{name: "x", status: doctor.StatusWarn, message: "warn", fixable: true}
	deps := testDoctorDeps(t, check)
	deps.ConfirmFix = func(*cobra.Command) (bool, error) { return false, nil }
	_, err := execRootDoctor(t, deps, "doctor", "--fix")
	if err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("doctor --fix without confirmation = %v, want a refusal", err)
	}
	if check.fixed {
		t.Fatal("a check was fixed after the confirmation was declined")
	}
}
