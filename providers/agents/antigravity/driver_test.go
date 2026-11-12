package antigravity_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
	ag "github.com/acamarata/cascade/providers/agents/antigravity"
)

const fixtureHelp = "agy 1.4.12\n  --print\n  --output-format <format>  stream-json\n  --mode\n  --model\n  --effort\n  --print-timeout\n"

// TestMain provides a child fixture using the production command shape.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--help" {
		fmt.Print(fixtureHelp)
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "--print" {
		b, _ := json.Marshal(os.Environ())
		if os.WriteFile(filepath.Join(os.Getenv("HOME"), "environment.json"), b, 0600) != nil {
			os.Exit(2)
		}
		if os.Args[len(os.Args)-1] == "garbage" {
			fmt.Println("not a stream-json event")
			os.Exit(0)
		}
		fmt.Println(`{"event":"init","conversation_id":"fixture","init":{}}`)
		if os.Args[len(os.Args)-1] == "hold" {
			for {
				if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), "release")); err == nil {
					break
				}
				time.Sleep(time.Millisecond)
			}
		}
		fmt.Println(`{"event":"result","result":{"conversation_id":"fixture","status":"success","response":"ok"}}`)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func kit(t *testing.T) (*ag.Driver, provider.AgentJobSpec) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	d, err := ag.New(ag.Config{Binary: bin, RuntimeHome: home, Clock: fixedClock{now},
		Posture: provider.CompliancePosture{ProgrammaticEntitlement: true}})
	if err != nil {
		t.Fatal(err)
	}
	spec := provider.AgentJobSpec{Prompt: "run", JobID: "fixture-job", WorktreeRoot: home,
		Env:      provider.SpawnEnv{Home: home, Driver: []string{"HOME=" + home, "LANG=C"}},
		Filter:   func(b []byte) ([]byte, error) { return b, nil },
		Envelope: provider.SpawnEnvelope{Digest: strings.Repeat("ab", 32), Principal: "fixture", Roots: []string{home}, ExpiresAt: now.Add(time.Minute)}}
	return d, spec
}

