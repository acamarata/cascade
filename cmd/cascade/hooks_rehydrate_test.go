//go:build !windows

// Purpose: the hook rehydration seam over the admitted bindings: an
// accepted (hook, plugin, vault name) binding resolves, anything else is
// refused before the vault is read, a pii tag is never resolved, and the
// binding source answers with fixed text.
// Inputs: a real HotReloader over a temp config, a counting in-memory
// custody under a real secrets.Rehydrator.
// SPORT: tests for hook rehydration in the cmd/cascade composition (P1-SUP-02).
package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/hooks"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

const hookSecretValue = "s3cr3t-value-for-test"

// hookCountingCustody is an in-memory custody that counts reads.
type hookCountingCustody struct {
	mu    sync.Mutex
	items map[string][]byte
	gets  int
}

func (c *hookCountingCustody) Name() string    { return "counting" }
func (c *hookCountingCustody) Available() bool { return true }
func (c *hookCountingCustody) Set(_ context.Context, name string, v []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[name] = append([]byte(nil), v...)
	return nil
}
func (c *hookCountingCustody) Get(_ context.Context, name string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	v, ok := c.items[name]
	if !ok {
		return nil, secrets.ErrSecretNotFound(name)
	}
	return append([]byte(nil), v...), nil
}
func (c *hookCountingCustody) Delete(context.Context, string) error { return nil }
func (c *hookCountingCustody) List(context.Context) ([]string, error) {
	return nil, nil
}

// rehydrateRig is the composed seam: the admitted config, the vault and
// the BoundRehydrate the daemon builds.
type rehydrateRig struct {
	custody *hookCountingCustody
	seam    hooks.RehydrateFunc
}

func newRehydrateRig(t *testing.T, body string) rehydrateRig {
	t.Helper()
	cfg, path := loadBody(t, body)
	hr := runtime.NewHotReloader(path, guardedOpts(path), cfg, guardedClock, nil, nil, nil)
	custody := &hookCountingCustody{items: map[string][]byte{"NOTIFY_KEY": []byte(hookSecretValue)}}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		t.Fatal(err)
	}
	rh, err := secrets.NewRehydrator(broker)
	if err != nil {
		t.Fatal(err)
	}
	return rehydrateRig{custody: custody, seam: hooks.BoundRehydrate(admittedHookBindings(hr), rehydrateOver(rh))}
}

func (r rehydrateRig) run(hookID, tag string) (map[string]string, func(), error) {
	fire := hooks.Fire{Hook: hooks.HookConfig{ID: hookID, ActionType: hooks.ActionTypePluginCall}, Namespace: "jobs", EventSeq: 1}
	return r.seam(context.Background(), fire, map[string]string{hooks.PluginParamName: "notifier", hooks.PluginToolParam: "send", "hdr": tag})
}

func TestDaemonHookRehydratesAcceptedBinding(t *testing.T) {
	rig := newRehydrateRig(t, hookTagged)
	plain, zero, err := rig.run("notify-key", "<apikey>NOTIFY_KEY</apikey>")
	if err != nil {
		t.Fatalf("accepted binding refused: %v", err)
	}
	defer zero()
	if plain["hdr"] != hookSecretValue || rig.custody.gets != 1 {
		t.Fatalf("hdr = %q after %d vault reads; want the vault value after one read", plain["hdr"], rig.custody.gets)
	}
}

func TestDaemonHookUnacceptedBindingRefused(t *testing.T) {
	rig := newRehydrateRig(t, hookPlain) // [hooks] admits notify, which binds nothing
	_, _, err := rig.run("notify", "<apikey>NOTIFY_KEY</apikey>")
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("unbound tag: %v; want KindPermissionDenied", err)
	}
	if rig.custody.gets != 0 {
		t.Fatalf("%d vault reads for an unaccepted binding; want 0", rig.custody.gets)
	}
	if _, _, err := rig.run("ghost", "<apikey>NOTIFY_KEY</apikey>"); !cascade.HasKind(err, cascade.KindPermissionDenied) || rig.custody.gets != 0 {
		t.Fatalf("a hook with no admitted entry: %v after %d reads", err, rig.custody.gets)
	}
}

func TestDaemonHookAcceptedBindingMissingVaultName(t *testing.T) {
	rig := newRehydrateRig(t, hookTagged) // binds NOTIFY_KEY only
	_, _, err := rig.run("notify-key", "<apikey>OTHER_KEY</apikey>")
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("a name outside the binding: %v; want KindPermissionDenied", err)
	}
	if rig.custody.gets != 0 {
		t.Fatalf("%d vault reads for a name no binding gives; want 0", rig.custody.gets)
	}
}

func TestRehydrateOverRefusesPIITag(t *testing.T) {
	custody := &hookCountingCustody{items: map[string][]byte{"HOME_SSN": []byte("123-45-6789")}}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		t.Fatal(err)
	}
	rh, err := secrets.NewRehydrator(broker)
	if err != nil {
		t.Fatal(err)
	}
	asked := 0
	_, zero, err := rehydrateOver(rh)(context.Background(), []byte(`<pii kind="ssn">HOME_SSN</pii>`),
		func(string) bool { asked++; return true })
	if zero == nil {
		t.Fatal("a refusal returned a nil zero func")
	}
	zero()
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("pii tag with an allow-all predicate: %v; want KindPermissionDenied", err)
	}
	if asked != 0 || custody.gets != 0 {
		t.Fatalf("name predicate asked %d times, vault read %d times; want 0 and 0", asked, custody.gets)
	}
}

func TestAdmittedHookBindingsFixedTextErrors(t *testing.T) {
	const canary = "CANARY-VALUE-9f2c"
	if _, err := admittedHookBindings(nil)(); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("nil reloader: %v; want KindUnavailable", err)
	}
	cfg, path := loadBody(t, tightToml)
	reloaderWith := func(actionType string, params map[string]interface{}) *runtime.HotReloader {
		cfg.Extra[hooks.SectionName] = []interface{}{map[string]interface{}{"id": "x", "namespace": "jobs",
			"trigger": "job.done", "action_type": actionType, "action_params": params}}
		return runtime.NewHotReloader(path, guardedOpts(path), cfg, guardedClock, nil, nil, nil)
	}
	// A section this build cannot load (shell has no runner) must not echo what it holds.
	_, err := admittedHookBindings(reloaderWith("shell", map[string]interface{}{"command": canary}))()
	if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "hooks: the admitted [hooks] section does not parse") {
		t.Fatalf("unloadable section: %v; want the fixed text", err)
	}
	// A pii tag in a hook param refuses the bindings with a fixed text too.
	_, err = admittedHookBindings(reloaderWith("plugin-call", map[string]interface{}{hooks.PluginParamName: "p",
		hooks.PluginToolParam: "t", "v": `<pii kind="ssn">` + canary + `</pii>`}))()
	if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "hooks: the admitted vault bindings are invalid") {
		t.Fatalf("pii binding: %v; want the fixed text", err)
	}
	if err != nil && strings.Contains(err.Error(), canary) {
		t.Fatalf("error text quotes a configured value: %v", err)
	}
}
