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
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
)

// sweepStaleTransportHomes removes cascade-transport-home* dirs left behind
// by previous runs of this test binary that were killed before reaching the
// os.RemoveAll below (test timeout, interrupted CI job, unwound panic) --
// see the matching helper and comment in cmd/cascade/main_test.go, which
// this package's TestMain mirrors.
func sweepStaleTransportHomes() {
	sweepStaleHomes(os.TempDir(), "cascade-transport-home", time.Now(), staleHomeAge)
}

// staleHomeAge is how long a throwaway HOME must sit untouched before a
// later run treats it as abandoned; a younger one belongs to a run that is
// still going (several runs share the temp dir).
const staleHomeAge = 3 * time.Hour

// sweepStaleHomes removes the directories in dir that start with prefix and
// were last modified more than maxAge before now.
func sweepStaleHomes(dir, prefix string, now time.Time, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

// TestSweepStaleHomesSparesFreshDirs: only the old directory goes.
func TestSweepStaleHomesSparesFreshDirs(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old, fresh := filepath.Join(dir, "cascade-transport-home1"), filepath.Join(dir, "cascade-transport-home2")
	for _, d := range []string{old, fresh} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	past := now.Add(-4 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	sweepStaleHomes(dir, "cascade-transport-home", now, staleHomeAge)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("the old dir survived: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("the fresh dir was removed: %v", err)
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

	// Both transports write through the process firewall; install one over
	// a file vault inside the throwaway HOME, so no test reaches the host
	// keychain and none runs without a firewall.
	if err := installProcessFirewall(filepath.Join(home, "vault")); err != nil {
		panic("transport tests: could not install the process firewall: " + err.Error())
	}

	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// installProcessFirewall installs a process firewall over an empty file
// vault under dir and the process detector.
func installProcessFirewall(dir string) error {
	custody, err := secrets.SelectCustody(secrets.Config{
		Service: "cascade-transport-test", Dir: dir, ForceFileVault: true, Passphrase: "transport-test-pass",
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("test: no platform keychain helper")
		},
	})
	if err != nil {
		return err
	}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		return err
	}
	vault, err := secrets.NewEgressVault(broker)
	if err != nil {
		return err
	}
	egress.SetProcessEngineFactory(func(r *egress.Registry) (*egress.Engine, error) {
		detector, derr := secrets.ProcessDetector()
		if derr != nil {
			return nil, derr
		}
		return egress.NewEngine(r, vault, detector)
	})
	return nil
}
