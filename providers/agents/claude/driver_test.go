//go:build !windows

package claude

// Purpose: Spawn's custody guards: entitlement, the exact child environ,
//   the SpawnEnv refusals (empty, allowlist, HOME, vendor var, tool env,
//   runtime-home mode), the pre-spawn scan, session naming, New and
//   Capabilities.
// Constraints: every refusal test also proves no child ran (the fake
//   harness touches a marker on ANY invocation, --version included).
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestClaudeSpawnRefusesWithoutProgrammaticEntitlement(t *testing.T) {
	f := newFx(t)
	cfg := f.config()
	prof := f.profile()
	prof.Posture.ProgrammaticEntitlement = false
	cfg.Profile = fakeProfile{prof: prof}
	_, err := f.driver(cfg).Spawn(context.Background(), f.spec())
	if err != provider.ErrEntitlement {
		t.Fatalf("Spawn = %v, want the ErrEntitlement sentinel", err)
	}
	if exists(f.anyRan) {
		t.Fatal("a subprocess started without programmatic entitlement")
	}
}

func TestClaudeChildEnvEqualsSpawnEnv(t *testing.T) {
	f := newFx(t)
	envOut := filepath.Join(f.dir, "env")
	f.body = "env > " + envOut + "\n" + f.body
	t.Setenv("CASCADE_DAEMON_ONLY_CANARY", "must-not-reach-the-child")
	spec := f.spec()
	_, _, err := run(t, f.driver(f.config()), spec)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	b, _ := os.ReadFile(envOut)
	got := strings.Split(strings.TrimSpace(string(b)), "\n")
	got = dropShellVars(got)
	want := append([]string(nil), spec.Env.Driver...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("child environ\n%v\nwant exactly SpawnEnv.Driver\n%v", got, want)
	}
}

// dropShellVars removes the variables /bin/sh itself sets (PWD, SHLVL, _)
// so the comparison sees only what the driver handed over.
func dropShellVars(env []string) []string {
	out := []string{}
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k != "PWD" && k != "SHLVL" && k != "_" && k != "OLDPWD" {
			out = append(out, kv)
		}
	}
	return out
}

func TestClaudeDriverRejectsEmptySpawnEnv(t *testing.T) {
	f := newFx(t)
	spec := f.spec()
	spec.Env.Driver = nil
	_, err := f.driver(f.config()).Spawn(context.Background(), spec)
	if err != provider.ErrEntitlement {
		t.Fatalf("Spawn(empty Env.Driver) = %v, want the ErrEntitlement sentinel", err)
	}
	if exists(f.anyRan) {
		t.Fatal("a subprocess started with an empty spawn environment")
	}
}

// TestClaudeSpawnEnvAllowlistKeyCheck is the allowlist check's own test:
// every other part of the environment is valid, so only the key check can
// refuse (item 1).
func TestClaudeSpawnEnvAllowlistKeyCheck(t *testing.T) {
	f := newFx(t)
	spec := f.spec()
	spec.Env.Driver = append(spec.Env.Driver, "AWS_SECRET_ACCESS_KEY=x")
	_, err := f.driver(f.config()).Spawn(context.Background(), spec)
	if !errors.Is(err, ErrSpawnEnvRejected) || !strings.Contains(err.Error(), `key "AWS_SECRET_ACCESS_KEY" is outside the allowlist`) {
		t.Fatalf("Spawn(extra key) = %v, want the allowlist refusal", err)
	}
	if exists(f.anyRan) {
		t.Fatal("a subprocess started with a key outside DriverEnvAllowlist")
	}
}

func TestClaudeSpawnEnvRefusals(t *testing.T) {
	cases := map[string]struct {
		mutate func(f *fx, s *provider.AgentJobSpec, p *Profile)
		msg    string
	}{
		"home mismatch": {func(f *fx, s *provider.AgentJobSpec, _ *Profile) { s.Env.Home = f.dir }, "must equal SpawnEnv.Home"},
		"vendor outside home": {func(f *fx, s *provider.AgentJobSpec, _ *Profile) {
			s.Env.Driver[2] = VendorConfigDirVar + "=" + f.dir
		}, "under the runtime home"},
		"missing vendor": {func(_ *fx, s *provider.AgentJobSpec, _ *Profile) { s.Env.Driver = s.Env.Driver[:2] }, "under the runtime home"},
		"duplicate": {func(_ *fx, s *provider.AgentJobSpec, _ *Profile) {
			s.Env.Driver = append(s.Env.Driver, s.Env.Driver[0])
		}, "appears twice"},
		"malformed": {func(_ *fx, s *provider.AgentJobSpec, _ *Profile) { s.Env.Driver = append(s.Env.Driver, "NOEQUALS") }, "malformed"},
		"wrong vendor var": {func(_ *fx, _ *provider.AgentJobSpec, p *Profile) { p.VendorAuthVar = "OTHER_DIR" },
			"is not this driver's"},
		"home mode": {func(f *fx, _ *provider.AgentJobSpec, _ *Profile) { _ = os.Chmod(f.home, 0o755) }, "want 0700"},
		"home comma": {func(f *fx, s *provider.AgentJobSpec, p *Profile) {
			h := filepath.Join(f.dir, "a,b")
			mustMkdir(t, filepath.Join(h, "v"))
			s.Env.Home, p.RuntimeHome = h, h
			s.Env.Driver = []string{"HOME=" + h, VendorConfigDirVar + "=" + filepath.Join(h, "v"), KeyVarOAuthToken + "=" + fakeKeyValue}
		}, "rule lists split on"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			spec, prof := f.spec(), f.profile()
			c.mutate(f, &spec, &prof)
			cfg := f.config()
			cfg.Profile = fakeProfile{prof: prof}
			_, err := f.driver(cfg).Spawn(context.Background(), spec)
			if !errors.Is(err, ErrSpawnEnvRejected) || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("Spawn = %v, want a refusal naming %q", err, c.msg)
			}
			if exists(f.anyRan) {
				t.Fatal("a subprocess started despite the refusal")
			}
		})
	}
}

