// Purpose: hook vault bindings and the bound rehydration seam. Bindings
//
//	are collected from plugin-call params only; BoundRehydrate, behind the
//	real secrets.Rehydrator.RehydrateOnly over a counting in-memory vault,
//	resolves an admitted binding and refuses everything else before any
//	vault read. Every vault value here is a generated fake.
//
// Inputs: HookConfig fixtures, the real egress engine (testEngine). HOME,
//
//	USERPROFILE and CASCADE_HOME point into t.TempDir(); custody is the
//	in-memory vault below, never a keychain.
package hooks

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeValue is a generated stand-in for a vault value; no real secret.
func fakeValue(tag string) string { return "fake-" + tag + "-config-value" }

// countingVault is a secrets.Custody over a map that records every Get.
type countingVault struct {
	entries map[string][]byte
	reads   []string
}

func (v *countingVault) Name() string                              { return "counting" }
func (v *countingVault) Available() bool                           { return true }
func (v *countingVault) Set(context.Context, string, []byte) error { return errors.New("read-only") }
func (v *countingVault) Delete(context.Context, string) error      { return errors.New("read-only fixture") }
func (v *countingVault) List(context.Context) ([]string, error) {
	return slices.Sorted(maps.Keys(v.entries)), nil
}
func (v *countingVault) Get(_ context.Context, name string) ([]byte, error) {
	v.reads = append(v.reads, name)
	value, ok := v.entries[name]
	if !ok {
		return nil, secrets.ErrSecretNotFound(name)
	}
	return append([]byte(nil), value...), nil
}

type refuseGate struct{}

func (refuseGate) Authorize(context.Context, string) error { return errors.New("no elevation") }

// realHome is the home directory before any test redirects it.
var realHome, _ = os.UserHomeDir()

// requireIsolatedHome fails a test whose home still resolves to the real one.
func requireIsolatedHome(t *testing.T) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || realHome == "" || home == realHome || strings.HasPrefix(home, realHome+string(filepath.Separator)) {
		t.Fatalf("test home %q is not isolated from the real home (err %v)", home, err)
	}
}

// boundRig is BoundRehydrate's production shape over the real rehydrator:
// allowName is RehydrateOnly's predicate, so the binding check runs on the
// rehydrator's own spans. asked counts the names it was asked about.
type boundRig struct {
	vault *countingVault
	bytes RehydrateBytes
	asked []string
	plain []byte
}

func newBoundRig(t *testing.T, entries map[string]string) *boundRig {
	t.Helper()
	home := t.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(k, home)
	}
	requireIsolatedHome(t)
	rig := &boundRig{vault: &countingVault{entries: map[string][]byte{}}}
	for name, value := range entries {
		rig.vault.entries[name] = []byte(value)
	}
	b, err := secrets.NewBroker(rig.vault, refuseGate{})
	r, rerr := secrets.NewRehydrator(b)
	if err != nil || rerr != nil {
		t.Fatal(err, rerr)
	}
	rig.bytes = func(ctx context.Context, tagged []byte, allowName func(string) bool) ([]byte, func(), error) {
		rc, err := r.RehydrateOnly(ctx, tagged, func(tag secrets.Tag) bool {
			rig.asked = append(rig.asked, tag.Name)
			return allowName(tag.Name)
		})
		if err != nil {
			return nil, nil, err
		}
		rig.plain = rc.Data
		return rc.Data, rc.Zero, nil
	}
	return rig
}

func admittedOf(b ...VaultBinding) func() ([]VaultBinding, error) {
	return func() ([]VaultBinding, error) { return b, nil }
}

func pluginFire(id string) Fire {
	return Fire{Hook: HookConfig{ID: id, Namespace: "jobs", Trigger: "k", ActionType: ActionTypePluginCall}}
}

