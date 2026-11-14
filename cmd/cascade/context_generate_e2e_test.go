//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/generate"
	"github.com/acamarata/cascade/internal/context/policy"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/agents/claude"
)

// genPkgDir is read before any t.Chdir so testdata paths stay valid.
var genPkgDir, _ = os.Getwd()

func genTestdata(rel string) string { return filepath.Join(genPkgDir, filepath.FromSlash(rel)) }

// genFiles is the fresh repository every leg starts from.
func genFiles() map[string]string {
	return map[string]string{"go.mod": "module example.com/x\n\ngo 1.22\n", "main.go": "package main\n\nfunc main() {}\n"}
}

// genEnv isolates the homes, injects the recorded probe fixture, creates the
// repository with the real git binary and changes into it.
func genEnv(t *testing.T, fixture string, files map[string]string) string {
	t.Helper()
	if !gitRunnable() {
		t.Fatal("this e2e needs the real git binary")
	}
	for _, k := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(k, t.TempDir())
	}
	var f struct {
		Version, Stdout string
		ExitCode        int `json:"exit_code"`
	}
	genOK(t, json.Unmarshal(genRead(t, genTestdata("../../internal/context/generate/testdata/real-cc-workflows/"+fixture)), &f))
	prev := contextGenerateProbe
	t.Cleanup(func() { contextGenerateProbe = prev })
	contextGenerateProbe = func() generate.WorkflowsSupportProbe { return genReplay(f.Version, f.Stdout, f.ExitCode) }
	root := factsGitRepo(t, files)
	t.Chdir(root)
	return root
}

// genReplay answers --version and --help from one recorded probe fixture.
func genReplay(version, stdout string, exit int) generate.WorkflowsSupportProbe {
	return &generate.ExecProbe{
		LookPath: func(string) (string, error) { return "recorded-harness", nil },
		Run: func(_ context.Context, _ string, args, _ []string) ([]byte, int, error) {
			if args[0] == "--version" {
				return []byte(version + " (claude CLI)"), 0, nil
			}
			return []byte(stdout), exit, nil
		},
	}
}

// genRun drives the root command in process; want < 0 means any non-zero exit.
func genRun(t *testing.T, want int, args ...string) scriptResult {
	t.Helper()
	r := execCascade(append([]string{"context", "generate"}, args...))
	if (want >= 0 && r.exit != want) || (want < 0 && r.exit == 0) {
		t.Fatalf("context generate %v: exit %d, want %d\nstdout: %s\nstderr: %s", args, r.exit, want, r.stdout, r.stderr)
	}
	return r
}

// genManifest reads the manifest and checks every entry exists on disk.
func genManifest(t *testing.T, root string) (generate.Manifest, map[string]int) {
	t.Helper()
	m, ok, err := generate.ReadManifest(root)
	if err != nil || !ok {
		t.Fatalf("manifest: ok=%v err=%v", ok, err)
	}
	dirs := map[string]int{}
	for _, e := range m.Entries {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(e.Path))); err != nil {
			t.Errorf("manifest entry %s: %v", e.Path, err)
		}
		dirs[e.Path]++
		dirs[path.Dir(e.Path)]++
	}
	return m, dirs
}

func genOK(t *testing.T, err error) {
	if t.Helper(); err != nil {
		t.Fatal(err)
	}
}

func genRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	genOK(t, err)
	return b
}

func genWrite(t *testing.T, p string, b []byte) { t.Helper(); genOK(t, os.WriteFile(p, b, 0o644)) }

func TestContextGenerateE2E(t *testing.T) {
	root := genEnv(t, "supported-fixture.json", genFiles())
	genRun(t, 0)
	m, seen := genManifest(t, root)
	for _, p := range []string{".claude/CLAUDE.md", ".claude/rules/root.md", ".cascade/policy-kernel.md", "AGENTS.md", ".codex/config.toml", "opencode.json"} {
		if seen[p] == 0 {
			t.Errorf("manifest lacks %s", p)
		}
	}
	if seen[".claude/agents"] != 4 || seen[".claude/workflows"] == 0 || len(m.Deferrals) != 0 {
		t.Errorf("agents %d, workflows %d, deferrals %+v", seen[".claude/agents"], seen[".claude/workflows"], m.Deferrals)
	}
	genRun(t, 0, "--check")
	agentsMD := filepath.Join(root, "AGENTS.md")
	edited := append(genRead(t, agentsMD), "operator note outside every marker\n"...)
	genWrite(t, agentsMD, edited)
	genRun(t, 0)
	if !bytes.Equal(genRead(t, agentsMD), edited) {
		t.Fatal("the outside-edited AGENTS.md was rewritten")
	}
	genRead(t, agentsMD+".cascade-new")
	genRun(t, 0, "--check")
	rules := filepath.Join(root, ".claude", "rules", "root.md")
	b := genRead(t, rules)
	cut := len(b)/2 + bytes.IndexByte(b[len(b)/2:], '\n') + 1
	genWrite(t, rules, append(append(append([]byte{}, b[:cut]...), "edited inside the block\n"...), b[cut:]...))
	if r := genRun(t, -1, "--check"); !strings.Contains(r.stderr, ".claude/rules/root.md") || !strings.Contains(r.stdout, ".claude/rules/root.md") {
		t.Fatalf("--check did not name the edited block:\nstdout: %s\nstderr: %s", r.stdout, r.stderr)
	}
}

