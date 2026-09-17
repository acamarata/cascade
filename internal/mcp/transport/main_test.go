package transport_test

// Purpose: TestMain for the transport test binary, which points HOME at a
//   throwaway directory for every test in the package.
// Constraints: this is not a convenience. Both transports build the real
//   response marshaler, which opens THIS PROCESS's vault at the data
//   directory resolved from $HOME. On a host with an OS keychain -- the
//   darwin dev machine -- custody takes the keychain branch and creates
//   nothing, so these tests looked clean locally. On a linux host there is
//   no keychain, custody falls back to the encrypted file vault, and that
//   CREATES $HOME/.cascade/data. That is an Art.7.1 violation which failed
//   CI's redirected-HOME job on every run while being invisible to every
//   command run on the dev machine; it was found by running the suite in a
//   linux container with a FILE planted at $HOME/.cascade, so the mkdir
//   failed and named the tests doing it.
//   TestMain rather than a per-test t.Setenv because the leak belongs to
//   the transports themselves, so every test in this package that serves a
//   request has it, including ones not yet written.
// SPORT: internal/mcp/transport test-support (ADD).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sweepStaleTransportHomes removes cascade-transport-home* dirs left behind
// by previous runs of this test binary that were killed before reaching the
// os.RemoveAll below (test timeout, interrupted CI job, unwound panic) --
// see the matching helper and comment in cmd/cascade/main_test.go, which
// this package's TestMain mirrors.
func sweepStaleTransportHomes() {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "cascade-transport-home") {
			continue
		}
		_ = os.RemoveAll(filepath.Join(os.TempDir(), e.Name()))
	}
}

func TestMain(m *testing.M) {
	sweepStaleTransportHomes()

	home, err := os.MkdirTemp("", "cascade-transport-home")
	if err != nil {
		panic("transport tests: could not create a throwaway HOME: " + err.Error())
	}

	// Pin GOPATH/GOMODCACHE to their real, persistent values before
	// redirecting HOME -- see cmd/cascade/main_test.go for why: without
	// this, any `go` subprocess spawned during the suite falls back to
	// Go's HOME-relative default and re-downloads the whole module cache
	// into this throwaway dir.
	goCacheEnv := map[string]string{"GOPATH": "", "GOMODCACHE": ""}
	for key := range goCacheEnv {
		if v := os.Getenv(key); v != "" {
			goCacheEnv[key] = v
			continue
		}
		out, err := exec.Command("go", "env", key).Output()
		if err == nil {
			goCacheEnv[key] = strings.TrimSpace(string(out))
		}
	}

	if err := os.Setenv("HOME", home); err != nil {
		panic("transport tests: could not redirect HOME: " + err.Error())
	}
	for key, val := range goCacheEnv {
		if val == "" {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			panic("transport tests: could not pin " + key + ": " + err.Error())
		}
	}

	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
