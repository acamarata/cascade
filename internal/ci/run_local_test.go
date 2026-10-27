// Purpose: RunLocal's ordering contract -- a refusal (never-pay routing, or
// a path that is not a checkout) happens BEFORE a store is opened or a
// step is run -- plus the shared fixtures the MCP method tests reuse.
// Inputs: temp directories only; the steps are shell builtins that behave
// the same under sh and cmd.
// Constraints: no timing, no file modes, no path separators in assertions.

package ci

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// markerFile is written by the test step. Its presence proves a step ran;
// its absence is only meaningful beside a control that DOES create it.
const markerFile = "marker.txt"

// markerStep writes markerFile into the step's working directory. The same
// text is valid for sh and cmd.
const markerStep = "echo ran>" + markerFile

// localDepsFor builds CmdDeps rooted at homeDir with the given resolver.
func localDepsFor(t *testing.T, homeDir string, routes RouteResolver) CmdDeps {
	t.Helper()
	getenv := func(k string) string {
		if k == "CASCADE_HOME" {
			return homeDir
		}
		return ""
	}
	paths, err := runtime.NewPathProvider(getenv, func() (string, error) { return homeDir, nil })
	if err != nil {
		t.Fatal(err)
	}
	return CmdDeps{
		Paths: paths, Getenv: getenv, Environ: testEnviron,
		Clock:  runtime.NewFixedClock(time.Unix(1_700_000_000, 0)),
		Routes: routes,
	}
}