func TestGenerateWorkflowsDeferralExitsZero(t *testing.T) {
	root := genEnv(t, "unsupported-fixture.json", genFiles())
	r := genRun(t, 0)
	m, seen := genManifest(t, root)
	if len(m.Deferrals) != 1 || m.Deferrals[0].Owner == "" || m.Deferrals[0].Expiry == "" {
		t.Fatalf("deferrals: %+v", m.Deferrals)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "workflows")); seen[".claude/workflows"] != 0 || !os.IsNotExist(err) {
		t.Fatalf("unsupported probe still wrote workflows: listed %d, stat %v", seen[".claude/workflows"], err)
	}
	if !strings.Contains(r.stdout, "deferred") || !strings.Contains(r.stdout, m.Deferrals[0].Owner) {
		t.Fatalf("stdout does not report the deferral: %s", r.stdout)
	}
}

func TestContextGenerateErrorPaths(t *testing.T) {
	root := genEnv(t, "unsupported-fixture.json", genFiles())
	invalid := output.ExitCode(cascade.New(cascade.KindInvalidInput, "x"))
	for _, args := range [][]string{{"extra"}, {"--bogus"}, {"--check=maybe"}} {
		if r := genRun(t, -1, args...); r.stdout != "" {
			t.Errorf("%v wrote stdout: %s", args, r.stdout)
		}
	}
	genOK(t, os.MkdirAll(filepath.Join(root, ".cascade"), 0o755))
	genWrite(t, filepath.Join(root, ".cascade", "generation-manifest.json"), []byte("{"))
	r := genRun(t, invalid)
	if r.stdout != "" || !strings.Contains(r.stderr, "error: invalid-input: generate: decode manifest") {
		t.Fatalf("corrupt manifest: stdout %q stderr %q", r.stdout, r.stderr)
	}
	t.Chdir(t.TempDir())
	r = genRun(t, invalid)
	if r.stdout != "" || !strings.Contains(r.stderr, "not in a git repository") {
		t.Fatalf("outside a repository: stdout %q stderr %q", r.stdout, r.stderr)
	}
}

func TestContextGenerateNoPrompt(t *testing.T) {
	var runs [2]string
	for i, v := range []string{"1", ""} {
		root := genEnv(t, "unsupported-fixture.json", genFiles())
		t.Setenv("CASCADE_NO_INPUT", v)
		runs[i] = genRun(t, 0).stdout
		m, _ := genManifest(t, root)
		for _, e := range m.Entries {
			runs[i] += "\n== " + e.Path + "\n" + string(genRead(t, filepath.Join(root, filepath.FromSlash(e.Path))))
		}
	}
	if runs[0] != runs[1] || !strings.Contains(runs[0], "== AGENTS.md") {
		t.Fatalf("CASCADE_NO_INPUT changed the run:\n%s\n---\n%s", runs[0], runs[1])
	}
}

func TestContextGenerateDaemonlessSinkWritesStderr(t *testing.T) {
	files := genFiles()
	files["AGENTS.md"] = "operator text\n"
	root := genEnv(t, "unsupported-fixture.json", files)
	r := genRun(t, 0)
	want := "cascade: context generate refused AGENTS.md: " + generate.ReasonNoManifest + " (rendered to AGENTS.md.cascade-new)\n"
	if strings.Count(r.stderr, "cascade: context generate refused") != 1 || !strings.Contains("\n"+r.stderr, "\n"+want) || strings.Contains(r.stdout, "refused AGENTS.md:") {
		t.Fatalf("want exactly one stderr line %q\nstderr: %s\nstdout: %s", want, r.stderr, r.stdout)
	}
	if string(genRead(t, filepath.Join(root, "AGENTS.md"))) != "operator text\n" {
		t.Fatal("refused AGENTS.md changed")
	}
	genRead(t, filepath.Join(root, "AGENTS.md.cascade-new"))
}

