package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestCodexForbiddenLaunchPathRejected(t *testing.T) {
	for _, argv := range [][]string{
		{"/usr/bin/codex", "mcp-server"},
		{"/usr/bin/codex-mcp-server", "app-server"},
		{"/usr/bin/codex", "app-server", "mcp-server"},
		{"/usr/bin/codex", "exec"},
		{"/usr/bin/codex"},
	} {
		if err := ValidateLaunch(argv); err != ErrForbiddenLaunchPath || err.Error() != ErrForbiddenLaunchPath.Error() {
			t.Errorf("ValidateLaunch(%q) = %v, want ErrForbiddenLaunchPath", argv, err)
		}
	}
	if err := ValidateLaunch([]string{"/usr/bin/codex", "app-server"}); err != nil {
		t.Fatalf("app-server refused: %v", err)
	}
	if _, err := New(Config{Binary: "/opt/codex-mcp-server"}); err != ErrForbiddenLaunchPath {
		t.Fatalf("New(mcp-server binary) = %v", err)
	}
}

func TestCodexEnvAllowlist(t *testing.T) {
	k := newKit(t)
	k.opts.EnvOut = true
	spec := k.spec()
	if _, _, err := k.run(spec); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(k.codexHome, "env.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := append([]string(nil), spec.Env.Driver...)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("child environ = %q, want exactly Env.Driver %q", got, want)
	}
	for _, extra := range []string{"AWS_SECRET_ACCESS_KEY=x", "NOEQUALS", "ALL_PROXY=http://p"} {
		bad := k.spec()
		bad.Env.Driver = append(bad.Env.Driver, extra)
		if _, err := k.driver().Spawn(testCtx(t), bad); !isSentinel(err, ErrEnvNotAllowed) {
			t.Errorf("Spawn with %q = %v, want ErrEnvNotAllowed", extra, err)
		}
	}
	bad := k.spec()
	bad.Env.Home = "/elsewhere"
	if _, err := k.driver().Spawn(testCtx(t), bad); err != ErrHomeMismatch {
		t.Fatalf("HOME != Env.Home: %v", err)
	}
}

func TestCodexDriverRejectsEmptySpawnEnv(t *testing.T) {
	k := newKit(t)
	spec := k.spec()
	spec.Env = provider.SpawnEnv{}
	if _, err := k.driver().Spawn(testCtx(t), spec); err != ErrEmptySpawnEnv {
		t.Fatalf("Spawn(empty env) = %v, want ErrEmptySpawnEnv", err)
	}
	if k.childStarted() {
		t.Fatal("a child ran despite the refusal")
	}
}

func TestCodexEntitlementFalseRefuses(t *testing.T) {
	k := newKit(t)
	k.cfg.Posture = entitled(false)
	if _, err := k.driver().Spawn(testCtx(t), k.spec()); err != provider.ErrEntitlement {
		t.Fatalf("Spawn = %v, want ErrEntitlement", err)
	}
	if k.childStarted() {
		t.Fatal("a child ran despite the refusal")
	}
}

func TestCodexRefusesEmptyWorktreeRoot(t *testing.T) {
	k := newKit(t)
	spec := k.spec()
	spec.WorktreeRoot = ""
	if _, err := k.driver().Spawn(testCtx(t), spec); err != ErrEmptyWorktreeRoot {
		t.Fatalf("Spawn(empty root) = %v, want ErrEmptyWorktreeRoot", err)
	}
	if k.childStarted() {
		t.Fatal("a child ran despite the refusal")
	}
}

func TestCodexNilFilterRefuses(t *testing.T) {
	k := newKit(t)
	spec := k.spec()
	spec.Filter = nil
	if _, err := k.driver().Spawn(testCtx(t), spec); err != provider.ErrEgressSubstitutionFailed {
		t.Fatalf("Spawn(nil filter) = %v", err)
	}
	if k.childStarted() {
		t.Fatal("a child ran despite the refusal")
	}
}

