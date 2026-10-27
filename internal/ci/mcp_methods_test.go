// Purpose: the ci.run and ci.status MCP methods -- strict params, no repo
// parameter, the working directory is the only place the gate runs, the
// never-pay guard precedes every store and step, and no step output ever
// reaches a result.
// Inputs: temp directories only; steps are shell builtins that behave the
// same under sh and cmd.
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
	"github.com/acamarata/cascade/pkg/cascade"
)

// methodHarness is a registry with the CI methods bound, plus the
// directories a test asserts against.
type methodHarness struct {
	reg  *rpc.Registry
	deps CmdDeps
	home string
	repo string
}

// newMethodHarness binds the methods over a temp HOME whose steps are
// lint, test and build, with workDir naming repo.
func newMethodHarness(t *testing.T, routes RouteResolver, lint, test, build string) methodHarness {
	t.Helper()
	home := t.TempDir()
	writeStepsConfig(t, home, lint, test, build)
	repo := newTestCheckout(t)
	deps := localDepsFor(t, home, routes)
	reg := rpc.NewRegistry()
	RegisterMCPMethods(reg, deps, func() (string, error) { return repo, nil })
	return methodHarness{reg: reg, deps: deps, home: home, repo: repo}
}

// call dispatches a method and returns the raw result JSON and the error
// kind ("" on success).
func (h methodHarness) call(t *testing.T, method, params string) (string, string) {
	t.Helper()
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	res, eo := h.reg.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: method, Params: raw})
	if eo != nil {
		kind, ok := cascade.KindFromJSONRPCCode(eo.Code)
		if !ok {
			t.Fatalf("%s: error code %d maps to no cascade kind: %+v", method, eo.Code, eo)
		}
		return "", kind.String()
	}
	out, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("%s: result does not marshal: %v", method, err)
	}
	return string(out), ""
}

// assertNothingRan fails when a marker step ran in dir or a store was
// opened under the harness's data directory.
func (h methodHarness) assertNothingRan(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if pathExists(t, filepath.Join(d, markerFile)) {
			t.Errorf("a step ran in %s", d)
		}
	}
	if pathExists(t, h.deps.Paths.DataDir()) {
		t.Error("a store was opened: the data directory exists")
	}
}

func TestCIRunMethodRejectsRepoParam(t *testing.T) {
	h := newMethodHarness(t, localOnlyRoutes, "exit 0", markerStep, "exit 0")
	other := newTestCheckout(t)
	for _, params := range []string{
		`{"repo":"` + filepath.ToSlash(other) + `"}`,
		`{"path":"x"}`,
		`{"command":"exit 0"}`,
	} {
		_, kind := h.call(t, MethodRun, params)
		if kind != cascade.KindInvalidInput.String() {
			t.Errorf("ci.run %s: kind = %q, want %q", params, kind, cascade.KindInvalidInput)
		}
	}
	h.assertNothingRan(t, h.repo, other)
}

func TestCIRunMethodRejectsUnknownField(t *testing.T) {
	h := newMethodHarness(t, localOnlyRoutes, "exit 0", markerStep, "exit 0")
	for _, params := range []string{
		`{"lint_only":true,"bogus":1}`,
		`{"lint_only":"yes"}`,
		`{} {}`,
		`[]`,
		`7`,
	} {
		_, kind := h.call(t, MethodRun, params)
		if kind != cascade.KindInvalidInput.String() {
			t.Errorf("ci.run %s: kind = %q, want %q", params, kind, cascade.KindInvalidInput)
		}
	}
	h.assertNothingRan(t, h.repo)
}

func TestCIRunMethodRunsInWorkDir(t *testing.T) {
	h := newMethodHarness(t, localOnlyRoutes, "exit 0", markerStep, "exit 0")
	elsewhere := newTestCheckout(t)

	out, kind := h.call(t, MethodRun, `{}`)
	if kind != "" {
		t.Fatalf("ci.run: kind %q", kind)
	}
	var res runMethodResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Passed || res.FailedStep != "" || len(res.Steps) != 3 || res.RunID == 0 {
		t.Fatalf("ci.run result = %+v, want a passing three-step run with a run id", res)
	}
	if !pathExists(t, filepath.Join(h.repo, markerFile)) {
		t.Error("the step did not run in the work directory")
	}
	if pathExists(t, filepath.Join(elsewhere, markerFile)) {
		t.Error("a step ran outside the work directory")
	}

	only, kind := h.call(t, MethodRun, `{"build_only":true}`)
	if kind != "" || strings.Count(only, `"name"`) != 1 || !strings.Contains(only, `"build"`) {
		t.Errorf("ci.run build_only = (%s, %q), want exactly the build step", only, kind)
	}
}

func TestCIRunMethodReportsAFailedStepAsAResult(t *testing.T) {
	h := newMethodHarness(t, localOnlyRoutes, "exit 0", "echo CANARY_FAILING_OUTPUT&& exit 3", "exit 0")
	out, kind := h.call(t, MethodRun, `{}`)
	if kind != "" {
		t.Fatalf("a failing step must be a result, got error kind %q", kind)
	}
	if strings.Contains(out, "CANARY") {
		t.Errorf("a failing step's output reached the result: %s", out)
	}
	var res runMethodResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Passed || res.FailedStep != string(StepTest) {
		t.Fatalf("result = %+v, want passed=false failed_step=test", res)
	}
	var code int
	for _, s := range res.Steps {
		if s.Name == string(StepTest) {
			code = s.ExitCode
		}
	}
	if code != 3 {
		t.Errorf("test step exit_code = %d, want 3", code)
	}
}

