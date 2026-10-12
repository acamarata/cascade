package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"testing"

	"github.com/acamarata/cascade/internal/build"
)

func TestRun(t *testing.T) {
	for _, args := range [][]string{nil, {"one", "two"}, {filepath.Join(t.TempDir(), "absent")}} {
		if err := run(args); err == nil {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("test binary has no build info")
	}
	if build.DevkeysInBuildInfo(bi) {
		if err := run([]string{exe}); err == nil {
			t.Fatal("devkeys test binary accepted")
		}
		return
	}
	if err := run([]string{exe}); err != nil {
		t.Fatal(err)
	}
	before := os.Args
	t.Cleanup(func() { os.Args = before })
	os.Args = []string{"nodevkeys", exe}
	main()
}

// TestMainRejectsExitsNonZero runs main in a child process on a non-Go
// artifact and requires exit status 1, the contract release hooks rely on.
func TestMainRejectsExitsNonZero(t *testing.T) {
	if artifact := os.Getenv("NODEVKEYS_TEST_ARTIFACT"); artifact != "" {
		os.Args = []string{"nodevkeys", artifact}
		main()
		return
	}
	artifact := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(artifact, []byte("not a Go binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainRejectsExitsNonZero$")
	cmd.Env = append(os.Environ(), "NODEVKEYS_TEST_ARTIFACT="+artifact)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("main on a non-Go artifact: %v\n%s", err, out)
	}
}
