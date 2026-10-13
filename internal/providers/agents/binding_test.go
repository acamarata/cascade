// Purpose: tests for the harness binding table: the sorted deep-copy listing,
// duplicate and malformed registration, the Resolver's refusals (unknown
// lane, absent plugin, never another harness), the closed credential table
// and the claude factory.
// SPORT: internal.providers.agents tests (ADD) - P1-AGT-02.

package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/agents/claude"
)

// isolateHome points HOME and USERPROFILE at a fresh temp dir.
func isolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// fakeBinding returns a valid row for harness id with its own plugin.
func fakeBinding(id string) Binding {
	return Binding{Harness: HarnessID(id), PluginID: "plugin-" + id, Runtime: RuntimeHarnessCLI, LaneID: "agent:" + id,
		ConfigDirVar: strings.ToUpper(id) + "_HOME", Binaries: []string{id},
		Credentials: []CredentialSpec{{Class: CredentialSubscription}, {Class: CredentialAPIKey, KeyVar: strings.ToUpper(id) + "_KEY"}}}
}

// sameCred compares by field, so a map field added to CredentialSpec later keeps this file compiling.
func sameCred(a, b CredentialSpec) bool { return a.Class == b.Class && a.KeyVar == b.KeyVar }

func noFactory(SpawnSite) (provider.AgentProvider, error) { return nil, nil }

func TestBindingsListingSortedCopy(t *testing.T) {
	got := Bindings()
	if len(got) == 0 || got[0].Harness != HarnessClaude {
		t.Fatalf("Bindings() = %+v, want the claude row", got)
	}
	got[0].Harness, got[0].PluginID = "mutated", "mutated"
	got[0].Credentials[0].KeyVar = "MUTATED"
	got[0].Binaries[0] = "mutated"
	again := Bindings()[0]
	if again.Harness != HarnessClaude || again.PluginID != "cascade-claude" || again.Credentials[0].KeyVar != "CLAUDE_CODE_OAUTH_TOKEN" || again.Binaries[0] != "claude" {
		t.Fatalf("mutating a listing changed the table: %+v", again)
	}
	r := &registry{rows: map[HarnessID]entry{}}
	for _, id := range []string{"zeta", "alpha", "mid"} {
		r.add(fakeBinding(id), noFactory)
	}
	var order []string
	for _, b := range r.list() {
		order = append(order, string(b.Harness))
	}
	if strings.Join(order, ",") != "alpha,mid,zeta" {
		t.Fatalf("listing order %v, want sorted by harness id", order)
	}
}

func mustPanic(t *testing.T, what string, fn func()) string {
	t.Helper()
	var msg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				msg, _ = r.(string)
			}
		}()
		fn()
	}()
	if msg == "" {
		t.Fatalf("%s did not panic", what)
	}
	return msg
}

func TestRegisterHarnessDuplicatePanics(t *testing.T) {
	r := &registry{rows: map[HarnessID]entry{}}
	r.add(fakeBinding("dup"), noFactory)
	if msg := mustPanic(t, "a second add", func() { r.add(fakeBinding("dup"), noFactory) }); !strings.Contains(msg, "registered twice") {
		t.Fatalf("duplicate panic %q", msg)
	}
	claude := Bindings()[0]
	if msg := mustPanic(t, "re-registering claude", func() { RegisterHarness(claude, noFactory) }); !strings.Contains(msg, `"claude" registered twice`) {
		t.Fatalf("global duplicate panic %q", msg)
	}
	if n := len(Bindings()); n != 1 {
		t.Fatalf("a refused registration changed the table: %d rows", n)
	}
	mustPanic(t, "a nil factory", func() { r.add(fakeBinding("nilf"), nil) })
	for _, id := range []string{"local", "gemini", "x"} {
		if msg := mustPanic(t, "registering "+id, func() { RegisterHarness(fakeBinding(id), noFactory) }); !strings.Contains(msg, "closed harness set") {
			t.Fatalf("%s: panic %q, want the closed-set refusal", id, msg)
		}
	}
	bad := fakeBinding("bad")
	bad.LaneID = "agent:other"
	mustPanic(t, "a lane that is not agent:<harness>", func() { r.add(bad, noFactory) })
}

