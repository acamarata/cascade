package agents

import (
	"github.com/acamarata/cascade/providers/agents/opencode"
	"testing"
)

func TestOpenCodeHarnessBinding(t *testing.T) {
	r, err := NewResolver(func(id string) bool { return id == "cascade-opencode" })
	if err != nil {
		t.Fatal(err)
	}
	b, f, err := r.Resolve("agent:opencode")
	if err != nil || f == nil {
		t.Fatal(err)
	}
	if b.ConfigDirVar != opencode.VendorAuthVar || b.PluginID != "cascade-opencode" || b.Runtime != RuntimeHarnessCLI {
		t.Fatalf("%+v", b)
	}
	k, err := KeyVarFor(b, CredentialCompatKey)
	if err != nil || k != opencode.KeyVarCompatKey {
		t.Fatal(k, err)
	}
	p, err := f(SpawnSite{Binary: "/fixture/opencode"})
	if err != nil {
		t.Fatal(err)
	}
	if p.(*opencode.Driver).Executable() != "/fixture/opencode" {
		t.Fatal("wrong binary")
	}
	if _, err := f(SpawnSite{Binary: "/fixture/opencode", VendorAuthVar: "HOME"}); err == nil {
		t.Fatal("accepted bad site")
	}
}
