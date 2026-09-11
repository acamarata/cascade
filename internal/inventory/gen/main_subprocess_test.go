package main

// Purpose: main()'s own os.Exit(1)-on-failure contract, which cannot be
//
//	exercised in-process (os.Exit would kill the test binary itself).
//	Re-executes the test binary as a separate OS process, the same
//	pattern internal/fleet/resume/submit_kill9_test.go and
//	providers/sqlite/lock_crossprocess_test.go already establish in this
//	repo for exactly this situation.
//
// SPORT: internal.inventory.gen/ADDED (tests).

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// mainSubprocessEnv signals TestMainHelperProcess to actually call main()
// rather than being a no-op under an ordinary `go test` run.
const mainSubprocessEnv = "CASCADE_INVENTORY_GEN_MAIN_SUBPROCESS"

// TestMainHelperProcess is re-executed as a separate OS process by
// TestMain_Success and TestMain_Failure below. Invoked as an ordinary `go
// test` run (the env var unset) it is a no-op.
func TestMainHelperProcess(_ *testing.T) {
	if os.Getenv(mainSubprocessEnv) == "" {
		return
	}
	main()
}

func TestMain_Success(t *testing.T) {
	fixture := buildGenFixtureRepo(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainHelperProcess$")
	cmd.Dir = fixture
	cmd.Env = append(os.Environ(), mainSubprocessEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("main() subprocess over a valid tree: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(filepath.Join(fixture, "internal", "inventory", "counts.json")); statErr != nil {
		t.Fatalf("main() subprocess did not write counts.json: %v", statErr)
	}
}

// TestMain_Failure proves main() exits 1 (not 0, not a panic) when run
// fails, over a working directory that is not any git checkout.
func TestMain_Failure(t *testing.T) {
	outside := t.TempDir() // deliberately never git-init'd
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainHelperProcess$")
	cmd.Dir = outside
	cmd.Env = append(os.Environ(), mainSubprocessEnv+"=1")

	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("main() subprocess over a non-repo directory: err = %v, want an *exec.ExitError", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("main() subprocess exit code = %d, want 1", exitErr.ExitCode())
	}
}