func presence(enabled ...string) PluginPresence {
	return func(id string) bool {
		for _, e := range enabled {
			if e == id {
				return true
			}
		}
		return false
	}
}

func TestResolverRefusesAbsentPlugin(t *testing.T) {
	r, err := NewResolver(presence("somebody-else"))
	if err != nil {
		t.Fatal(err)
	}
	_, f, err := r.Resolve("agent:claude")
	if f != nil || !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), `"cascade-claude"`) {
		t.Fatalf("Resolve with the plugin absent = %v, %v: want KindUnavailable naming cascade-claude and no factory", f != nil, err)
	}
	ok, _ := NewResolver(presence("cascade-claude"))
	b, f, err := ok.Resolve("agent:claude")
	if err != nil || f == nil || b.Harness != HarnessClaude || b.LaneID != "agent:claude" {
		t.Fatalf("Resolve with the plugin present = %+v, %v", b, err)
	}
	if _, err := NewResolver(nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NewResolver(nil) = %v, want KindInvalidInput", err)
	}
}

func TestResolverRefusesUnknownLane(t *testing.T) {
	r, _ := NewResolver(presence("cascade-claude"))
	for _, lane := range []string{"", "claude", "agent:", "agent:nope", "agent:claude ", "AGENT:claude", "local", "agent:local",
		"agent:claude/../claude", "model:claude"} {
		b, f, err := r.Resolve(lane)
		if f != nil || b.Harness != "" || !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("Resolve(%q) = %+v, factory %v, %v: want KindUnavailable and nothing else", lane, b, f != nil, err)
		}
	}
}

func TestResolverNeverFallsBack(t *testing.T) {
	reg := &registry{rows: map[HarnessID]entry{}}
	other := false
	reg.add(fakeBinding("claude"), noFactory)
	reg.add(fakeBinding("other"), func(SpawnSite) (provider.AgentProvider, error) { other = true; return nil, nil })
	r, _ := newResolver(reg, presence("plugin-other"))
	b, f, err := r.Resolve("agent:claude")
	if f != nil || b.Harness != "" || !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), `"plugin-claude"`) {
		t.Fatalf("claude plugin disabled: %+v, factory %v, %v: want KindUnavailable and no fallback", b, f != nil, err)
	}
	if other {
		t.Fatal("the other harness's factory ran")
	}
	if _, f, err := r.Resolve("agent:other"); err != nil || f == nil {
		t.Fatalf("the other harness is itself available: %v", err)
	} else if _, _ = f(SpawnSite{}); !other {
		t.Fatal("Resolve(agent:other) returned a factory that is not other's")
	}
}

// closedClasses is the closed credential class set, written out here so the
// test does not borrow the validator's own list.
var closedClasses = map[string]bool{"subscription": true, "oauth_token": true, "api_key": true, "compat_key": true}

