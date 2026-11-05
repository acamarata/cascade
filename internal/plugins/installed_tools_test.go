//go:build !windows

// Purpose: InstalledToolSource offers cascade-github's tools only for an
// enabled, trusted record whose executable still matches its pinned digest
// and whose stored effective grants equal the embedded manifest's
// requires; a call re-checks all of that and refuses typed; a launch
// refusal is a typed tool error. Trust comes only from the witnessed
// plugin.add (launchFix.grant). The executable is a marker script that
// leaves <path>.ran if it is ever run.
// SPORT: internal/plugins installed process tools tests (P1-PLG-06).
package plugins

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// toolsFix is a launch fixture whose cascade-github executable is a marker.
type toolsFix struct {
	*launchFix
	exe string
}

func newToolsFix(t *testing.T) toolsFix {
	t.Helper()
	f := newLaunchFix(t)
	return toolsFix{launchFix: f, exe: f.installMarker(t, cascadeGitHubPluginID)}
}

func (f toolsFix) grantGitHub(t *testing.T) {
	t.Helper()
	f.grant(t, cascadeGitHubPluginID, string(githubManifestBytes))
}

func (f toolsFix) registry() *mcp.ToolRegistry {
	return mcp.NewToolRegistryWithProcess(func() []plugin.BuiltinRegistration { return nil }, InstalledToolSource(), nil)
}

// shippedToolNames is every tool the embedded manifest declares, sorted.
func shippedToolNames(t *testing.T) []string {
	t.Helper()
	fps := FirstPartyManifests()
	if len(fps) != 1 {
		t.Fatalf("FirstPartyManifests = %d entries, want 1", len(fps))
	}
	var out []string
	for _, tool := range fps[0].Manifest.Provides.Tools {
		out = append(out, tool.Name)
	}
	sort.Strings(out)
	return out
}

func (f toolsFix) requireTools(t *testing.T, want []string) {
	t.Helper()
	var got []string
	for _, tool := range f.registry().List() {
		got = append(got, tool.Name)
	}
	sort.Strings(got)
	if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
		t.Fatalf("listed %v, want %v", got, want)
	}
}

func (f toolsFix) editRecord(t *testing.T, edit func(*PluginMetadata)) {
	t.Helper()
	rec, ok, err := LoadMetadata(context.Background(), f.store, cascadeGitHubPluginID)
	if err != nil || !ok {
		t.Fatalf("loading the record: ok=%v err=%v", ok, err)
	}
	edit(&rec)
	if err := SaveMetadata(context.Background(), f.store, rec); err != nil {
		t.Fatal(err)
	}
}

func dropGrant(grants []string, drop string) []string {
	var out []string
	for _, g := range grants {
		if g != drop {
			out = append(out, g)
		}
	}
	return out
}

func TestFirstPartyManifestMatchesShipped(t *testing.T) {
	shipped, err := os.ReadFile("../../plugins/github/manifest.toml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(shipped, githubManifestBytes) {
		t.Fatal("firstparty/github.manifest.toml is not a byte-identical copy of plugins/github/manifest.toml")
	}
	fps := FirstPartyManifests()
	if len(fps) != 1 || fps[0].Manifest.ID != cascadeGitHubPluginID || !bytes.Equal(fps[0].Raw, shipped) ||
		len(fps[0].Manifest.Provides.Tools) == 0 || len(fps[0].Manifest.Requires) == 0 {
		t.Fatalf("FirstPartyManifests = %+v, want the parsed cascade-github manifest", fps)
	}
	defer func(saved [][]byte) { firstPartyManifestBytes = saved }(firstPartyManifestBytes)
	firstPartyManifestBytes = [][]byte{[]byte("not = [toml"), []byte(`id = "x"`)}
	if got := FirstPartyManifests(); len(got) != 0 {
		t.Fatalf("unparseable or invalid manifests contributed %+v", got)
	}
}

func TestInstalledProcessToolsAbsentWhenUntrusted(t *testing.T) {
	f := newToolsFix(t)
	f.requireTools(t, nil)
	m := FirstPartyManifests()[0].Manifest
	untrusted := PluginMetadata{Name: m.ID, InstalledVersion: m.Version, Enabled: true, RuntimeMode: m.Runtime,
		Grants: m.Requires, TrustGrants: m.Requires}
	if err := SaveMetadata(context.Background(), f.store, untrusted); err != nil {
		t.Fatal(err)
	}
	f.requireTools(t, nil)
	f.grantGitHub(t)
	f.requireTools(t, shippedToolNames(t))
	defer SetLaunchEnvSource(nil)()
	f.requireTools(t, nil)
}