func TestAntigravityAbsentRefusesEveryMethod(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	changed := filepath.Join(home, "changed-agy")
	if err := os.WriteFile(changed, []byte("#!/bin/sh\nprintf 'agy 1.4.12\\n  --print\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, bin := range []string{filepath.Join(home, "missing-agy"), changed} {
		d, err := ag.New(ag.Config{Binary: bin, RuntimeHome: home})
		if err != nil {
			t.Fatal(err)
		}
		assertRefused(t, d)
	}
}

func assertRefused(t *testing.T, d *ag.Driver) {
	t.Helper()
	ctx := context.Background()
	r, err := d.Spawn(ctx, provider.AgentJobSpec{})
	sentinel(t, err, ag.ErrInterfaceAbsent)
	if r.ProcessGroupID != 0 || r.JobID != "" {
		t.Fatalf("refused result: %+v", r)
	}
	_, err = d.Capabilities(ctx, "")
	sentinel(t, err, ag.ErrInterfaceAbsent)
	_, err = d.Negotiate(ctx, provider.ProtocolRange{})
	sentinel(t, err, ag.ErrInterfaceAbsent)
	_, err = d.Chat(ctx, provider.ChatRequest{})
	sentinel(t, err, ag.ErrInterfaceAbsent)
	_, err = d.Embed(ctx, provider.ModelEmbedRequest{})
	sentinel(t, err, ag.ErrInterfaceAbsent)
	_, err = d.Count(ctx, provider.CountRequest{})
	sentinel(t, err, ag.ErrInterfaceAbsent)
	sentinel(t, d.Stream(ctx, provider.ChatRequest{}, nil), ag.ErrInterfaceAbsent)
	sentinel(t, d.ResolveApproval("", ""), ag.ErrInterfaceAbsent)
	sentinel(t, d.Message(ctx, "", ""), ag.ErrInterfaceAbsent)
	_, err = d.Status(ctx, "")
	sentinel(t, err, ag.ErrInterfaceAbsent)
	sentinel(t, d.Cancel(ctx, ""), ag.ErrInterfaceAbsent)
	_, err = d.Collect(ctx, "")
	sentinel(t, err, ag.ErrInterfaceAbsent)
	_, err = d.Artifacts(ctx, "")
	sentinel(t, err, ag.ErrInterfaceAbsent)
	if d.SupportedProtocols() != (provider.ProtocolRange{}) {
		t.Fatal("refused protocol")
	}
	_ = d.ApprovalRequests()
	_ = d.Executable()
}

func TestAntigravityEnvAllowlist(t *testing.T) {
	d, spec := kit(t)
	t.Setenv("UNLISTED_PARENT_MARKER", "parent-only")
	r, err := d.Spawn(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Collect(context.Background(), r.JobID); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(spec.Env.Home, "environment.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := slices.Clone(spec.Env.Driver)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("child env = %q, want %q", got, want)
	}
	for _, extra := range []string{"UNLISTED_PARENT_MARKER=x", "ALL_PROXY=x", "NOEQUALS", "HOME=duplicate"} {
		bad := spec
		bad.Env.Driver = append(slices.Clone(spec.Env.Driver), extra)
		_, err := d.Spawn(context.Background(), bad)
		sentinel(t, err, ag.ErrEnvNotAllowed)
	}
	spec.Env.Home = "different"
	_, err = d.Spawn(context.Background(), spec)
	sentinel(t, err, ag.ErrHomeMismatch)
}

func TestAntigravityRefusesEmptyWorktreeRoot(t *testing.T) {
	d, spec := kit(t)
	spec.WorktreeRoot = ""
	r, err := d.Spawn(context.Background(), spec)
	sentinel(t, err, ag.ErrEmptyWorktreeRoot)
	if r.ProcessGroupID != 0 {
		t.Fatal("refusal started a child")
	}
}

func TestAntigravityNilFilterRefuses(t *testing.T) {
	d, spec := kit(t)
	spec.Filter = nil
	r, err := d.Spawn(context.Background(), spec)
	sentinel(t, err, provider.ErrEgressSubstitutionFailed)
	if r.ProcessGroupID != 0 {
		t.Fatal("refusal started a child")
	}
}

func TestAntigravityEntitlementFalseRefuses(t *testing.T) {
	d, spec := kit(t)
	d, err := ag.New(ag.Config{Binary: d.Executable(), RuntimeHome: spec.Env.Home})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Spawn(context.Background(), spec)
	sentinel(t, err, provider.ErrEntitlement)
}

func TestAntigravityUsesSpecJobID(t *testing.T) {
	d, spec := kit(t)
	spec.Prompt = "hold"
	r, err := d.Spawn(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Cancel(context.Background(), r.JobID) })
	if r.JobID != spec.JobID || r.ProcessGroupID <= 0 {
		t.Fatalf("Spawn = %+v", r)
	}
	_, err = d.Spawn(context.Background(), spec)
	sentinel(t, err, ag.ErrDuplicateJob)
	if err := os.WriteFile(filepath.Join(spec.Env.Home, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Collect(context.Background(), r.JobID); err != nil {
		t.Fatal(err)
	}
	r, err = d.Spawn(context.Background(), spec)
	if err != nil || r.JobID != spec.JobID {
		t.Fatalf("reuse = %+v, %v", r, err)
	}
	if _, err := d.Collect(context.Background(), r.JobID); err != nil {
		t.Fatal(err)
	}
}

func TestAntigravityExecutableReported(t *testing.T) {
	d, _ := kit(t)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var reporter provider.ExecutableReporter = d
	if reporter.Executable() != bin {
		t.Fatalf("Executable = %q", reporter.Executable())
	}
}

func TestAntigravityMessageUnsupported(t *testing.T) {
	d, spec := kit(t)
	r, err := d.Spawn(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	sentinel(t, d.Message(context.Background(), r.JobID, "more"), provider.ErrMessageUnsupported)
	sentinel(t, d.Message(context.Background(), "missing", "more"), provider.ErrJobNotFound)
	if _, err := d.Collect(context.Background(), r.JobID); err != nil {
		t.Fatal(err)
	}
}

var buildHome = os.Getenv("HOME")

func TestAntigravityDriverWiring(t *testing.T) {
	_, spec := kit(t)
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(spec.Env.Home, "wiring_test.go")
	if err := os.WriteFile(source, []byte(registryTest), 0600); err != nil {
		t.Fatal(err)
	}
	virtual := filepath.Join(root, "internal/providers/agents/antigravity_ticket_wiring_test.go")
	overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{virtual: source}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(spec.Env.Home, "overlay.json")
	if err := os.WriteFile(path, overlay, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", "-overlay", path, "-run", "^TestAntigravityRegistrySeam$", "./internal/providers/agents")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOPATH="+filepath.Join(buildHome, "go"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("registry wiring: %v\n%s", err, output)
	}
}

// The overlay exercises the real host registry without adding internal imports
// to this package or writing a source file outside the test's temporary home.
const registryTest = `package agents_test
import (
 "path/filepath"; "testing"
 "github.com/acamarata/cascade/internal/providers/agents"; "github.com/acamarata/cascade/pkg/provider"; "github.com/acamarata/cascade/providers/agents/antigravity"
)
func TestAntigravityRegistrySeam(t *testing.T) {
 home := t.TempDir(); t.Setenv("HOME", home); t.Setenv("USERPROFILE", home)
 agents.RegisterHarness(agents.Binding{Harness:"antigravity", PluginID:"antigravity",
 Runtime:agents.RuntimeHarnessCLI, LaneID:"agent:antigravity", ConfigDirVar:"ANTIGRAVITY_HOME",
 Binaries:[]string{"agy"}, Credentials:[]agents.CredentialSpec{{Class:agents.CredentialSubscription}}},
 func(site agents.SpawnSite) (provider.AgentProvider,error) {
 return antigravity.New(antigravity.Config{Binary:site.Binary,RuntimeHome:site.RuntimeHome})
 })
 resolver,err:=agents.NewResolver(func(id string)bool{return id=="antigravity"}); if err!=nil {t.Fatal(err)}
 _,factory,err:=resolver.Resolve("agent:antigravity"); if err!=nil {t.Fatal(err)}
 binary:=filepath.Join(home,"agy"); p,err:=factory(agents.SpawnSite{Binary:binary,RuntimeHome:home}); if err!=nil {t.Fatal(err)}
 d,ok:=p.(*antigravity.Driver)
 if !ok || d.Executable()!=binary {t.Fatalf("factory returned %T, executable mismatch",p)}
}
`