func TestCodexPreSpawnScanAborts(t *testing.T) {
	k := newKit(t)
	seeded := "AKIA" + "9QRX2PLM7NZV6WTC"
	if err := os.WriteFile(filepath.Join(k.root, "seed.env"), []byte("SECRET="+seeded), 0o600); err != nil {
		t.Fatal(err)
	}
	k.cfg.ContentMatch = func(b []byte) (string, bool) {
		return "aws", slices.Contains([]string{string(b)}, "SECRET="+seeded)
	}
	if _, err := k.driver().Spawn(testCtx(t), k.spec()); err != provider.ErrPreSpawnSecretFound {
		t.Fatalf("Spawn = %v, want ErrPreSpawnSecretFound", err)
	}
	if k.childStarted() {
		t.Fatal("a child ran despite the scan hit")
	}
}

func TestCodexAutoApproveFlagRejected(t *testing.T) {
	for _, mut := range []func(*provider.DriverDefaults){
		func(d *provider.DriverDefaults) { d.SessionNameFlag = "--dangerously-bypass-approvals-and-sandbox" },
		func(d *provider.DriverDefaults) { d.PluginDir = "-c sandbox_mode=danger-full-access" },
		func(d *provider.DriverDefaults) { d.MCPConfigPath = "-c approval_policy=never" },
		func(d *provider.DriverDefaults) { d.FallbackModel = "--full-auto" },
		func(d *provider.DriverDefaults) { d.Effort = "never" },
		func(d *provider.DriverDefaults) { d.KernelPromptFile = "approvalsReviewer=auto_review" },
	} {
		d := testDefaults()
		mut(&d)
		if _, err := New(Config{Binary: "/usr/bin/codex", Defaults: d}); err != ErrAutoApprove {
			t.Errorf("New(%+v) = %v, want ErrAutoApprove", d, err)
		}
	}
	d := testDefaults()
	d.PluginDir = "/some/plugins"
	if _, err := New(Config{Binary: "/usr/bin/codex", Defaults: d}); err == nil {
		t.Fatal("a flag-shaped default was accepted")
	}
	d = testDefaults()
	d.Effort = "turbo"
	if _, err := New(Config{Binary: "/usr/bin/codex", Defaults: d}); err != ErrInvalidEffort {
		t.Fatalf("New(effort turbo) = %v", err)
	}
	if _, err := New(Config{Binary: "/usr/bin/codex", VendorAuthVar: "HOME"}); err == nil {
		t.Fatal("a foreign vendor auth variable was accepted")
	}
}

func TestCodexUsesSpecJobID(t *testing.T) {
	k := newKit(t)
	spec := k.spec()
	spec.JobID = "job-codex-1"
	d := k.driver()
	res, err := d.Spawn(testCtx(t), spec)
	if err != nil || res.JobID != spec.JobID {
		t.Fatalf("Spawn = %+v, %v; want the spec's job id", res, err)
	}
	if _, err := d.Spawn(testCtx(t), spec); err != ErrDuplicateJob {
		t.Fatalf("live duplicate = %v, want ErrDuplicateJob", err)
	}
	if _, err := d.Collect(testCtx(t), res.JobID); err != nil {
		t.Fatal(err)
	}
	if res2, err := d.Spawn(testCtx(t), spec); err != nil || res2.JobID != spec.JobID {
		t.Fatalf("respawn after the job ended = %v", err)
	}
}

func TestCodexExecutableReported(t *testing.T) {
	d, err := New(Config{Binary: "/opt/bin/codex"})
	if err != nil {
		t.Fatal(err)
	}
	var r provider.ExecutableReporter = d
	if r.Executable() != "/opt/bin/codex" {
		t.Fatalf("Executable = %q", r.Executable())
	}
	var _ provider.AgentProvider = d
}

func TestCodexMessageUnsupported(t *testing.T) {
	k := newKit(t)
	d := k.driver()
	res, err := d.Spawn(testCtx(t), k.spec())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Message(testCtx(t), res.JobID, "more"); err != provider.ErrMessageUnsupported {
		t.Fatalf("Message(live) = %v", err)
	}
	if err := d.Message(testCtx(t), "missing", "x"); err != provider.ErrJobNotFound {
		t.Fatalf("Message(missing) = %v", err)
	}
}