func TestCIRunMethodNeverReturnsStepOutput(t *testing.T) {
	const outToken, errToken = "CANARY_STDOUT_TOKEN", "CANARY_STDERR_TOKEN"
	h := newMethodHarness(t, localOnlyRoutes, "exit 0", "echo "+outToken+"&& echo "+errToken+" 1>&2", "exit 0")

	// Control: the same steps DO capture output, so its absence below is
	// the method's doing and not a step that printed nothing.
	direct, err := RunLocal(context.Background(), h.deps, h.repo, StepFilter{TestOnly: true})
	if err != nil {
		t.Fatalf("RunLocal(control): %v", err)
	}
	captured := direct.Steps[0].Stdout + direct.Steps[0].Stderr
	if !strings.Contains(captured, outToken) || !strings.Contains(captured, errToken) {
		t.Fatalf("control: the runner captured %q, want both canaries", captured)
	}

	out, kind := h.call(t, MethodRun, `{"test_only":true}`)
	if kind != "" {
		t.Fatalf("ci.run: kind %q", kind)
	}
	if strings.Contains(out, "CANARY") {
		t.Errorf("ci.run returned step output: %s", out)
	}
	for _, key := range []string{"stdout", "stderr", "output"} {
		if strings.Contains(strings.ToLower(out), key) {
			t.Errorf("ci.run result carries a %q field: %s", key, out)
		}
	}
}

func TestCIRunMethodPolicyRefusalBeforeSteps(t *testing.T) {
	cases := []struct {
		name   string
		routes RouteResolver
		want   cascade.Kind
	}{
		{"hosted-routed repository", allowActionsRoutes, cascade.KindPolicyDenied},
		{"no policy installed", nil, cascade.KindUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newMethodHarness(t, c.routes, "exit 0", markerStep, "exit 0")
			origin := newGitCheckoutWithOrigin(t, "https://github.com/example-owner/example-repo.git")
			reg := rpc.NewRegistry()
			RegisterMCPMethods(reg, h.deps, func() (string, error) { return origin, nil })
			h.reg = reg

			_, kind := h.call(t, MethodRun, `{}`)
			if kind != c.want.String() {
				t.Errorf("kind = %q, want %q", kind, c.want)
			}
			h.assertNothingRan(t, origin)
		})
	}
}

func TestCIRunMethodRefusesANonCheckoutWorkDir(t *testing.T) {
	h := newMethodHarness(t, localOnlyRoutes, "exit 0", markerStep, "exit 0")
	notRepo := t.TempDir()
	reg := rpc.NewRegistry()
	RegisterMCPMethods(reg, h.deps, func() (string, error) { return notRepo, nil })
	h.reg = reg
	if _, kind := h.call(t, MethodRun, `{}`); kind != cascade.KindInvalidInput.String() {
		t.Errorf("kind = %q, want %q", kind, cascade.KindInvalidInput)
	}
	h.assertNothingRan(t, notRepo)
}

func TestCIStatusMethodLimitBounds(t *testing.T) {
	h := newMethodHarness(t, localOnlyRoutes, "exit 0", "exit 0", "exit 0")
	seedRuns(t, h.deps, 12)

	rows := func(params string) int {
		t.Helper()
		out, kind := h.call(t, MethodStatus, params)
		if kind != "" {
			t.Fatalf("ci.status %q: kind %q", params, kind)
		}
		var res struct {
			Runs []json.RawMessage `json:"runs"`
		}
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatal(err)
		}
		return len(res.Runs)
	}
	for params, want := range map[string]int{"": 10, `{}`: 10, `{"limit":null}`: 10, `{"limit":1}`: 1, `{"limit":12}`: 12, `{"limit":50}`: 12} {
		if got := rows(params); got != want {
			t.Errorf("ci.status %q returned %d rows, want %d", params, got, want)
		}
	}
	for _, params := range []string{`{"limit":0}`, `{"limit":-1}`, `{"limit":51}`, `{"limit":"3"}`, `{"limit":1.5}`, `{"repo":"x"}`} {
		if _, kind := h.call(t, MethodStatus, params); kind != cascade.KindInvalidInput.String() {
			t.Errorf("ci.status %s: kind = %q, want %q", params, kind, cascade.KindInvalidInput)
		}
	}
}

// seedRuns reserves n local run rows so ci.status has something to page.
func seedRuns(t *testing.T, deps CmdDeps, n int) {
	t.Helper()
	ctx := context.Background()
	db, err := openResultsDB(ctx, deps.Paths.DataDir(), deps.Clock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for i := 0; i < n; i++ {
		if _, err := ReserveLocalRun(ctx, db, 1, "seed", time.Unix(1_700_000_000+int64(i), 0)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(deps.Paths.DataDir()); err != nil {
		t.Fatal(err)
	}
}