// writeStepsConfig writes a config.toml whose three steps are the given
// commands.
func writeStepsConfig(t *testing.T, homeDir, lint, test, build string) {
	t.Helper()
	toml := "[ci.local]\nlint = [\"" + lint + "\"]\ntest = [\"" + test + "\"]\nbuild = [\"" + build + "\"]\n"
	if err := os.WriteFile(filepath.Join(homeDir, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatalf("writing config.toml: %v", err)
	}
}

// pathExists reports whether p exists, failing the test on any error other
// than "does not exist".
func pathExists(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Stat(p)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	t.Fatalf("stat %s: %v", p, err)
	return false
}

// TestRunLocalGuardBeforeSteps: a repository the policy routes to hosted
// Actions is refused before the data directory exists (no store opened) and
// before any step runs. The control run proves the marker step and the
// database ARE produced when the policy allows the run, so the absence in
// the refused case cannot be an artefact of a step that never works.
func TestRunLocalGuardBeforeSteps(t *testing.T) {
	steps := func(home string) { writeStepsConfig(t, home, "exit 0", markerStep, "exit 0") }

	t.Run("hosted-routed repository is refused first", func(t *testing.T) {
		home := t.TempDir()
		steps(home)
		repo := newGitCheckoutWithOrigin(t, "https://github.com/example-owner/example-repo.git")
		deps := localDepsFor(t, home, allowActionsRoutes)

		_, err := RunLocal(context.Background(), deps, repo, StepFilter{})
		if err == nil {
			t.Fatal("RunLocal ran a repository the policy routes to hosted Actions")
		}
		assertKind(t, err, cascade.KindPolicyDenied)
		if pathExists(t, filepath.Join(repo, markerFile)) {
			t.Error("a step ran before the never-pay guard refused the run")
		}
		if pathExists(t, deps.Paths.DataDir()) {
			t.Error("the data directory was created: a store opened before the guard refused the run")
		}
	})

	t.Run("control: a locally routed repository runs and records", func(t *testing.T) {
		home := t.TempDir()
		steps(home)
		repo := newGitCheckoutWithOrigin(t, "https://github.com/example-owner/example-repo.git")
		deps := localDepsFor(t, home, localOnlyRoutes)

		res, err := RunLocal(context.Background(), deps, repo, StepFilter{})
		if err != nil || !res.Passed() {
			t.Fatalf("RunLocal(control) = (%+v, %v), want a passing run", res, err)
		}
		if !pathExists(t, filepath.Join(repo, markerFile)) {
			t.Error("the control run did not run the marker step")
		}
		if !pathExists(t, filepath.Join(deps.Paths.DataDir(), "cascade.db")) {
			t.Error("the control run recorded no cascade.db")
		}
	})
}

// TestRunLocalRefusesNonCheckout: a directory with no .git is refused
// before anything is created or run.
func TestRunLocalRefusesNonCheckout(t *testing.T) {
	home := t.TempDir()
	writeStepsConfig(t, home, "exit 0", markerStep, "exit 0")
	notRepo := t.TempDir()
	deps := localDepsFor(t, home, localOnlyRoutes)

	_, err := RunLocal(context.Background(), deps, notRepo, StepFilter{})
	if err == nil {
		t.Fatal("RunLocal ran in a directory that is not a repository checkout")
	}
	assertKind(t, err, cascade.KindInvalidInput)
	if pathExists(t, filepath.Join(notRepo, markerFile)) {
		t.Error("a step ran in a non-checkout")
	}
	if pathExists(t, deps.Paths.DataDir()) {
		t.Error("a store was opened for a non-checkout")
	}
}

// The tests below cover ci.run in a process that already holds the embedded
// store's write lock (the `cascade mcp serve` process does, for its recall
// index; a running daemon does too). The CLI path keeps refusing in that
// situation, exactly as before; the MCP method still runs the gate and
// records it in the results database.

// holdStoreLock takes the embedded write lock on the harness's cascade.db
// for the rest of the test, as a long-lived sibling in the same process
// would.
func holdStoreLock(t *testing.T, h methodHarness) {
	t.Helper()
	dataDir := h.deps.Paths.DataDir()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := runtime.OpenEmbeddedWriteStore(context.Background(), filepath.Join(dataDir, "cascade.db"), nil, nil)
	if err != nil {
		t.Fatalf("taking the store lock: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
}

func TestCIRunMethodRunsWhileTheStoreIsHeld(t *testing.T) {
	h := newMethodHarness(t, localOnlyRoutes, "exit 0", markerStep, "exit 0")
	holdStoreLock(t, h)

	// The CLI entry point keeps its strict behaviour: refused, nothing run.
	_, err := RunLocal(context.Background(), h.deps, h.repo, StepFilter{})
	if err == nil {
		t.Fatal("RunLocal ran while another holder owned the embedded store")
	}
	assertKind(t, err, cascade.KindConflict)
	if pathExists(t, filepath.Join(h.repo, markerFile)) {
		t.Fatal("a refused RunLocal still ran a step")
	}

	out, kind := h.call(t, MethodRun, `{}`)
	if kind != "" {
		t.Fatalf("ci.run while the store is held: kind %q", kind)
	}
	var res runMethodResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || !res.Passed || len(res.Steps) != 3 {
		t.Fatalf("ci.run = (%s, %v), want a passing three-step run", out, err)
	}
	if !pathExists(t, filepath.Join(h.repo, markerFile)) {
		t.Error("the step did not run")
	}
	status, kind := h.call(t, MethodStatus, `{"limit":5}`)
	if kind != "" || !strings.Contains(status, `"source":"local"`) {
		t.Errorf("ci.status = (%s, %q), want the run recorded as a local run", status, kind)
	}
}

// TestSharedOpenerFailsClosedOnAnythingButAConflict: the fallback applies to
// a held store lock (KindConflict) and nothing else. Any other failure of
// the strict opener refuses the run before a step runs; only a conflict
// proceeds over the results database.
func TestSharedOpenerFailsClosedOnAnythingButAConflict(t *testing.T) {
	failWith := func(kind cascade.Kind) runDepsOpener {
		return func(context.Context, string, runtime.Clock) (Deps, func(), error) {
			return Deps{}, func() {}, cascade.New(kind, "store refused")
		}
	}
	for _, kind := range []cascade.Kind{cascade.KindInternal, cascade.KindUnavailable, cascade.KindPermissionDenied} {
		h := newMethodHarness(t, localOnlyRoutes, "exit 0", markerStep, "exit 0")
		_, err := runLocal(context.Background(), h.deps, h.repo, StepFilter{}, sharedOpener(failWith(kind)))
		if err == nil {
			t.Fatalf("a %s store failure still ran the gate", kind)
		}
		assertKind(t, err, kind)
		if pathExists(t, filepath.Join(h.repo, markerFile)) {
			t.Errorf("a step ran after a %s store failure", kind)
		}
	}

	h := newMethodHarness(t, localOnlyRoutes, "exit 0", markerStep, "exit 0")
	res, err := runLocal(context.Background(), h.deps, h.repo, StepFilter{}, sharedOpener(failWith(cascade.KindConflict)))
	if err != nil || !res.Passed() {
		t.Fatalf("a held store lock (conflict) should fall back to the results database: (%+v, %v)", res, err)
	}
	if !pathExists(t, filepath.Join(h.repo, markerFile)) {
		t.Error("the conflict fallback did not run the gate")
	}
}

// TestCIRunMethodRefusesAConcurrentRun: while one ci.run holds the gate a
// second is refused before it resolves a directory, opens a store or runs a
// step; once the first finishes the next run proceeds. The gate is held
// directly, so there is no timing in the test.
func TestCIRunMethodRefusesAConcurrentRun(t *testing.T) {
	h := newMethodHarness(t, localOnlyRoutes, "exit 0", markerStep, "exit 0")
	gate := &runGate{}
	reg := rpc.NewRegistry()
	registerMCPMethods(reg, h.deps, func() (string, error) { return h.repo, nil }, gate)
	h.reg = reg

	release, ok := gate.tryAcquire()
	if !ok {
		t.Fatal("a fresh gate refused its first run")
	}
	_, kind := h.call(t, MethodRun, `{}`)
	if kind != cascade.KindConflict.String() {
		t.Errorf("a run during another run: kind = %q, want %q", kind, cascade.KindConflict)
	}
	h.assertNothingRan(t, h.repo)
	if _, kind := h.call(t, MethodStatus, `{}`); kind != "" {
		t.Errorf("ci.status must not be gated by a running ci.run: kind %q", kind)
	}

	release()
	if _, kind := h.call(t, MethodRun, `{}`); kind != "" {
		t.Errorf("a run after the gate was released: kind %q", kind)
	}
	if !pathExists(t, filepath.Join(h.repo, markerFile)) {
		t.Error("the run after release did not run its step")
	}
}