func TestClaudeToolEnvOmitsVendorAuthVar(t *testing.T) {
	for _, k := range provider.ToolEnvAllowlist() {
		if k == VendorConfigDirVar {
			t.Fatal("ToolEnvAllowlist carries the vendor auth variable")
		}
	}
	for name, tool := range map[string]string{
		"vendor var": VendorConfigDirVar + "=/x", "foreign key": "GITHUB_TOKEN=x",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			spec := f.spec()
			spec.Env.Tool = append(spec.Env.Tool, tool)
			_, err := f.driver(f.config()).Spawn(context.Background(), spec)
			if !errors.Is(err, ErrSpawnEnvRejected) || !strings.Contains(err.Error(), "outside the allowlist") {
				t.Fatalf("Spawn(tool env %q) = %v, want a refusal", tool, err)
			}
			if exists(f.anyRan) {
				t.Fatal("a subprocess started with a vendor auth variable in the tool env")
			}
		})
	}
}

func TestClaudePreSpawnScanAborts(t *testing.T) {
	for name, rel := range map[string]string{"in scope": "src/.env", "outside scope": ".env"} {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			mustMkdir(t, filepath.Join(f.root, "src"))
			mustWrite(t, filepath.Join(f.root, rel), "AWS=AKIA"+strings.Repeat("Q", 16)+"\n")
			spec := f.spec()
			spec.Scope = provider.WorktreeScope{InScopePrefixes: []string{"src"}}
			_, err := f.driver(f.config()).Spawn(context.Background(), spec)
			if err != provider.ErrPreSpawnSecretFound {
				t.Fatalf("Spawn = %v, want the ErrPreSpawnSecretFound sentinel", err)
			}
			if exists(f.anyRan) {
				t.Fatal("a subprocess started after a pre-spawn scan hit")
			}
		})
	}
}

func TestClaudeSessionNameArgument(t *testing.T) {
	for name, ticket := range map[string]string{"ticket id": "P1-E30-W6-S61-T2", "job id": ""} {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			cfg := f.config()
			cfg.Defaults.SessionNameFlag = "--name"
			spec := f.spec()
			spec.TicketID, spec.JobID = ticket, "job-w7-session-0001"
			res, _, err := run(t, f.driver(cfg), spec)
			if err != nil {
				t.Fatalf("Collect: %v", err)
			}
			want := ticket
			if want == "" {
				want = string(res.JobID)
			}
			argv := argvOf(t, f)
			sid := provider.SessionIDFor(spec.JobID)
			if !hasPair(argv, "--name", want) || !hasPair(argv, "--session-id", sid) ||
				sid == provider.SessionIDFor(provider.AgentJobID(spec.TicketID)) {
				t.Fatalf("argv %q lacks --name %s / --session-id %s (derived from the job id, never the ticket id)", argv, want, sid)
			}
			for _, kv := range spec.Env.Driver {
				if strings.Contains(kv, want) {
					t.Fatalf("session name travelled in the environment: %s", kv)
				}
			}
		})
	}
}

func hasPair(argv []string, flag, val string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == val {
			return true
		}
	}
	return false
}

func TestNewValidatesConfig(t *testing.T) {
	f := newFx(t)
	for name, mut := range map[string]func(*Config){
		"relative binary": func(c *Config) { c.Binary = "claude" },
		"nil leaser":      func(c *Config) { c.Worktree = nil },
		"no grace":        func(c *Config) { c.Defaults.CancelGrace = 0 },
	} {
		cfg := f.config()
		mut(&cfg)
		if _, err := New(cfg); err == nil {
			t.Fatalf("%s: New accepted an invalid config", name)
		}
	}
	defaulted := f.config()
	defaulted.ProbeTimeout, defaulted.Descriptor = 0, Descriptor{}
	if d := f.driver(defaulted); d.cfg.ProbeTimeout != defaultProbeTimeout || d.cfg.Descriptor.MinVersions == nil {
		t.Fatal("New did not default the probe timeout and descriptor")
	}
	in := populated(baseInput(provider.ToolPolicyAll))
	if pf := probeFlags(in); !hasPair(pf, "--mcp-config", "/k/mcp.json") || !hasPair(pf, "--plugin-dir", "/k/plugins") ||
		!hasPair(pf, "--permission-mode", "bypassPermissions") || !hasPair(pf, "--add-dir", "/r/src") {
		t.Fatalf("probeFlags %q", pf)
	}
	d := f.driver(f.config())
	caps, err := d.Capabilities(context.Background(), "")
	if err != nil || !caps.CompliancePosture.ProgrammaticEntitlement {
		t.Fatalf("Capabilities = %+v, %v", caps, err)
	}
	cfg := f.config()
	cfg.Profile = fakeProfile{err: provider.ErrJobNotFound}
	if _, err := f.driver(cfg).Capabilities(context.Background(), ""); err == nil {
		t.Fatal("Capabilities hid a resolver error")
	}
	if _, err := f.driver(cfg).Spawn(context.Background(), f.spec()); err == nil {
		t.Fatal("Spawn hid a resolver error")
	}
}