// tagNames is the production tag reader's shape: the rehydrate scanner's
// names, refusing a pii tag.
func tagNames(content []byte) ([]string, error) {
	tags, err := secrets.RehydrationTagNames(content)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, tag := range tags {
		if tag.Type == secrets.TagPII {
			return nil, cascade.New(cascade.KindInvalidInput, "fixture: pii tag")
		}
		names = append(names, tag.Name)
	}
	return names, nil
}

func plugin(id string, params map[string]string) HookConfig {
	return HookConfig{ID: id, ActionType: ActionTypePluginCall, ActionParams: params}
}

func TestVaultBindingsOfCollectsPluginCallTags(t *testing.T) {
	hooks := []HookConfig{
		plugin("h2", map[string]string{"plugin": "q", "dsn": "<password>DB_ENTRY</password>"}),
		plugin("h1", map[string]string{"plugin": "p", "tool": "send", "hdr": "pre <token>T_ONE</token>",
			"k2": "<apikey>K_ONE</apikey>,<apikey>K_ONE</apikey>"}),
		plugin("plain", map[string]string{"plugin": "p", "x": "no tag"}),
		{ID: "note", ActionType: ActionTypeAgentNote, ActionParams: map[string]string{"note": "untagged"}},
	}
	got, err := VaultBindingsOf(hooks, tagNames)
	want := []VaultBinding{{"h1", "p", "K_ONE"}, {"h1", "p", "T_ONE"}, {"h2", "q", "DB_ENTRY"}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("bindings %v (err %v), want %v", got, err, want)
	}
	if got, err := VaultBindingsOf(nil, tagNames); err != nil || len(got) != 0 {
		t.Fatalf("no hooks: %v %v", got, err)
	}
}

func TestVaultBindingsOfRefusesNoteTagAndPII(t *testing.T) {
	ok := plugin("ok", map[string]string{"plugin": "x", "k": "<apikey>K</apikey>"})
	cases := map[string]struct {
		hook HookConfig
		want string
	}{
		"note tag": {HookConfig{ID: "n", ActionType: ActionTypeAgentNote, ActionParams: map[string]string{"note": "<apikey>K</apikey>"}},
			`invalid-input: hooks: hook "n" param "note" carries a vault tag; only a plugin-call hook binds a vault name`},
		"shell tag": {HookConfig{ID: "s", ActionType: ActionTypeShell, ActionParams: map[string]string{"command": "<token>K</token>"}},
			`invalid-input: hooks: hook "s" param "command" carries a vault tag; only a plugin-call hook binds a vault name`},
		"pii": {plugin("p", map[string]string{"plugin": "x", "ssn": `<pii kind="ssn">SSN</pii>`}),
			`invalid-input: hooks: hook "p" param "ssn" carries a malformed or pii vault tag`},
		"tagged plugin": {plugin("t", map[string]string{"plugin": "<apikey>K</apikey>"}),
			`invalid-input: hooks: hook "t" param "plugin" names its plugin with a vault tag`},
		"no plugin": {plugin("u", map[string]string{"k": "<apikey>K</apikey>"}),
			`invalid-input: hooks: hook "u" param "k" carries a vault tag and names no plugin`},
		"tag in a name": {plugin("v", map[string]string{"plugin": "x", "<apikey>K</apikey>": "v"}),
			`invalid-input: hooks: hook "v" has a param name carrying a vault tag`},
		"pasted value": {plugin("w", map[string]string{"plugin": "x", "k": "<apikey>pasted-fake-value</apikey>"}),
			`invalid-input: hooks: hook "w" param "k" carries a malformed or pii vault tag`},
	}
	for name, tc := range cases {
		got, err := VaultBindingsOf([]HookConfig{ok, tc.hook}, tagNames)
		if got != nil || !cascade.HasKind(err, cascade.KindInvalidInput) || err.Error() != tc.want {
			t.Fatalf("%s: %v, err %v, want %s", name, got, err, tc.want)
		}
	}
	split := plugin("sp", map[string]string{"plugin": "x", "a": "<apikey>K", "b": "</apikey>"})
	if got, err := VaultBindingsOf([]HookConfig{split}, tagNames); got != nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("a tag split across two params: %v, err %v, want a refusal", got, err)
	}
	if got, err := VaultBindingsOf([]HookConfig{ok}, nil); got != nil || err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil tag reader: %v, err %v, want a refusal", got, err)
	}
}

