package agents

import (
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/agents/antigravity"
)

func TestAntigravityHarnessBinding(t *testing.T) {
	isolateHome(t)
	var got Binding
	for _, b := range Bindings() {
		if b.Harness == HarnessAntigravity {
			got = b
		}
	}
	want := antigravityRow
	if got.Harness != want.Harness || got.PluginID != "antigravity" || got.Runtime != RuntimeHarnessCLI ||
		got.LaneID != "agent:antigravity" || got.ConfigDirVar != want.ConfigDirVar ||
		!slices.Equal(got.Binaries, []string{"agy"}) ||
		len(got.Credentials) != 1 || !sameCred(got.Credentials[0], CredentialSpec{Class: CredentialSubscription}) {
		t.Fatalf("registered row = %+v, want %+v", got, want)
	}
	b, f, err := mustResolver(t, func(id string) bool { return id == "antigravity" }).Resolve("agent:antigravity")
	if err != nil || f == nil || b.Harness != HarnessAntigravity {
		t.Fatalf("Resolve(agent:antigravity) = %+v, %v", b, err)
	}
}

func TestAntigravityHarnessFactory(t *testing.T) {
	isolateHome(t)
	site := SpawnSite{Binary: "/nonexistent/agy", Defaults: provider.NewDriverDefaults(), RuntimeHome: t.TempDir()}
	p, err := newAntigravityDriver(site)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := p.(*antigravity.Driver)
	if !ok || d.Executable() != site.Binary {
		t.Fatalf("factory built %T", p)
	}
	site.VendorAuthVar = "OTHER_HOME"
	_, err = newAntigravityDriver(site)
	want := checkSite(antigravityRow, site)
	if err == nil || want == nil || err.Error() != want.Error() || !strings.Contains(err.Error(), "not the config directory variable") {
		t.Fatalf("refusal = %v, want %v", err, want)
	}
}

func TestAntigravityBindingsSortOrder(t *testing.T) {
	var ids []string
	for _, b := range Bindings() {
		ids = append(ids, string(b.Harness))
	}
	if !slices.IsSorted(ids) {
		t.Fatalf("Bindings() order %v is not sorted by harness id", ids)
	}
	a, c := slices.Index(ids, "antigravity"), slices.Index(ids, "claude")
	if a < 0 || c < 0 || a >= c {
		t.Fatalf("antigravity at %d, claude at %d in %v", a, c, ids)
	}
}