func TestInstalledProcessToolsAbsentWhenDisabled(t *testing.T) {
	f := newToolsFix(t)
	f.grantGitHub(t)
	f.requireTools(t, shippedToolNames(t))
	if _, err := SetEnabled(context.Background(), f.store, cascadeGitHubPluginID, false); err != nil {
		t.Fatal(err)
	}
	f.requireTools(t, nil)
}

func TestInstalledProcessToolsAbsentOnDigestMismatch(t *testing.T) {
	f := newToolsFix(t)
	f.grantGitHub(t)
	f.requireTools(t, shippedToolNames(t))
	appendByte(t, f.exe)
	f.requireTools(t, nil)
	if err := os.Remove(f.exe); err != nil {
		t.Fatal(err)
	}
	f.requireTools(t, nil)
}

func TestInstalledProcessToolsUseStoredGrants(t *testing.T) {
	f := newToolsFix(t)
	f.grantGitHub(t)
	regs := InstalledToolSource()()
	rec, _, _ := LoadMetadata(context.Background(), f.store, cascadeGitHubPluginID)
	if len(regs) != 1 || !reflect.DeepEqual(regs[0].Grants, EffectiveGrants(rec)) {
		t.Fatalf("registrations = %+v, want one carrying EffectiveGrants %v", regs, EffectiveGrants(rec))
	}
	f.editRecord(t, func(r *PluginMetadata) {
		r.Grants = []string{"oauth:repo", "host.secret_ref", "net.http:github.com", "net.http:api.github.com"}
	})
	f.requireTools(t, shippedToolNames(t))
	f.editRecord(t, func(r *PluginMetadata) { r.Grants = dropGrant(r.Grants, "oauth:repo") })
	f.requireTools(t, nil)
	f.editRecord(t, func(r *PluginMetadata) {
		r.Grants = append(r.Grants, "oauth:repo")
		r.TrustGrants = dropGrant(r.TrustGrants, "net.http:github.com")
	})
	f.requireTools(t, nil)
}

func TestProcessToolCallRechecksExposure(t *testing.T) {
	f := newToolsFix(t)
	f.grantGitHub(t)
	r := f.registry()
	if len(r.List()) == 0 {
		t.Fatal("tools/list listed nothing after a witnessed add")
	}
	before := len(f.allRecords(t))
	f.editRecord(t, func(rec *PluginMetadata) { rec.Grants = dropGrant(rec.Grants, "oauth:repo") })
	_, err := r.Call(context.Background(), "cascade-github.repos.get", []byte(`{}`))
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("call after a revoke = %v, want KindPolicyDenied", err)
	}
	if _, serr := os.Stat(f.exe + ".ran"); !errors.Is(serr, os.ErrNotExist) {
		t.Fatal("a call refused by the re-check launched the plugin")
	}
	if after := len(f.allRecords(t)); after != before {
		t.Fatalf("a refused call wrote %d effect records", after-before)
	}
	f.editRecord(t, func(rec *PluginMetadata) { rec.Grants = append(rec.Grants, "oauth:repo") })
	SetLaunchEnvSource(nil)
	if _, err := r.Call(context.Background(), "cascade-github.repos.get", nil); !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("call with no launch environment = %v, want KindPolicyDenied", err)
	}
}

func TestInstalledProcessToolLaunchRefusalIsTypedToolError(t *testing.T) {
	f := newToolsFix(t)
	f.grantGitHub(t)
	r := f.registry()
	restore := SetLaunchEnvSource(func(ctx context.Context) (LaunchEnv, error) {
		env, err := f.source(ctx)
		env.Effects = failingEffects{EffectWriter: f.effects, begin: true}
		return env, err
	})
	defer restore()
	out, err := r.Call(context.Background(), "cascade-github.repos.get", []byte(`{}`))
	if out != nil || !cascade.HasKind(err, cascade.KindUnavailable) || !refusedByInjection(err) {
		t.Fatalf("call whose launch was refused = %q, %v; want a KindUnavailable tool error", out, err)
	}
	if _, serr := os.Stat(f.exe + ".ran"); !errors.Is(serr, os.ErrNotExist) {
		t.Fatal("a refused launch ran the plugin")
	}
}

func TestUntrustedResultShapes(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":1}`: `{"result":{"a":1},"_meta":{"cascade/provenance":"untrusted-source"}}`,
		`plain`:   `{"result":"plain","_meta":{"cascade/provenance":"untrusted-source"}}`,
		``:        `{"_meta":{"cascade/provenance":"untrusted-source"}}`,
	} {
		if got, err := untrustedResult([]byte(in), nil); err != nil || string(got) != want {
			t.Fatalf("untrustedResult(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
	got, _ := untrustedResult(nil, cascade.New(cascade.KindInternal, "reply"))
	if string(got) != `{"error":"internal: reply","_meta":{"cascade/provenance":"untrusted-source"}}` {
		t.Fatalf("error envelope = %s", got)
	}
}