const wantTagRefusal = "permission-denied: secrets: tag not permitted at this boundary"

func TestBoundRehydrateRefusesUnboundName(t *testing.T) {
	rig := newBoundRig(t, map[string]string{"VAULT_X": fakeValue("x"), "VAULT_Y": fakeValue("y")})
	seam := BoundRehydrate(admittedOf(VaultBinding{"h", "p1", "VAULT_X"}, VaultBinding{"g", "p1", "VAULT_Y"}), rig.bytes)
	cases := map[string]struct {
		hook   string
		tagged map[string]string
	}{
		"unbound name": {"h", map[string]string{"plugin": "p1", "hdr": "<apikey>VAULT_Y</apikey>"}},
		"other plugin": {"h", map[string]string{"plugin": "p2", "hdr": "<apikey>VAULT_X</apikey>"}},
		"other hook":   {"g", map[string]string{"plugin": "p1", "hdr": "<apikey>VAULT_X</apikey>"}},
		"one of two":   {"h", map[string]string{"plugin": "p1", "a": "<apikey>VAULT_X</apikey>", "z": "<token>VAULT_Y</token>"}},
		"no plugin":    {"h", map[string]string{"hdr": "<apikey>VAULT_X</apikey>"}},
	}
	for name, tc := range cases {
		rig.asked = nil
		plain, zero, err := seam(context.Background(), pluginFire(tc.hook), tc.tagged)
		if plain != nil || zero == nil || err != error(secrets.ErrTagNotPermitted) || err.Error() != wantTagRefusal {
			t.Fatalf("%s: plain=%v err=%v, want the tag-not-permitted refusal", name, plain, err)
		}
		if len(rig.asked) == 0 {
			t.Fatalf("%s: the rehydrator's own scan was never asked about a tag", name)
		}
		zero()
	}
	if len(rig.vault.reads) != 0 {
		t.Fatalf("the vault was read %v before a refusal", rig.vault.reads)
	}
}

func TestBoundRehydrateAcceptedBindingPlaintext(t *testing.T) {
	value := fakeValue("x")
	rig := newBoundRig(t, map[string]string{"VAULT_X": value})
	seam := BoundRehydrate(admittedOf(VaultBinding{"h", "p1", "VAULT_X"}), rig.bytes)
	tagged := map[string]string{"plugin": "p1", "tool": "send", "hdr": "pre <apikey>VAULT_X</apikey>", "empty": ""}
	plain, zero, err := seam(context.Background(), pluginFire("h"), tagged)
	if err != nil || zero == nil {
		t.Fatalf("admitted binding refused: %v", err)
	}
	want := map[string]string{"plugin": "p1", "tool": "send", "hdr": "pre " + value, "empty": ""}
	if !maps.Equal(plain, want) || !slices.Equal(rig.vault.reads, []string{"VAULT_X"}) {
		t.Fatalf("plain %v reads %v, want %v and one read", plain, rig.vault.reads, want)
	}
	if !strings.Contains(string(rig.plain), value) {
		t.Fatal("stub check: the plain buffer must hold the value before zero")
	}
	zero()
	if strings.Trim(string(rig.plain), "\x00") != "" {
		t.Fatal("zero left plaintext in the rehydrated buffer")
	}
	untagged := map[string]string{"plugin": "p1", "x": "a < b"}
	never := func() ([]VaultBinding, error) { t.Fatal("admitted read for untagged params"); return nil, nil }
	out, _, err := BoundRehydrate(never, rig.bytes)(context.Background(), pluginFire("h"), untagged)
	if err != nil || !maps.Equal(out, untagged) || len(rig.vault.reads) != 1 {
		t.Fatalf("untagged params: %v err %v reads %v, want unchanged", out, err, rig.vault.reads)
	}
}

