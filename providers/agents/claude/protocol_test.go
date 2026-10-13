//go:build !windows

package claude

// Purpose: the argv emission rule against its golden (W6 and populated),
//   the version gates, bypass-only-for-all, api_key-only flags, and the
//   runtime protocol refusal (R-21.158).
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

type argvGolden struct {
	Ticket    string   `json:"w6_ticket"`
	JobID     string   `json:"w6_job_id"`
	W6        []string `json:"w6"`
	Populated []string `json:"populated"`
}

func loadArgvGolden(t *testing.T, root, home, prompt string) argvGolden {
	t.Helper()
	var g argvGolden
	if err := json.Unmarshal(readTestdata(t, "protocol/1/spawn_argv.golden.json"), &g); err != nil {
		t.Fatal(err)
	}
	r := strings.NewReplacer("{ROOT}", root, "{HOME}", home, "{PROMPT}", prompt)
	for _, list := range [][]string{g.W6, g.Populated} {
		for i := range list {
			list[i] = r.Replace(list[i])
		}
	}
	return g
}

func baseInput(policy string) argvInput {
	return argvInput{def: provider.NewDriverDefaults(), desc: NewDescriptor(), version: "2.1.273", policy: policy,
		cred: CredentialClassOAuthToken, name: "P1-TEST-T1", jobID: "job-w6-fixture-0001", root: "/r", addDirs: []string{"/r/src"},
		home: "/h/home", prompt: "do the thing"}
}

func populated(in argvInput) argvInput {
	in.def.KernelPromptFile = "/k/kernel.md"
	in.def.PluginDir, in.def.Effort = "/k/plugins", "high"
	in.def.FallbackModel, in.def.MCPConfigPath, in.def.SessionNameFlag = "fallback-x", "/k/mcp.json", "--name"
	in.def.MaxBudgetUSD, in.cred, in.policy = 2.5, CredentialClassAPIKey, provider.ToolPolicyAll
	return in
}

func TestHeadlessSpawnFlagsGolden(t *testing.T) {
	g := loadArgvGolden(t, "/r", "/h/home", "do the thing")
	in := baseInput(provider.ToolPolicyNone)
	in.jobID = provider.AgentJobID(g.JobID)
	w6, err := buildArgv(in)
	if err != nil || strings.Join(w6, "\x00") != strings.Join(g.W6, "\x00") {
		t.Fatalf("W6 argv\n%q\nwant golden\n%q (err %v)", w6, g.W6, err)
	}
	pop, err := buildArgv(populated(in))
	if err != nil || strings.Join(pop, "\x00") != strings.Join(g.Populated, "\x00") {
		t.Fatalf("populated argv\n%q\nwant golden\n%q (err %v)", pop, g.Populated, err)
	}
	// The spawned child receives exactly the argv buildArgv builds.
	f := newFx(t)
	mustMkdir(t, filepath.Join(f.root, "src"))
	spec := f.spec()
	spec.TicketID, spec.JobID = g.Ticket, provider.AgentJobID(g.JobID)
	spec.Scope = provider.WorktreeScope{InScopePrefixes: []string{"src"}}
	if _, _, err := run(t, f.driver(f.config()), spec); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	live := loadArgvGolden(t, f.root, f.home, "hello")
	if got := argvOf(t, f); strings.Join(got, "\x00") != strings.Join(live.W6, "\x00") {
		t.Fatalf("child argv\n%q\nwant\n%q", got, live.W6)
	}
}

func TestHeadlessFlagsVersionGated(t *testing.T) {
	in := baseInput(provider.ToolPolicyNone)
	in.version = "2.1.258"
	if _, err := buildArgv(in); !errors.Is(err, provider.ErrHarnessIncompatible) || !strings.Contains(err.Error(), "--permission-prompts") {
		t.Fatalf("2.1.258: %v, want the --permission-prompts gate", err)
	}
	in = populated(baseInput(provider.ToolPolicyNone))
	in.version = "2.1.260"
	argv, err := buildArgv(in)
	if err != nil || has(argv, "--plugin-dir") {
		t.Fatalf("2.1.260: argv %q err %v: --plugin-dir must fall back to the hook-pack copy", argv, err)
	}
	in.version = "2.1.26" // numerically older than 2.1.259, lexically not
	if _, err := buildArgv(in); !errors.Is(err, provider.ErrHarnessIncompatible) || !strings.Contains(err.Error(), "--permission-prompts") {
		t.Fatalf("2.1.26: %v, want the --permission-prompts refusal", err)
	}
	for name, block := range map[string]string{
		"exit 3":              "exit 3",
		"no semver":           "echo banana; exit 0",
		"too old":             `echo "2.1.200 (vendor harness)"; exit 0`,
		"no output":           "exit 0",
		"stderr only":         "echo 2.1.273 >&2; exit 0",
		"version then exit 3": `echo "2.1.273 (vendor harness)"; exit 3`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			f.versionBlock = block
			_, err := f.driver(f.config()).Spawn(context.Background(), f.spec())
			if !errors.Is(err, provider.ErrHarnessIncompatible) {
				t.Fatalf("Spawn = %v, want ErrHarnessIncompatible", err)
			}
			if exists(f.jobRan) {
				t.Fatal("the job ran although the version probe failed")
			}
		})
	}
}