func TestBindingCredentialTableClosed(t *testing.T) {
	for _, b := range Bindings() {
		if b.ConfigDirVar == "" {
			t.Fatalf("%s: empty ConfigDirVar", b.Harness)
		}
		for _, c := range b.Credentials {
			if !closedClasses[c.Class] {
				t.Fatalf("%s: credential class %q is outside the closed set", b.Harness, c.Class)
			}
			if (c.Class == "subscription") != (c.KeyVar == "") {
				t.Fatalf("%s: class %q with key variable %q", b.Harness, c.Class, c.KeyVar)
			}
		}
	}
	want := Binding{Harness: "claude", PluginID: "cascade-claude", Runtime: RuntimeHarnessCLI, LaneID: "agent:claude",
		ConfigDirVar: claude.VendorConfigDirVar, Binaries: []string{"claude"},
		Credentials: []CredentialSpec{{Class: "oauth_token", KeyVar: "CLAUDE_CODE_OAUTH_TOKEN"}, {Class: "api_key", KeyVar: "ANTHROPIC_API_KEY"}}}
	var got Binding
	for _, b := range Bindings() {
		if b.Harness == "claude" {
			got = b
		}
	}
	if got.Harness != want.Harness || got.PluginID != want.PluginID || got.Runtime != want.Runtime || got.LaneID != want.LaneID ||
		got.ConfigDirVar != want.ConfigDirVar || strings.Join(got.Binaries, ",") != "claude" ||
		len(got.Credentials) != 2 || !sameCred(got.Credentials[0], want.Credentials[0]) || !sameCred(got.Credentials[1], want.Credentials[1]) {
		t.Fatalf("claude row = %+v, want exactly %+v", got, want)
	}
	for name, edit := range map[string]func(*Binding){
		"empty ConfigDirVar":         func(b *Binding) { b.ConfigDirVar = "" },
		"lower-case ConfigDirVar":    func(b *Binding) { b.ConfigDirVar = "claude_dir" },
		"unknown class":              func(b *Binding) { b.Credentials = []CredentialSpec{{Class: "password", KeyVar: "X_KEY"}} },
		"key class without KeyVar":   func(b *Binding) { b.Credentials = []CredentialSpec{{Class: CredentialAPIKey}} },
		"subscription with KeyVar":   func(b *Binding) { b.Credentials = []CredentialSpec{{Class: CredentialSubscription, KeyVar: "X_KEY"}} },
		"duplicate class":            func(b *Binding) { b.Credentials = append(b.Credentials, b.Credentials[0]) },
		"no credentials":             func(b *Binding) { b.Credentials = nil },
		"binary with a path":         func(b *Binding) { b.Binaries = []string{"/usr/bin/x"} },
		"no binaries":                func(b *Binding) { b.Binaries = nil },
		"runtime other than harness": func(b *Binding) { b.Runtime = "in-process" },
		"empty plugin":               func(b *Binding) { b.PluginID = "" },
		"harness with a colon":       func(b *Binding) { b.Harness, b.LaneID = "a:b", "agent:a:b" },
	} {
		b := fakeBinding("x")
		edit(&b)
		if validateBinding(b) == nil {
			t.Errorf("%s: validateBinding accepted the row", name)
		}
	}
	if err := validateBinding(fakeBinding("x")); err != nil {
		t.Fatalf("the fixture row is itself invalid: %v", err)
	}
}

func TestClaudeFactoryAdaptsSpawnSite(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	site := SpawnSite{Binary: bin, Defaults: provider.NewDriverDefaults(), WorktreeRoot: dir, Clock: fixedClock{}}
	d, err := newClaudeDriver(site)
	if err != nil {
		t.Fatalf("newClaudeDriver: %v", err)
	}
	want, _ := filepath.EvalSymlinks(bin)
	if rep, ok := d.(provider.ExecutableReporter); !ok || rep.Executable() != want {
		t.Fatalf("driver Executable = %v, want %q", d, want)
	}
	site.Clock = nil
	if _, err := newClaudeDriver(site); err != nil {
		t.Fatalf("a probe-only site (no clock) must build a driver: %v", err)
	}
	site.Clock = fixedClock{}
	for name, edit := range map[string]func(*SpawnSite){
		"key variable outside the table": func(s *SpawnSite) { s.KeyVar, s.CredentialClass = "OPERATOR_CHOSEN_KEY", "api_key" },
		"class and variable mismatched":  func(s *SpawnSite) { s.KeyVar, s.CredentialClass = "ANTHROPIC_API_KEY", "oauth_token" },
		"class outside the claude table": func(s *SpawnSite) { s.CredentialClass = "subscription" },
		"vendor auth is not the config":  func(s *SpawnSite) { s.VendorAuthVar = "SOMETHING_ELSE" },
	} {
		bad := site
		edit(&bad)
		if _, err := newClaudeDriver(bad); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: %v, want KindInvalidInput", name, err)
		}
	}
	ok := site
	ok.KeyVar, ok.CredentialClass, ok.VendorAuthVar = "CLAUDE_CODE_OAUTH_TOKEN", "oauth_token", claude.VendorConfigDirVar
	if _, err := newClaudeDriver(ok); err != nil {
		t.Fatalf("a site inside the table refused: %v", err)
	}
	site.Clock, site.Binary = fixedClock{}, "relative/claude"
	if _, err := newClaudeDriver(site); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("relative binary: %v, want KindInvalidInput", err)
	}
}