func TestBoundRehydrateEgressIntroducedTagRefused(t *testing.T) {
	raw := fakeValue("egress")
	rig := newBoundRig(t, map[string]string{"HOOK_ENTRY": raw, "AK": fakeValue("other")})
	hook := HookConfig{ID: "h", Namespace: "jobs", Trigger: "k", ActionType: ActionTypePluginCall,
		ActionParams: map[string]string{"plugin": "p1", "field": raw, "auth": "<apikey>AK</apikey>"}}
	bindings, err := VaultBindingsOf([]HookConfig{hook}, tagNames)
	if err != nil || !slices.Equal(bindings, []VaultBinding{{"h", "p1", "AK"}}) {
		t.Fatalf("bindings %v (err %v), want only the declared AK", bindings, err)
	}
	tok, _ := egress.DefaultRegistry().Capability(egress.EgressClassHook)
	engine := testEngine(t, map[string][]byte{"HOOK_ENTRY": []byte(raw)})
	tagged, err := interceptParams(context.Background(), engine, tok, hook.ActionParams)
	if err != nil || !strings.Contains(tagged["field"], "HOOK_ENTRY</") || strings.Contains(tagged["field"], raw) {
		t.Fatalf("stub check: the egress pass did not tag the raw value: %v %v", tagged, err)
	}
	plain, _, err := BoundRehydrate(admittedOf(bindings...), rig.bytes)(context.Background(), Fire{Hook: hook}, tagged)
	if plain != nil || err != error(secrets.ErrTagNotPermitted) || err.Error() != wantTagRefusal || len(rig.vault.reads) != 0 {
		t.Fatalf("egress-introduced tag: plain=%v err=%v reads=%v, want refused unread", plain, err, rig.vault.reads)
	}
	if !slices.Contains(rig.asked, "HOOK_ENTRY") {
		t.Fatalf("the guard was not asked about the egress tag: %v", rig.asked)
	}
}

func TestBoundRehydrateAdmittedErrorRefuses(t *testing.T) {
	rig := newBoundRig(t, map[string]string{"VAULT_X": fakeValue("x")})
	cause := cascade.New(cascade.KindIntegrity, "fixture: the admitted config is not readable")
	tagged := map[string]string{"plugin": "p1", "hdr": "<apikey>VAULT_X</apikey>"}
	failing := func() ([]VaultBinding, error) { return []VaultBinding{{"h", "p1", "VAULT_X"}}, cause }
	plain, zero, err := BoundRehydrate(failing, rig.bytes)(context.Background(), pluginFire("h"), tagged)
	if plain != nil || zero == nil || !cascade.HasKind(err, cascade.KindUnavailable) || !errors.Is(err, cause) ||
		err.Error() != "unavailable: hooks: the admitted vault bindings are unavailable; the action is not rehydrated: "+
			"integrity: fixture: the admitted config is not readable" {
		t.Fatalf("admitted error: plain=%v err=%v", plain, err)
	}
	for name, seam := range map[string]RehydrateFunc{
		"nil admitted":  BoundRehydrate(nil, rig.bytes),
		"nil rehydrate": BoundRehydrate(admittedOf(VaultBinding{"h", "p1", "VAULT_X"}), nil),
	} {
		plain, zero, err := seam(context.Background(), pluginFire("h"), tagged)
		if plain != nil || zero == nil || !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("%s: plain=%v err=%v, want an unavailable refusal", name, plain, err)
		}
	}
	if len(rig.vault.reads) != 0 {
		t.Fatalf("the vault was read %v", rig.vault.reads)
	}
}
