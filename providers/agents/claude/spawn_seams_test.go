//go:build !windows

package claude

// Purpose: the R-14.329 / R-14.322 spawn seams through a real Spawn: the
//   empty and mismatched spec.WorktreeRoot, the nil per-spawn Filter, the
//   caller's spec.JobID, Executable(), an unsupported flag value, and
//   Message as a capability probe. Every refusal is checked by identity and
//   by the absence of any harness invocation (the fixture's any-ran marker
//   is touched by every run of the fake harness, --version included).
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; split
//   out because driver_test.go and exec_test.go sit near the 300-line cap.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// refusedBeforeAnyChild spawns spec and returns the error, failing when the
// fake harness ran at all.
func refusedBeforeAnyChild(t *testing.T, f *fx, cfg Config, spec provider.AgentJobSpec) error {
	t.Helper()
	_, err := f.driver(cfg).Spawn(context.Background(), spec)
	if err == nil || exists(f.anyRan) {
		t.Fatalf("Spawn = %v, harness ran = %v: want a refusal before any child", err, exists(f.anyRan))
	}
	return err
}

func TestClaudeRefusesEmptyWorktreeRoot(t *testing.T) {
	f := newFx(t)
	spec := f.spec()
	spec.WorktreeRoot = ""
	err := refusedBeforeAnyChild(t, f, f.config(), spec)
	if !wraps(err, provider.ErrEntitlement) || !strings.Contains(err.Error(), "WorktreeRoot") {
		t.Fatalf("Spawn = %v, want ErrEntitlement naming spec.WorktreeRoot", err)
	}
}

func TestClaudeRefusesWorktreeRootLeaseMismatch(t *testing.T) {
	for name, root := range map[string]func(f *fx) string{
		"another tree":    func(*fx) string { return newGitWorktree(t) },
		"relative root":   func(f *fx) string { return filepath.Base(f.root) },
		"unclean spelled": func(f *fx) string { return f.root + string(filepath.Separator) + "." },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			spec := f.spec()
			spec.WorktreeRoot = root(f)
			if err := refusedBeforeAnyChild(t, f, f.config(), spec); !wraps(err, ErrWorktreeRootMismatch) {
				t.Fatalf("Spawn = %v, want ErrWorktreeRootMismatch", err)
			}
		})
	}
	f := newFx(t) // the matching root is the child's cwd
	f.body = "pwd -P > " + filepath.Join(f.dir, "cwd") + "\n" + f.body
	if _, _, err := run(t, f.driver(f.config()), f.spec()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	want, _ := filepath.EvalSymlinks(f.root)
	if got, _ := os.ReadFile(filepath.Join(f.dir, "cwd")); strings.TrimSpace(string(got)) != want {
		t.Fatalf("child cwd %q, want spec.WorktreeRoot %q", got, want)
	}
}

func TestClaudeNilFilterRefuses(t *testing.T) {
	f := newFx(t)
	spec := f.spec()
	spec.Filter = nil
	if err := refusedBeforeAnyChild(t, f, f.config(), spec); err != provider.ErrEgressSubstitutionFailed {
		t.Fatalf("Spawn = %v, want the ErrEgressSubstitutionFailed sentinel", err)
	}
}

func TestClaudeUsesSpecJobID(t *testing.T) {
	f := newFx(t)
	d := f.driver(f.config())
	spec := f.spec()
	spec.JobID = "shim-job-7"
	bad := spec
	bad.ToolPolicy = "bogus" // refused after the id is claimed: the claim must be released
	if _, err := d.Spawn(context.Background(), bad); err != provider.ErrInvalidToolPolicy {
		t.Fatalf("Spawn(bad policy) = %v", err)
	}
	res, cr, err := run(t, d, spec)
	if err != nil || res.JobID != "shim-job-7" || cr.JobID != "shim-job-7" {
		t.Fatalf("SpawnResult %q CollectResult %q err %v, want the caller's job id", res.JobID, cr.JobID, err)
	}
	if st, err := d.Status(context.Background(), "shim-job-7"); err != nil || st != provider.AgentRunAccepted {
		t.Fatalf("Status(spec id) = %s, %v", st, err)
	}
	if err := os.Remove(f.anyRan); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Spawn(context.Background(), spec); !cascade.HasKind(err, cascade.KindConflict) || exists(f.anyRan) {
		t.Fatalf("Spawn(live id) = %v, harness ran = %v: want KindConflict before any child", err, exists(f.anyRan))
	}
	spec.JobID = ""
	res, _, err = run(t, d, spec)
	if err != nil || res.JobID == "" || res.JobID == "shim-job-7" {
		t.Fatalf("minted id %q, %v", res.JobID, err)
	}
}

func TestClaudeExecutableReported(t *testing.T) {
	f := newFx(t)
	cfg := f.config()
	link := filepath.Join(t.TempDir(), "harness-link")
	if err := os.Symlink(cfg.Binary, link); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(cfg.Binary)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Binary = link
	var r provider.ExecutableReporter = f.driver(cfg)
	if got := r.Executable(); got != want || !filepath.IsAbs(got) {
		t.Fatalf("Executable() = %q, want the resolved %q", got, want)
	}
	for name, bin := range map[string]string{
		"relative": "harness", "missing": filepath.Join(f.dir, "absent"), "directory": f.dir,
	} {
		bad := f.config()
		bad.Binary = bin
		if _, err := New(bad); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s binary: New = %v, want KindInvalidInput", name, err)
		}
	}
}

func TestUnsupportedFlagValueRefuses(t *testing.T) {
	for name, set := range map[string]func(*provider.DriverDefaults){
		"--append-subagent-system-prompt-file": func(d *provider.DriverDefaults) { d.SubagentKernelPromptFile = "/k/sub.md" },
		"--max-turns":                          func(d *provider.DriverDefaults) { d.MaxTurns = 3 },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			cfg := f.config()
			set(&cfg.Defaults)
			err := refusedBeforeAnyChild(t, f, cfg, f.spec())
			if !wraps(err, provider.ErrHarnessIncompatible) || !strings.Contains(err.Error(), name) {
				t.Fatalf("Spawn = %v, want ErrHarnessIncompatible naming %s", err, name)
			}
			in := baseInput(provider.ToolPolicyNone)
			set(&in.def)
			if argv, err := buildArgv(in); !wraps(err, provider.ErrHarnessIncompatible) || has(argv, name) {
				t.Fatalf("buildArgv = %q, %v", argv, err)
			}
		})
	}
}

func TestClaudeMessageUnsupported(t *testing.T) {
	f := newFx(t)
	stdin := filepath.Join(f.dir, "stdin")
	d, res := spawnSleeper(t, f, f.config(), "cat > "+stdin+"\n"+f.body)
	ctx := context.Background()
	if err := d.Message(ctx, res.JobID, "a later turn"); err != provider.ErrMessageUnsupported {
		t.Fatalf("Message(known job) = %v, want the ErrMessageUnsupported sentinel", err)
	}
	if err := d.Message(ctx, "claude-unknown", "x"); err != provider.ErrJobNotFound {
		t.Fatalf("Message(unknown job) = %v, want ErrJobNotFound", err)
	}
	if _, err := d.Collect(ctx, res.JobID); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got, err := os.ReadFile(stdin); err != nil || len(got) != 0 {
		t.Fatalf("the child's stdin carried %q (%v); Message must write nothing", got, err)
	}
	if err := d.Message(ctx, res.JobID, "after exit"); err != provider.ErrMessageUnsupported {
		t.Fatalf("Message(finished job) = %v", err)
	}
}
