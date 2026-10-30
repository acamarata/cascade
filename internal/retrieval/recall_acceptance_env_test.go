//go:build integration && !windows

// Purpose: the isolated machine the recall-to-resume acceptance runs on
// (P1-CPA-09, contract:recall-acceptance-harness). One env per test: its own
// HOME, cascade home, workspace, socket and daemon, all under a temp tree.
// Constraints: the child environment is built from scratch, never inherited,
// so no real HOME, keychain or credential reaches the daemon (custody is the
// QA file vault; the binary must be a devkeys build for the switch to bind).
// SPORT: internal.retrieval recall_acceptance (ADD) (P1-CPA-09).
package retrieval_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
)

// acceptEnv is one isolated machine.
type acceptEnv struct {
	root, home, work, sock string
	env                    []string
}

// liveDaemons are the daemons this package started; TestMain kills any that
// a failed test left behind.
var (
	liveMu      sync.Mutex
	liveDaemons []*exec.Cmd
)

// killDaemons is the TestMain backstop.
func killDaemons() {
	liveMu.Lock()
	defer liveMu.Unlock()
	for _, c := range liveDaemons {
		if c.Process != nil {
			_ = c.Process.Kill()
		}
	}
}

// isolatedPath is the fixed system PATH the child gets (git for project
// detection); the host PATH is never inherited.
const isolatedPath = "/usr/bin:/bin:/usr/sbin:/sbin"

// isolatedEnv is the whole child environment. CASCADE_TEST_FILE_VAULT=1 keeps
// custody in the file vault under CASCADE_HOME; nothing else is inherited.
func isolatedEnv(home, cascadeHome, sock string) []string {
	return []string{
		"HOME=" + home, "USERPROFILE=" + home,
		"CASCADE_HOME=" + cascadeHome, "CASCADE_SOCKET=" + sock,
		"CASCADE_TEST_FILE_VAULT=1", "CASCADE_NO_INPUT=1",
		"PATH=" + isolatedPath,
	}
}

// newAcceptEnv builds the tree, a plain workspace (no git, no project
// marker: a general session) and the retrieval config naming the files
// source. The socket lives in a short directory: a unix path is capped.
func newAcceptEnv(t *testing.T) *acceptEnv {
	t.Helper()
	root := t.TempDir()
	short, err := os.MkdirTemp("", "cpa09")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	e := &acceptEnv{
		root: root, home: filepath.Join(root, "home"),
		work: filepath.Join(root, "W"), sock: filepath.Join(short, "d.sock"),
	}
	for _, d := range []string{e.home, filepath.Join(e.home, ".cascade"), filepath.Join(e.work, "files")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	e.env = isolatedEnv(e.home, filepath.Join(e.home, ".cascade"), e.sock)
	cfg := "[retrieval]\nsources = [\"" + filepath.Join(e.work, "files") + "\"]\n"
	if err := os.WriteFile(filepath.Join(e.home, ".cascade", "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

// cli runs one cascade command from the workspace and returns its stdout,
// stderr and exit code (-1 when the process could not run).
func (e *acceptEnv) cli(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(acceptanceBinary, args...) //nolint:gosec // the binary is built by TestMain.
	cmd.Dir, cmd.Env = e.work, e.env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.String(), errb.String(), 0
	case errors.As(err, &ee):
		return out.String(), errb.String(), ee.ExitCode()
	}
	t.Fatalf("running cascade %v: %v", args, err)
	return "", "", -1
}

// mustCLI runs a command that must exit 0.
func (e *acceptEnv) mustCLI(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	out, errb, code := e.cli(t, stdin, args...)
	if code != 0 {
		t.Fatalf("cascade %v exited %d\nstdout: %s\nstderr: %s", args, code, out, errb)
	}
	return out
}

// startDaemon runs this env's own daemon and waits until it answers status.
// Memory records are seeded BEFORE this call: the memory projection runs at
// daemon start and then every 60 s (R-14.294), so a record written after
// start would not be searchable inside the test.
func (e *acceptEnv) startDaemon(t *testing.T) {
	t.Helper()
	cmd := exec.Command(acceptanceBinary, "daemon", "run") //nolint:gosec // built by TestMain.
	cmd.Dir, cmd.Env = e.work, e.env
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the daemon: %v", err)
	}
	liveMu.Lock()
	liveDaemons = append(liveDaemons, cmd)
	liveMu.Unlock()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	// A loaded CI host can take a long time to open the database, so wait on
	// a deadline rather than a fixed handful of polls.
	for deadline := time.Now().Add(120 * time.Second); time.Now().Before(deadline); {
		if _, _, code := e.cli(t, "", "status"); code == 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the daemon never answered `cascade status`")
}

// rpc calls one method on the live daemon socket as the owner CLI.
func (e *acceptEnv) rpc(t *testing.T, method string, params, out any) {
	t.Helper()
	c := client.New(e.sock, client.UnixDialer, 15*time.Second)
	if err := c.Do(context.Background(), method, params, out); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
}
