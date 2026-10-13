//go:build !windows

// Purpose: P1-AGT-02 tests for the claude driver: an incomplete
//
//	spec.Envelope refuses before any child, and spec.Model / spec.Effort
//	reach the argv (--model, --effort over DriverDefaults.Effort).
//
// SPORT: providers.agents.claude tests (ADD) - P1-AGT-02.
package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// testNow is the instant testClock reports.
var testNow = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

// testClock is the injected fixed clock every driver test uses.
type testClock struct{}

func (testClock) Now() time.Time { return testNow }

// testEnvelope returns a complete envelope whose one root is root.
func testEnvelope(root string) provider.SpawnEnvelope {
	return provider.SpawnEnvelope{Digest: strings.Repeat("0f", 32), Principal: "principal-test",
		Roots: []string{filepath.ToSlash(root)}, Sensitivity: provider.SensitivityInternal,
		ExpiresAt: testNow.Add(time.Hour)}
}

func TestClaudeRefusesIncompleteEnvelope(t *testing.T) {
	rows := map[string]func(e *provider.SpawnEnvelope, root string){
		"zero envelope":    func(e *provider.SpawnEnvelope, _ string) { *e = provider.SpawnEnvelope{} },
		"bad digest":       func(e *provider.SpawnEnvelope, _ string) { e.Digest = "xyz" },
		"empty principal":  func(e *provider.SpawnEnvelope, _ string) { e.Principal = "" },
		"no roots":         func(e *provider.SpawnEnvelope, _ string) { e.Roots = nil },
		"relative root":    func(e *provider.SpawnEnvelope, _ string) { e.Roots = []string{"rel"} },
		"expired":          func(e *provider.SpawnEnvelope, _ string) { e.ExpiresAt = testNow },
		"zero expiry":      func(e *provider.SpawnEnvelope, _ string) { e.ExpiresAt = time.Time{} },
		"worktree outside": func(e *provider.SpawnEnvelope, _ string) { e.Roots = []string{filepath.ToSlash(newGitWorktree(t))} },
		// The root is the worktree minus its last character: a string prefix, not a path prefix.
		"sibling prefix": func(e *provider.SpawnEnvelope, root string) { e.Roots = []string{filepath.ToSlash(root[:len(root)-1])} },
	}
	for name, edit := range rows {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			spec := f.spec()
			edit(&spec.Envelope, f.root)
			if err := refusedBeforeAnyChild(t, f, f.config(), spec); !wraps(err, provider.ErrEnvelopeIncomplete) {
				t.Fatalf("Spawn = %v, want an error wrapping provider.ErrEnvelopeIncomplete by identity", err)
			}
		})
	}
	f := newFx(t)
	if _, err := f.driver(f.config()).Spawn(context.Background(), f.spec()); err != nil {
		t.Fatalf("a complete envelope was refused: %v", err)
	}
}

// A driver built without a Clock is probe-only: it exists, and Spawn refuses
// before any child because the envelope expiry cannot be checked.
func TestClaudeNilClockRefusesSpawn(t *testing.T) {
	f := newFx(t)
	cfg := f.config()
	cfg.Clock = nil
	d, err := New(cfg)
	if err != nil {
		t.Fatalf("New with a nil Clock = %v, want a probe-only driver", err)
	}
	if _, err := d.Capabilities(context.Background(), ""); err != nil {
		t.Fatalf("Capabilities on a probe-only driver: %v", err)
	}
	if err := refusedBeforeAnyChild(t, f, cfg, f.spec()); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Spawn with a nil Clock = %v, want KindInvalidInput", err)
	}
}

