package agents

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/agents/codex"
)

type codexTestClock struct{}

func (codexTestClock) Now() time.Time { return time.Unix(1, 0) }

func TestCodexHarnessBinding(t *testing.T) {
	r, err := NewResolver(func(id string) bool { return id == "cascade-codex" })
	if err != nil {
		t.Fatal(err)
	}
	b, f, err := r.Resolve("agent:codex")
	if err != nil || f == nil {
		t.Fatalf("Resolve(agent:codex) = %v", err)
	}
	if b.Harness != HarnessCodex || b.PluginID != "cascade-codex" || b.ConfigDirVar != "CODEX_HOME" ||
		b.Runtime != RuntimeHarnessCLI || len(b.Binaries) != 1 || b.Binaries[0] != "codex" {
		t.Fatalf("binding = %+v", b)
	}
	if k, err := KeyVarFor(b, CredentialAPIKey); err != nil || k != "OPENAI_API_KEY" {
		t.Fatalf("api_key variable = %q, %v", k, err)
	}
	if _, _, err := (mustResolver(t, func(string) bool { return false })).Resolve("agent:codex"); err == nil {
		t.Fatal("resolved codex with its plugin absent")
	}
}

func mustResolver(t *testing.T, p PluginPresence) *Resolver {
	t.Helper()
	r, err := NewResolver(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCodexHarnessFactory(t *testing.T) {
	site := SpawnSite{Binary: "/usr/local/bin/codex", Defaults: provider.NewDriverDefaults(), VendorAuthVar: "CODEX_HOME"}
	p, err := newCodexDriver(site)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := p.(*codex.Driver)
	if !ok || d.Executable() != site.Binary {
		t.Fatalf("factory built %T", p)
	}
	site.Clock = codexTestClock{}
	site.CredentialClass, site.KeyVar = CredentialAPIKey, "OPENAI_API_KEY"
	if _, err := newCodexDriver(site); err != nil {
		t.Fatalf("api_key site: %v", err)
	}
	for _, bad := range []SpawnSite{
		{Binary: "/usr/local/bin/codex", VendorAuthVar: "HOME"},
		{Binary: "/usr/local/bin/codex", CredentialClass: CredentialAPIKey, KeyVar: "ANTHROPIC_API_KEY"},
		{Binary: "/usr/local/bin/codex", CredentialClass: CredentialOAuthToken, KeyVar: "OPENAI_API_KEY"},
		{Binary: "/usr/local/bin/codex-mcp-server"},
	} {
		if _, err := newCodexDriver(bad); err == nil {
			t.Errorf("site %+v was accepted", bad)
		}
	}
}
