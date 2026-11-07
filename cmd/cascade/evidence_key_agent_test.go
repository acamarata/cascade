// Purpose: proves the agent leg of newEvidenceKeyFn: an "agent:<harness>"
// account keys on the lane id, owning plugin, vendor executable and
// credential class, never reads the registry, reads a lane that no longer
// resolves as gone and an uncomposed resolver as unavailable; and that
// newEvidenceKeyFn returns topology.EvidenceKeyFn itself, with no mirror type.
// Inputs: temp HOME, a fake agentLanes, a fake driver, a counting registry.
// Outputs: none.
// Constraints: untagged, no network, no child process, no vault.
// SPORT: cmd/cascade billing evidence (NEW, P1-TOP-19c).

package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/agents"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// evkAgentDriver is a harness driver that only reports its executable.
type evkAgentDriver struct {
	provider.AgentProvider
	exe string
}

func (d evkAgentDriver) Executable() string { return d.exe }

// evkLanes is a settable fake agentLanes.
type evkLanes struct {
	plugin, exe string
	err         error
}

func (l *evkLanes) Resolve(string) (agents.Binding, agents.DriverFactory, error) {
	if l.err != nil {
		return agents.Binding{}, nil, l.err
	}
	exe := l.exe
	factory := func(agents.SpawnSite) (provider.AgentProvider, error) { return evkAgentDriver{exe: exe}, nil }
	return agents.Binding{Harness: agents.HarnessClaude, PluginID: l.plugin}, factory, nil
}

// evkNoRegistry fails the test on any registry read.
type evkNoRegistry struct{ t *testing.T }

func (r evkNoRegistry) GetProvider(_ context.Context, name string) (providerregistry.ProviderRecord, error) {
	r.t.Errorf("GetProvider(%q) called for an agent id", name)
	return providerregistry.ProviderRecord{}, cascade.New(cascade.KindNotFound, "test: no registry")
}

func evkAgentFn(t *testing.T, lanes agentLanes, class string) topology.EvidenceKeyFn {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("CASCADE_HOME", dir)
	shim := agents.ShimDeps{Auth: map[agents.HarnessID]agents.DriverAuth{}}
	if class != "" {
		shim.Auth[agents.HarnessClaude] = agents.DriverAuth{Harness: agents.HarnessClaude, Class: class}
	}
	fn, err := newEvidenceKeyFn(evidenceKeyDeps{Registry: evkNoRegistry{t}, Credentials: &evkCreds{},
		Agents: lanes, Shim: shim, KeyPath: filepath.Join(dir, "evidence.key")})
	if err != nil {
		t.Fatalf("newEvidenceKeyFn: %v", err)
	}
	return fn
}

func TestEvidenceKeyFnAgentLeg(t *testing.T) {
	lanes := &evkLanes{plugin: "cascade-claude", exe: "/opt/x/claude"}
	fn := evkAgentFn(t, lanes, "subscription")
	base := evkKey(t, fn, "agent:claude")
	if base.Provider != "agent:claude" || base.InstanceID != "agent:claude@cascade-claude" ||
		base.Endpoint != "" || base.AuthRef != "" || len(base.CredentialFingerprint) != 32 {
		t.Fatalf("agent key shape %+v", base)
	}
	if again := evkKey(t, fn, "agent:claude"); again != base {
		t.Fatalf("unchanged lane: %+v != %+v", again, base)
	}
	lanes.exe = "/opt/y/claude"
	if k := evkKey(t, fn, "agent:claude"); k.CredentialFingerprint == base.CredentialFingerprint {
		t.Fatal("Executable change kept the fingerprint")
	}
	lanes.exe, lanes.plugin = "/opt/x/claude", "cascade-claude-2"
	if k := evkKey(t, fn, "agent:claude"); k.CredentialFingerprint == base.CredentialFingerprint {
		t.Fatal("PluginID change kept the fingerprint")
	}
	lanes.plugin = "cascade-claude"
	if k := evkKey(t, fn, "agent:claude"); k != base {
		t.Fatalf("restored lane: %+v != %+v", k, base)
	}
	t.Run("class", func(t *testing.T) { evkAgentClassLeg(t, lanes) })
	lanes.err = cascade.New(cascade.KindUnavailable, "test: plugin absent")
	_, err := fn(context.Background(), "agent:claude")
	evkWantErr(t, err, cascade.KindNotFound, `agent lane "agent:claude" no longer resolves`)
	_, err = evkAgentFn(t, nil, "subscription")(context.Background(), "agent:claude")
	evkWantErr(t, err, cascade.KindUnavailable, "evidence key: agent lanes are not composed")
}

// evkAgentClassLeg proves the credential class is in the fingerprint and a
// harness with no Auth entry fingerprints the class "none". Each fn has its
// own key file, so keys are compared within one install key only.
func evkAgentClassLeg(t *testing.T, lanes *evkLanes) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "evidence.key")
	keyFor := func(class string) topology.EvidenceKey {
		shim := agents.ShimDeps{Auth: map[agents.HarnessID]agents.DriverAuth{}}
		if class != "" {
			shim.Auth[agents.HarnessClaude] = agents.DriverAuth{Harness: agents.HarnessClaude, Class: class}
		}
		fn, err := newEvidenceKeyFn(evidenceKeyDeps{Registry: evkNoRegistry{t}, Credentials: &evkCreds{},
			Agents: lanes, Shim: shim, KeyPath: keyPath})
		if err != nil {
			t.Fatalf("newEvidenceKeyFn: %v", err)
		}
		return evkKey(t, fn, "agent:claude")
	}
	sub, tok, none := keyFor("subscription"), keyFor("oauth_token"), keyFor("")
	if sub.CredentialFingerprint == tok.CredentialFingerprint || sub.CredentialFingerprint == none.CredentialFingerprint ||
		tok.CredentialFingerprint == none.CredentialFingerprint {
		t.Fatalf("class change kept the fingerprint: %s %s %s", sub.CredentialFingerprint, tok.CredentialFingerprint, none.CredentialFingerprint)
	}
	if again := keyFor("subscription"); again != sub {
		t.Fatalf("same class, same install key: %+v != %+v", again, sub)
	}
}

func TestEvidenceKeyFnReturnsTopologyType(t *testing.T) {
	var fn topology.EvidenceKeyFn
	var err error
	fn, err = newEvidenceKeyFn(evidenceKeyDeps{Registry: evkNoRegistry{t}, Credentials: &evkCreds{},
		KeyPath: filepath.Join(t.TempDir(), "evidence.key")})
	if err != nil || fn == nil {
		t.Fatalf("newEvidenceKeyFn: %v", err)
	}
	_, err = fn(context.Background(), "")
	evkWantErr(t, err, cascade.KindInvalidInput, "account id is required")
	fset := token.NewFileSet()
	for _, name := range []string{"evidence_key.go", "evidence_key_agent.go"} {
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			st, ok := n.(*ast.StructType)
			if !ok {
				return true
			}
			for _, f := range st.Fields.List {
				for _, id := range f.Names {
					if id.Name == "CredentialFingerprint" {
						t.Errorf("%s declares a struct with a CredentialFingerprint field (a mirror type)", fset.Position(st.Pos()))
					}
				}
			}
			return true
		})
	}
}
