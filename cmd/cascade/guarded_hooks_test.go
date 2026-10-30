//go:build !windows

// Purpose: the guarded [hooks] family end to end: a hot reload adding a
// hook or a vault binding returns ElevationRequired through the real
// HotReloader and applies only through config.accept_loosening over that
// exact file (the SEC-04 fixture); a restart over a baseline without the
// binding runs the admitted list (ApplyEffective); a removal applies at once.
// Inputs: temp config files, the elevated daemon fixture over in-memory
// custody, a fixed clock. HOME, USERPROFILE and CASCADE_HOME point into
// t.TempDir().
// SPORT: cmd/cascade guarded hooks family tests (P1-SUP-05).
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

const (
	hookPlain = "[[hooks]]\nid = \"notify\"\nnamespace = \"jobs\"\ntrigger = \"job.done\"\naction_type = \"plugin-call\"\n" +
		"action_params = { plugin = \"notifier\", tool = \"send\" }\n"
	hookTagged = "[[hooks]]\nid = \"notify-key\"\nnamespace = \"jobs\"\ntrigger = \"job.done\"\naction_type = \"plugin-call\"\n" +
		"action_params = { plugin = \"notifier\", tool = \"send\", hdr = \"<apikey>NOTIFY_KEY</apikey>\" }\n"
	hookSecond = "[[hooks]]\nid = \"audit\"\nnamespace = \"jobs\"\ntrigger = \"job.failed\"\naction_type = \"plugin-call\"\n" +
		"action_params = { plugin = \"notifier\", tool = \"page\" }\n"
)

var notifyKeyBinding = hooks.VaultBinding{HookID: "notify-key", PluginID: "notifier", VaultName: "NOTIFY_KEY"}

// hooksRealHome is the home directory before any test redirects it.
var hooksRealHome, _ = os.UserHomeDir()

// isolateHome points HOME, USERPROFILE and CASCADE_HOME into a temp dir and
// fails the test if the home still resolves to the real one.
func isolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(k, home)
	}
	got, err := os.UserHomeDir()
	if err != nil || hooksRealHome == "" || got == hooksRealHome || strings.HasPrefix(got, hooksRealHome+string(filepath.Separator)) {
		t.Fatalf("test home %q is not isolated from the real home (err %v)", got, err)
	}
}

// acceptBody writes body and runs config.accept_loosening over exactly it.
func acceptBody(t *testing.T, f acceptFixture, body string) {
	t.Helper()
	writeConfig(t, f.path, body)
	var out acceptLooseningResult
	if err := f.d.Call(context.Background(), runtime.AcceptLooseningMethod, digestParams(body), &out); err != nil || !out.Reload.Accepted {
		t.Fatalf("accept: %+v %v", out, err)
	}
}

// hookIDs is the admitted hook ids of cfg, read through the family.
func hookIDs(t *testing.T, cfg *runtime.Config) []string {
	t.Helper()
	list, err := hooks.ParseHooksSection(cfg.Extra[hooks.SectionName], hookLoadable)
	if err != nil {
		t.Fatalf("[hooks] of the config: %v", err)
	}
	var ids []string
	for _, h := range list {
		ids = append(ids, h.ID)
	}
	slices.Sort(ids)
	return ids
}