func TestBypassOnlyForToolPolicyAll(t *testing.T) {
	for _, p := range []string{provider.ToolPolicyNone, provider.ToolPolicyReadonly, provider.ToolPolicyAll} {
		argv, err := buildArgv(baseInput(p))
		if err != nil {
			t.Fatal(err)
		}
		if bypass := has(argv, "bypassPermissions"); bypass != (p == provider.ToolPolicyAll) {
			t.Fatalf("policy %s: bypassPermissions emitted = %v", p, bypass)
		}
		if p != provider.ToolPolicyAll && has(argv, "--permission-mode") {
			t.Fatalf("policy %s emitted --permission-mode; the default mode is the flag's absence", p)
		}
	}
	readonly, _ := buildArgv(baseInput(provider.ToolPolicyReadonly))
	if has(readonly, "WebFetch") || !has(readonly, "Bash") {
		t.Fatalf("readonly argv %q", readonly)
	}
	if _, err := buildArgv(baseInput("bogus")); err != provider.ErrInvalidToolPolicy {
		t.Fatalf("bogus policy: %v", err)
	}
	f := newFx(t)
	spec := f.spec()
	spec.ToolPolicy = "everything"
	if _, err := f.driver(f.config()).Spawn(context.Background(), spec); err != provider.ErrInvalidToolPolicy || exists(f.anyRan) {
		t.Fatalf("Spawn(unknown policy) = %v, anyRan=%v", err, exists(f.anyRan))
	}
	spec.ToolPolicy = "" // resolves to none
	if _, _, err := run(t, f.driver(f.config()), spec); err != nil || has(argvOf(t, f), "bypassPermissions") {
		t.Fatalf("empty policy: err %v argv %q", err, argvOf(t, f))
	}
}

func TestBareOnlyForAPIKeyLane(t *testing.T) {
	sub := populated(baseInput(provider.ToolPolicyNone))
	sub.cred = CredentialClassOAuthToken
	argv, _ := buildArgv(sub)
	if has(argv, "--bare") || has(argv, "--max-budget-usd") {
		t.Fatalf("oauth_token lane argv %q carries an api_key-only flag", argv)
	}
	api := baseInput(provider.ToolPolicyNone)
	api.cred = CredentialClassAPIKey
	argv, _ = buildArgv(api)
	if !has(argv, "--bare") || has(argv, "--max-budget-usd") {
		t.Fatalf("api_key lane with no budget: %q", argv)
	}
	api.def.MaxBudgetUSD = 1.25
	argv, _ = buildArgv(api)
	if !hasPair(argv, "--max-budget-usd", "1.25") {
		t.Fatalf("api_key lane with a budget: %q", argv)
	}
	if has(argv, "--json-schema") {
		t.Fatal("--json-schema is never emitted")
	}
}

func TestBuildArgvRefusals(t *testing.T) {
	for name, mut := range map[string]func(*argvInput){
		"leading dash": func(in *argvInput) { in.prompt = "--version" },
		"empty prompt": func(in *argvInput) { in.prompt = "" },
		"ultracode":    func(in *argvInput) { in.def.Effort = "ultracode" },
	} {
		in := baseInput(provider.ToolPolicyNone)
		mut(&in)
		if _, err := buildArgv(in); err == nil {
			t.Fatalf("%s: buildArgv accepted it", name)
		}
	}
	argv, _ := buildArgv(baseInput(provider.ToolPolicyNone))
	if argv[len(argv)-2] != "--" || argv[len(argv)-1] != "do the thing" {
		t.Fatalf("the prompt must follow a literal --: %q", argv[len(argv)-2:])
	}
}

func TestClaudeProtocolNegotiationRefusal(t *testing.T) {
	f := newFx(t)
	d := f.driver(f.config())
	if r := d.SupportedProtocols(); r.Min != "1" || r.Max != "1" {
		t.Fatalf("SupportedProtocols = %+v, want {1,1}", r)
	}
	if _, err := d.Negotiate(context.Background(), provider.ProtocolRange{Min: "2", Max: "3"}); err != provider.ErrHarnessIncompatible {
		t.Fatalf("Negotiate(disjoint) = %v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"missing plugins key": func(m map[string]any) { delete(m, "plugins") },
		"null tools":          func(m map[string]any) { m["tools"] = nil },
		"version mismatch":    func(m map[string]any) { m["claude_code_version"] = "2.1.274" },
		"not semver":          func(m map[string]any) { m["claude_code_version"] = "two" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			f.initDefault = initLine(t, func(m map[string]any) { m["permissionMode"] = "default"; mutate(m) })
			_, err := f.driver(f.config()).Spawn(context.Background(), f.spec())
			if err != provider.ErrHarnessIncompatible || exists(f.jobRan) {
				t.Fatalf("Spawn = %v jobRan=%v, want the runtime ErrHarnessIncompatible and no job", err, exists(f.jobRan))
			}
		})
	}
	for name, mutate := range map[string]func(map[string]any){
		"result without total_cost_usd": func(m map[string]any) { delete(m, "total_cost_usd") },
		"result of another type":        func(m map[string]any) { m["type"] = "assistant" },
		"result without usage":          func(m map[string]any) { delete(m, "usage") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			f.body = "printf '%s\\n' '" + resultLine(t, mutate) + "'\n"
			_, _, err := run(t, f.driver(f.config()), f.spec())
			if !errors.Is(err, provider.ErrHarnessIncompatible) || !strings.Contains(err.Error(), "revision 1") {
				t.Fatalf("Collect = %v, want the revision-1 result refusal", err)
			}
		})
	}
}