// genHarness writes a stand-in claude that answers --version and the init
// probe and records the job argv NUL-separated; it never runs a real harness.
func genHarness(t *testing.T) (bin, argvOut string) {
	t.Helper()
	var init map[string]any
	genOK(t, json.Unmarshal(genRead(t, genTestdata("../../providers/agents/claude/testdata/claude_headless_init.json")), &init))
	init["permissionMode"], init["mcp_servers"] = "default", []any{}
	line, _ := json.Marshal(init)
	dir := t.TempDir()
	genWrite(t, filepath.Join(dir, "init"), append(line, '\n'))
	result := genRead(t, genTestdata("../../providers/agents/claude/testdata/protocol/1/result.json"))
	genWrite(t, filepath.Join(dir, "result"), append(bytes.ReplaceAll(result, []byte("\n"), nil), '\n'))
	argvOut, bin = filepath.Join(dir, "argv"), filepath.Join(dir, "claude")
	script := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = --version ] && { echo '2.1.273'; exit 0; }; done\n" +
		"case \" $* \" in *\" stream-json \"*) cat " + dir + "/init; exec sleep 30;; esac\n" +
		"for a in \"$@\"; do printf '%s\\0' \"$a\"; done > " + argvOut + ".tmp && mv " + argvOut + ".tmp " + argvOut + "\ncat " + dir + "/result\n"
	genOK(t, os.WriteFile(bin, []byte(script), 0o700))
	return bin, argvOut
}

// genSpawn spawns the stand-in through the production claude factory with
// kernelFile as DriverDefaults and returns the recorded argv.
func genSpawn(t *testing.T, root, kernelFile, bin, argvOut string) ([]string, error) {
	t.Helper()
	def := provider.NewDriverDefaults()
	def.KernelPromptFile = kernelFile
	r, err := agents.NewResolver(builtinPluginPresence())
	genOK(t, err)
	binding, factory, err := r.Resolve("agent:" + string(agents.HarnessClaude))
	genOK(t, err)
	keyVar, err := agents.KeyVarFor(binding, agents.CredentialAPIKey)
	genOK(t, err)
	home := t.TempDir()
	genOK(t, os.Chmod(home, 0o700))
	vendor := filepath.Join(home, "vendor")
	genOK(t, os.Mkdir(vendor, 0o700))
	clock := runtime.NewSystemClock()
	d, err := factory(agents.SpawnSite{Binary: bin, Defaults: def, KeyVar: keyVar, CredentialClass: agents.CredentialAPIKey,
		VendorAuthVar: claude.VendorConfigDirVar, Posture: provider.CompliancePosture{ProgrammaticEntitlement: true},
		RuntimeHome: home, Clock: clock, WorktreeRoot: root})
	genOK(t, err)
	path := "PATH=" + os.Getenv("PATH")
	_, err = d.Spawn(t.Context(), provider.AgentJobSpec{JobID: "kernel-argv", Prompt: "hello", DataClass: provider.DataClassInternal,
		WorktreeRoot: root, Filter: func(b []byte) ([]byte, error) { return b, nil },
		Envelope: provider.SpawnEnvelope{Digest: strings.Repeat("0f", 32), Principal: "principal-test", Roots: []string{filepath.ToSlash(root)},
			Sensitivity: provider.SensitivityInternal, ExpiresAt: clock.Now().Add(time.Hour)},
		Env: provider.SpawnEnv{Home: home, Tool: []string{path, "HOME=" + home},
			Driver: []string{path, "HOME=" + home, claude.VendorConfigDirVar + "=" + vendor, keyVar + "=synthetic-key-value-0001"}}})
	if err != nil {
		return nil, err
	}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if b, err := os.ReadFile(argvOut); err == nil {
			return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00"), nil
		}
	}
	t.Fatal("the stand-in harness never recorded its argv")
	return nil, nil
}

func TestGeneratedKernelReachesClaudeArgv(t *testing.T) {
	bin, argvOut := genHarness(t)
	root := genEnv(t, "unsupported-fixture.json", genFiles())
	genRun(t, 0)
	repoKernel := filepath.Join(root, ".cascade", "policy-kernel.md")
	genRead(t, repoKernel)
	daemonKernel, err := policy.EnsureDaemonKernelFile(filepath.Join(t.TempDir(), "data"))
	genOK(t, err)
	if _, err := genSpawn(t, root, repoKernel, bin, argvOut); !cascade.HasKind(err, cascade.KindIntegrity) || !strings.Contains(err.Error(), "KernelPromptFile") {
		t.Fatalf("repository kernel as DriverDefaults: %v, want an integrity refusal", err)
	}
	if _, err := os.Stat(argvOut); !os.IsNotExist(err) {
		t.Fatal("a job ran with the repository kernel")
	}
	argv, err := genSpawn(t, root, daemonKernel, bin, argvOut)
	genOK(t, err)
	found := false
	for i, a := range argv {
		if strings.Contains(a, ".cascade/policy-kernel.md") || strings.Contains(a, repoKernel) {
			t.Errorf("argv[%d] names the repository kernel: %q", i, a)
		}
		// The driver verifies the daemon kernel and passes its bytes as text.
		found = found || (a == "--append-system-prompt" && i+1 < len(argv) && argv[i+1] == string(policy.RenderKernelFile()))
	}
	if !found {
		t.Fatalf("daemon kernel text missing from argv: %q", argv)
	}
}