func TestClaudeRefusesLeaseLinkedOutOfEnvelope(t *testing.T) {
	outside, envRoot := newGitWorktree(t), t.TempDir()
	link := filepath.Join(envRoot, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := leaseWithinRoots(link, []string{filepath.ToSlash(envRoot)}); !wraps(err, provider.ErrEnvelopeIncomplete) {
		t.Fatalf("a lease linked out of the envelope root = %v, want ErrEnvelopeIncomplete", err)
	}
	inside := filepath.Join(envRoot, "tree")
	mustMkdir(t, inside)
	for name, c := range map[string]struct {
		lease string
		roots []string
		ok    bool
	}{
		"real dir under the root": {inside, []string{filepath.ToSlash(envRoot)}, true},
		"the root itself":         {envRoot, []string{filepath.ToSlash(envRoot)}, true},
		"second root covers":      {inside, []string{filepath.ToSlash(outside), filepath.ToSlash(envRoot)}, true},
		"root does not exist":     {inside, []string{filepath.ToSlash(envRoot) + "/missing"}, false},
		"no roots":                {inside, nil, false},
		"lease does not exist":    {filepath.Join(envRoot, "gone"), []string{filepath.ToSlash(envRoot)}, false},
	} {
		if got := leaseWithinRoots(c.lease, c.roots) == nil; got != c.ok {
			t.Errorf("%s: leaseWithinRoots passed = %v, want %v", name, got, c.ok)
		}
	}
	// End to end: the same link through Spawn runs no child.
	f := newFx(t)
	spec := f.spec()
	spec.WorktreeRoot, spec.Envelope.Roots = link, []string{filepath.ToSlash(envRoot)}
	cfg := f.config()
	cfg.Worktree = fakeLeaser{lease: Lease{Root: link, GitDir: filepath.Join(link, ".git")}}
	if err := refusedBeforeAnyChild(t, f, cfg, spec); !wraps(err, provider.ErrEnvelopeIncomplete) {
		t.Fatalf("Spawn on a linked lease = %v, want ErrEnvelopeIncomplete", err)
	}
}

func TestClaudeModelEffortFromSpec(t *testing.T) {
	jobID, want := modelEffortGolden(t, "/r", "/h/home", "do the thing")
	in := baseInput(provider.ToolPolicyNone)
	in.jobID, in.model, in.effort = jobID, "sonnet", "max"
	in.def.Effort = "high"
	got, err := buildArgv(in)
	if err != nil || strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("model+effort argv\n%q\nwant golden\n%q (err %v)", got, want, err)
	}
	if has(got, "high") {
		t.Fatalf("DriverDefaults.Effort leaked beside spec.Effort: %q", got)
	}
	in.effort = ""
	if argv, err := buildArgv(in); err != nil || !has(argv, "high") || has(argv, "max") {
		t.Fatalf("empty spec.Effort must fall back to DriverDefaults.Effort: %q (%v)", argv, err)
	}
	in.model, in.def.Effort = "", ""
	if argv, err := buildArgv(in); err != nil || has(argv, "--model") || has(argv, "--effort") {
		t.Fatalf("empty values must emit nothing: %q (%v)", argv, err)
	}
	for name, edit := range map[string]func(*argvInput){
		"effort outside the closed set": func(in *argvInput) { in.effort = "ultracode" },
		"effort wrong case":             func(in *argvInput) { in.effort = "HIGH" },
		"flag-shaped model":             func(in *argvInput) { in.model = "--dangerous" },
		"model with a space":            func(in *argvInput) { in.model = "opus fast" },
		"model with a newline":          func(in *argvInput) { in.model = "opus\n--x" },
	} {
		bad := baseInput(provider.ToolPolicyNone)
		edit(&bad)
		if _, err := buildArgv(bad); err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: buildArgv = %v, want KindInvalidInput", name, err)
		}
	}
	for _, ok := range []string{"opus", "claude-opus-4-1", "opus[1m]", "us.vendor/model:v1"} {
		good := baseInput(provider.ToolPolicyNone)
		good.model = ok
		if argv, err := buildArgv(good); err != nil || !has(argv, ok) {
			t.Fatalf("model %q refused: %v", ok, err)
		}
	}
}

func TestClaudeSpawnCarriesSpecModelEffort(t *testing.T) {
	f := newFx(t)
	spec := f.spec()
	spec.Model, spec.Effort = "opus", "low"
	cfg := f.config()
	cfg.Defaults.Effort = "high"
	if _, _, err := run(t, f.driver(cfg), spec); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	argv := argvOf(t, f)
	if !hasPair(argv, "--model", "opus") || !hasPair(argv, "--effort", "low") || has(argv, "high") {
		t.Fatalf("child argv %q: want --model opus and --effort low, not the default high", argv)
	}
	for name, edit := range map[string]func(*provider.AgentJobSpec){
		"effort outside the set": func(s *provider.AgentJobSpec) { s.Effort = "ultracode" },
		"flag-shaped model":      func(s *provider.AgentJobSpec) { s.Model = "--dangerous" },
	} {
		g := newFx(t) // the same fixture the driver runs against, so the any-ran marker is meaningful
		bad := g.spec()
		edit(&bad)
		if err := refusedBeforeAnyChild(t, g, g.config(), bad); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: Spawn = %v, want KindInvalidInput before any child", name, err)
		}
	}
	g := newFx(t) // an operator default outside the set is refused before any child too
	dcfg := g.config()
	dcfg.Defaults.Effort = "ultracode"
	if err := refusedBeforeAnyChild(t, g, dcfg, g.spec()); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Defaults.Effort outside the set: Spawn = %v, want KindInvalidInput before any child", err)
	}
}

// modelEffortGolden returns the fixture job id and the model_effort argv of
// spawn_argv.golden.json with its placeholders filled.
func modelEffortGolden(t *testing.T, root, home, prompt string) (provider.AgentJobID, []string) {
	t.Helper()
	var g struct {
		JobID       string   `json:"w6_job_id"`
		ModelEffort []string `json:"model_effort"`
	}
	if err := json.Unmarshal(readTestdata(t, "protocol/1/spawn_argv.golden.json"), &g); err != nil || len(g.ModelEffort) == 0 {
		t.Fatalf("model_effort golden: %v (%d tokens)", err, len(g.ModelEffort))
	}
	r := strings.NewReplacer("{ROOT}", root, "{HOME}", home, "{PROMPT}", prompt)
	for i := range g.ModelEffort {
		g.ModelEffort[i] = r.Replace(g.ModelEffort[i])
	}
	return provider.AgentJobID(g.JobID), g.ModelEffort
}