func bindingsOf(t *testing.T, cfg *runtime.Config) []hooks.VaultBinding {
	t.Helper()
	list, err := hooks.ParseHooksSection(cfg.Extra[hooks.SectionName], hookLoadable)
	if err != nil {
		t.Fatal(err)
	}
	b, err := hooks.VaultBindingsOf(list, hookTagNames)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// expectAcceptGated: reloading after is refused naming want (and only the
// hooks keys in wantKeys, never the whole section), Current keeps prevIDs,
// and config.accept_loosening over that exact file applies it.
func expectAcceptGated(t *testing.T, before, after string, wantKeys, prevIDs, nextIDs []string) acceptFixture {
	t.Helper()
	isolateHome(t)
	f := newAcceptFixture(t)
	acceptBody(t, f, before)
	if got := hookIDs(t, f.hr.Current()); !slices.Equal(got, prevIDs) {
		t.Fatalf("setup: accepted [hooks] %v, want %v", got, prevIDs)
	}
	writeConfig(t, f.path, after)
	o := f.hr.Reload(context.Background())
	if keys := pathKeys(o.LooseningPaths); !o.ElevationRequired || !slices.Equal(slices.Compact(slices.Sorted(slices.Values(keys))), wantKeys) {
		t.Fatalf("reload: elevation=%v paths=%v, want ElevationRequired naming exactly %v", o.ElevationRequired, keys, wantKeys)
	}
	if got := hookIDs(t, f.hr.Current()); !slices.Equal(got, prevIDs) {
		t.Fatalf("refused reload changed [hooks]: %v, want %v", got, prevIDs)
	}
	acceptBody(t, f, after)
	if got := hookIDs(t, f.hr.Current()); !slices.Equal(got, nextIDs) {
		t.Fatalf("accepted reload [hooks]: %v, want %v", got, nextIDs)
	}
	return f
}

func TestHookWithVaultTagRequiresAcceptLoosening(t *testing.T) {
	base := tightToml + hookPlain
	f := expectAcceptGated(t, base, base+hookTagged, []string{"hooks.action", "hooks.vault_binding"},
		[]string{"notify"}, []string{"notify", "notify-key"})
	if got := bindingsOf(t, f.hr.Current()); !slices.Equal(got, []hooks.VaultBinding{notifyKeyBinding}) {
		t.Fatalf("admitted bindings after accept: %v", got)
	}
}

func TestHookLooseningDetails(t *testing.T) {
	a, _ := loadBody(t, hookPlain)
	b, _ := loadBody(t, strings.Replace(hookPlain, `"send"`, `"page"`, 1)+hookTagged)
	va, aerr := hooksFamilyExtract(a.Extra)
	vb, berr := hooksFamilyExtract(b.Extra)
	paths, err := hooksFamilyCompare(va, vb)
	var got []string
	for _, p := range paths {
		got = append(got, p.Key+" "+p.Proposed.(string))
	}
	want := []string{"hooks.action notify/plugin-call/notifier/page", "hooks.action notify-key/plugin-call/notifier/send",
		"hooks.vault_binding notify-key/notifier/NOTIFY_KEY"}
	if aerr != nil || berr != nil || err != nil || !slices.Equal(got, want) {
		t.Fatalf("paths %q (%v %v %v), want %q", got, aerr, berr, err, want)
	}
	if back, err := hooksFamilyCompare(vb, nil); err != nil || len(back) != 0 {
		t.Fatalf("removing every hook reported %v (%v), want a tightening", back, err)
	}
}

func TestHookAdditionRequiresAcceptLoosening(t *testing.T) {
	base := tightToml + hookPlain
	f := expectAcceptGated(t, base, base+hookSecond, []string{"hooks.action"}, []string{"notify"}, []string{"audit", "notify"})
	if got := bindingsOf(t, f.hr.Current()); len(got) != 0 {
		t.Fatalf("a hook without a tag bound %v", got)
	}
}

func TestHookTriggerOrParamsChangeIsLoosening(t *testing.T) {
	edit := func(old, repl string) string { return strings.Replace(hookPlain, old, repl, 1) }
	cases := map[string]struct {
		after string
		want  []string
	}{
		"trigger":     {edit(`trigger = "job.done"`, `trigger = "job.started"`), []string{"hooks.action"}},
		"namespace":   {edit(`namespace = "jobs"`, `namespace = "fleet"`), []string{"hooks.action"}},
		"action_type": {edit(`action_type = "plugin-call"`, `action_type = "agent-note"`), []string{"hooks.action"}},
		"param value": {edit(`tool = "send"`, `tool = "broadcast"`), []string{"hooks.action"}},
		"param added": {edit(`tool = "send"`, `tool = "send", channel = "ops"`), []string{"hooks.action"}},
		"tag added":   {edit(`tool = "send"`, `tool = "send", hdr = "<apikey>NOTIFY_KEY</apikey>"`), []string{"hooks.action", "hooks.vault_binding"}},
	}
	for name, tc := range cases {
		out := reload(t, hookPlain, tc.after)
		if keys := slices.Compact(slices.Sorted(slices.Values(pathKeys(out.LooseningPaths)))); !out.ElevationRequired || !slices.Equal(keys, tc.want) {
			t.Fatalf("%s: elevation=%v paths=%v, want %v", name, out.ElevationRequired, keys, tc.want)
		}
	}
	// A plugin change on an accepted binding is a new binding.
	moved := strings.Replace(hookTagged, `plugin = "notifier"`, `plugin = "mailer"`, 1)
	if out := reload(t, hookTagged, moved); !out.ElevationRequired || !slices.Contains(pathKeys(out.LooseningPaths), "hooks.vault_binding") {
		t.Fatalf("plugin change on a bound hook: %v", pathKeys(out.LooseningPaths))
	}
}

func TestHookRemovalIsTightening(t *testing.T) {
	for name, tc := range map[string][2]string{
		"one of two": {hookPlain + hookTagged, hookPlain}, "the last": {hookTagged, ""}, "binding dropped": {hookTagged + hookPlain, hookPlain},
	} {
		cfg, path := loadBody(t, tc[0])
		hr := runtime.NewHotReloader(path, guardedOpts(path), cfg, guardedClock, nil, nil, nil)
		writeConfig(t, path, tc[1])
		if out := hr.Reload(context.Background()); !out.Accepted || len(out.LooseningPaths) != 0 {
			t.Fatalf("%s: removal refused: %+v", name, out)
		}
		if len(bindingsOf(t, hr.Current())) != 0 {
			t.Fatalf("%s: a removed binding is still admitted", name)
		}
	}
}

// signedRestart records a signed baseline of accepted (config.accept_loosening
// under a fixture verifier), then boots edited against it the way the
// daemon does: CheckFile, then ApplyEffective when the values differ.
func signedRestart(t *testing.T, accepted, edited *runtime.Config) (runtime.BaselineResult, *runtime.Config) {
	t.Helper()
	ctx := context.Background()
	store := storetest.NewMemStore()
	checker := runtime.NewBaselineChecker(store, guardedClock, nil, runtime.NewStoreAuditRecorder(store, guardedClock))
	checker.SetVerifier(func(runtime.BaselineProof) error { return nil })
	digest := runtime.ConfigDigest([]byte("fixture config bytes"))
	proof := runtime.BaselineProof{Method: runtime.AcceptLooseningMethod, RawParams: []byte(`{"config_sha256":"` + digest + `"}`), Attestation: []byte(`{"fixture":true}`)}
	if err := checker.Accept(ctx, runtime.EffectiveConfigOf(accepted), proof); err != nil {
		t.Fatal(err)
	}
	onDisk := runtime.EffectiveConfigOf(edited)
	res := checker.CheckFile(ctx, onDisk, runtime.BaselineFile{})
	if runtime.ComputeSectionsHash(res.Effective) != runtime.ComputeSectionsHash(onDisk) {
		runtime.ApplyEffective(edited, res.Effective)
	}
	return res, edited
}

// TestBootUnacceptedBindingNotEffective: the signed baseline admits
// [notify]; the file adds the bound notify-key. The divergent boot runs the
// restrictive fallback, whose [hooks] is the shipped default (no hook the
// baseline did not sign, so no binding); a reload of the same hooks is
// refused naming hooks.vault_binding.
func TestBootUnacceptedBindingNotEffective(t *testing.T) {
	isolateHome(t)
	accepted, _ := loadBody(t, routingLooser+hookPlain)
	edited, path := loadBody(t, routingLooser+hookPlain+hookTagged)
	if got := bindingsOf(t, edited); !slices.Equal(got, []hooks.VaultBinding{notifyKeyBinding}) {
		t.Fatalf("stub check: the edited file binds %v", got)
	}
	res, cfg := signedRestart(t, accepted, edited)
	if res.Outcome.Detail() != "baseline_divergent" {
		t.Fatalf("restart with an added binding: %s, want baseline_divergent", res.Outcome.Detail())
	}
	if got := hookIDs(t, cfg); len(got) != 0 || len(bindingsOf(t, cfg)) != 0 || cfg.Extra[hooks.SectionName] != nil {
		t.Fatalf("effective [hooks] %v, want no unsigned hook and no binding", got)
	}
	hr := runtime.NewHotReloader(path, guardedOpts(path), cfg, guardedClock, nil, nil, nil)
	writeConfig(t, path, tightToml+hookPlain+hookTagged) // the unchanged hooks plus a tightening
	out := hr.Reload(context.Background())
	if !out.ElevationRequired || !slices.Contains(pathKeys(out.LooseningPaths), "hooks.vault_binding") {
		t.Fatalf("reload after a divergent boot: %+v, want ElevationRequired naming hooks.vault_binding", out)
	}
	if got := hookIDs(t, hr.Current()); len(got) != 0 || len(bindingsOf(t, hr.Current())) != 0 {
		t.Fatalf("Reloader.Current [hooks] %v after the refused reload", got)
	}
}

func TestHooksFamilyExtractCanonical(t *testing.T) {
	a, _ := loadBody(t, hookTagged+hookPlain+hookSecond)
	b, _ := loadBody(t, hookSecond+strings.Replace(hookPlain, "action_params = { plugin = \"notifier\", tool = \"send\" }",
		"[hooks.action_params]\ntool = \"send\"\nplugin = \"notifier\"", 1)+hookTagged)
	va, err := hooksFamilyExtract(a.Extra)
	if err != nil {
		t.Fatal(err)
	}
	if vb, err := hooksFamilyExtract(b.Extra); err != nil || string(va) != string(vb) {
		t.Fatalf("reordered hooks extract differently:\n%s\n%s (%v)", va, vb, err)
	}
	var decoded []hooks.HookConfig
	if err := json.Unmarshal(va, &decoded); err != nil || len(decoded) != 3 || decoded[2].ActionParams["hdr"] != "<apikey>NOTIFY_KEY</apikey>" {
		t.Fatalf("action_params are not stored verbatim: %s (%v)", va, err)
	}
	if got := []string{decoded[0].ID, decoded[1].ID, decoded[2].ID}; !slices.Equal(got, []string{"audit", "notify", "notify-key"}) {
		t.Fatalf("not sorted by id: %v", got)
	}
	if fam := runtime.EffectiveConfigOf(a).Families[hooks.SectionName]; string(fam) != string(va) {
		t.Fatalf("the registered family value %s differs from Extract %s", fam, va)
	}
	extra := map[string]interface{}{}
	hooksFamilyApply(extra, va)
	if back, err := hooksFamilyExtract(extra); err != nil || string(back) != string(va) {
		t.Fatalf("Apply then Extract: %s (%v)", back, err)
	}
	if v, err := hooksFamilyExtract(map[string]interface{}{}); v != nil || err != nil {
		t.Fatalf("no hooks: %s %v, want the default", v, err)
	}
	entry := func(actionType string, params map[string]interface{}) []interface{} {
		return []interface{}{map[string]interface{}{"id": "x", "namespace": "jobs", "trigger": "job.done",
			"action_type": actionType, "action_params": params}}
	}
	for name, section := range map[string]interface{}{
		"a table, not an array": map[string]interface{}{"id": "x"},
		"note tag":              entry("agent-note", map[string]interface{}{"note": "<apikey>K</apikey>"}),
		"pii tag":               entry("plugin-call", map[string]interface{}{"plugin": "p", "ssn": `<pii kind="ssn">S</pii>`}),
		"shell (not loadable)":  entry("shell", map[string]interface{}{"command": "true"}),
		"malformed tag":         entry("plugin-call", map[string]interface{}{"plugin": "p", "k": "<apikey>K"}),
	} {
		cfg := &runtime.Config{Extra: map[string]interface{}{hooks.SectionName: section}}
		if v, err := hooksFamilyExtract(cfg.Extra); err == nil || v != nil {
			t.Fatalf("%s: extracted %s (err %v), want an error (never an empty section)", name, v, err)
		}
		if fam := runtime.EffectiveConfigOf(cfg).Families[hooks.SectionName]; !json.Valid(fam) || len(fam) == 0 || string(fam) == "null" {
			t.Fatalf("%s: family value %q, want the invalid marker", name, fam)
		}
	}
}
