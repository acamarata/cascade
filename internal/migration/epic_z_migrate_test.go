//go:build integration

// Purpose: exercise migration and its post-checks through the shipping binary.
// Inputs: immutable fixtures copied into a sealed temporary home.
// Outputs: checked reports, ledger rows, catalog and encrypted vault contents.
// Constraints: read-only custody preflight precedes every command sequence.
// SPORT: migration end-to-end acceptance.
package migration

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/migration/tripwire"
	"github.com/acamarata/cascade/pkg/cascade"
)

var epicBinary string

func TestMain(m *testing.M) {
	if os.Getenv("MIGRATION_GUARD_EXIT") == "1" {
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "migration-binary-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	epicBinary = filepath.Join(dir, "cascade")
	cmd := exec.Command("go", "build", "-p", "4", "-buildvcs=false", "-o", epicBinary, "./cmd/cascade")
	cmd.Dir = "../.."
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	code := 1
	if err := cmd.Run(); err == nil {
		data, readErr := os.ReadFile(epicBinary)
		if readErr != nil {
			fmt.Fprintln(os.Stderr, readErr)
		} else {
			fmt.Printf("migration binary sha256=%x\n", sha256.Sum256(data))
			code = m.Run()
		}
	} else {
		fmt.Fprintln(os.Stderr, err)
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestEpicZMigration(t *testing.T) {
	before := fixtureHashes(t)
	t.Cleanup(func() {
		if !reflect.DeepEqual(before, fixtureHashes(t)) {
			t.Error("committed fixture changed")
		}
	})
	env, source := sealedEnv(t), materializeV1Home(t, false)
	manifest := fixtureManifest(t)
	guard := func(env []string) error { return custodyPreflight(env, epicRunner(t.Context(), env)) }
	err := runEpicZ(env, guard, []func() error{
		func() error {
			out := epicCommand(t, env, 0, "migrate", "v1", "--from", source, "--dry-run")
			for domain, count := range manifest {
				if !strings.Contains(out, fmt.Sprintf("%s: %d change(s) [planned] ok", domain, count)) {
					t.Fatalf("dry-run counts: %s", out)
				}
			}
			assertEpicDryDestinations(t, env)
			assertEpicLedger(t, env, false)
			return nil
		},
		func() error {
			out := epicCommand(t, env, 0, "migrate", "v1", "--from", source, "--yes")
			assertEpicLedger(t, env, true)
			assertEpicAccounts(t, env, manifest["accounts"])
			assertEpicVault(t, env, source, manifest["vault"])
			assertEpicRecallReport(t, out)
			return nil
		},
		func() error {
			out, rc := epicCommandResult(t, env, "doctor", "--json")
			return epicDoctorReport([]byte(out), rc)
		},
		func() error {
			list := epicProjectList(t)
			epicCommand(t, env, 0, "context", "sync", "--check", "--project-list", list)
			root, err := filepath.Abs("../..")
			if err != nil {
				return err
			}
			_, err = tripwire.VerifyTripwire(root, filepath.Join(root, "internal/migration/testdata/golden-checksums.sha256"))
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEpicZMigration_RawRedactedRefuses(t *testing.T) {
	before := fixtureHashes(t)
	t.Cleanup(func() {
		if !reflect.DeepEqual(before, fixtureHashes(t)) {
			t.Error("committed fixture changed")
		}
	})
	env, source := sealedEnv(t), materializeV1Home(t, true)
	err := runEpicZ(env, func(env []string) error { return custodyPreflight(env, epicRunner(t.Context(), env)) }, []func() error{
		func() error {
			out := epicCommand(t, env, cascade.ExitCode(cascade.New(cascade.KindIntegrity, "fixture refusal")), "migrate", "v1", "--from", source, "--yes", "--json")
			if !strings.Contains(out, "REDACTED fixture sentinel") || !strings.Contains(out, "integrity") {
				t.Fatalf("wrong refusal: %s", out)
			}
			assertEpicAbsent(t, filepath.Join(epicEnv(env, "CASCADE_HOME"), "data", "vault.age"))
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func epicCommand(t *testing.T, env []string, want int, args ...string) string {
	t.Helper()
	out, rc := epicCommandResult(t, env, args...)
	if rc != want {
		t.Fatalf("exit=%d want=%d", rc, want)
	}
	return out
}

func epicCommandResult(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), epicBinary, args...)
	cmd.Env = append(env, "PATH="+filepath.Dir(epicBinary)+string(os.PathListSeparator)+epicEnv(env, "PATH"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	rc := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			rc = exit.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	t.Logf("%v rc=%d\n%s%s", args, rc, out, stderr.String())
	return string(out), rc
}
