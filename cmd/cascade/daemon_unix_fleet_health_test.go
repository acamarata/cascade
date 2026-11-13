//go:build !windows

package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
)

// onlyFleetHealthRegistration narrows the registrations to the production
// fleet-health entry for this test, so a bare wiring can run through it.
func onlyFleetHealthRegistration(t *testing.T) {
	t.Helper()
	saved := daemonRegistrations
	var only []daemonRegistration
	for _, r := range saved {
		if r.Name == "fleet-health" {
			only = append(only, r)
		}
	}
	if len(only) != 1 {
		t.Fatalf("fleet-health registrations = %d, want 1", len(only))
	}
	daemonRegistrations = only
	t.Cleanup(func() { daemonRegistrations = saved })
}

// assertNoLocalRegistry fails on any package-level declaration of a second
// RPC registry or its legacy constructors in cmd/cascade.
func assertNoLocalRegistry(t *testing.T) {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("package files = %v, %v", files, err)
	}
	banned := map[string]bool{"RPCRegistry": true, "NewRPCRegistry": true, "daemonRegistry": true,
		"DaemonRegistry": true, "RegisterFleetHealthSummary": true}
	for _, name := range files {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range declNames(f) {
			if banned[decl] {
				t.Errorf("%s declares %s", name, decl)
			}
		}
	}
}

// declNames lists the top-level func, type and var names of f.
func declNames(f *ast.File) []string {
	var out []string
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
			out = append(out, fd.Name.Name)
		}
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			if ts, ok := spec.(*ast.TypeSpec); ok {
				out = append(out, ts.Name.Name)
			}
			if vs, ok := spec.(*ast.ValueSpec); ok {
				for _, n := range vs.Names {
					out = append(out, n.Name)
				}
			}
		}
	}
	return out
}

func TestEXE14LegacyAC20(t *testing.T) {
	w, _ := composeTopology(t)
	if w.Deps.Fleet == nil || !w.Registry.Registered(rpc.FleetHealthSummaryMethod) {
		t.Fatal("the composed daemon does not serve fleet.health.summary")
	}
	onlyFleetHealthRegistration(t)
	bare := &daemonWiring{Ctx: w.Ctx, Registry: rpc.NewRegistry(), Manifest: testManifest()}
	if err := runDaemonWiring(bare); err != nil {
		t.Fatal(err)
	}
	if bare.Registry.Registered(rpc.FleetHealthSummaryMethod) {
		t.Fatal("a daemon without a fleet runtime registered fleet.health.summary")
	}
	skipped := false
	for _, s := range bare.Manifest.Snapshot() {
		skipped = skipped || (s.Name == "fleet-health" && s.State == daemon.SubsystemSkipped)
	}
	if !skipped {
		t.Fatalf("manifest = %+v, want fleet-health skipped", bare.Manifest.Snapshot())
	}
	assertNoLocalRegistry(t)
}

func TestEXE14ProductionHandoff(t *testing.T) {
	w, _ := composeTopology(t)
	health, err := w.Deps.Fleet.Health(w.Ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := principal.WithCaller(w.Ctx, principal.Caller{OwnerCLI: true})
	result, eo := w.Registry.Dispatch(owner, &rpc.Request{Method: rpc.FleetHealthSummaryMethod})
	if eo != nil {
		t.Fatalf("dispatch refused: %+v", eo)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Status string                       `json:"status"`
		Keys   []map[string]json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(raw, &got); err != nil || got.Status != "ok" || len(got.Keys) != 5 {
		t.Fatalf("reply = %s, %v", raw, err)
	}
	assertHealthKeysCopied(t, got.Keys, health)
}

// assertHealthKeysCopied checks the reply's five keys, in order, against the
// runtime's own Health reading; an unknown percentage must be absent.
func assertHealthKeysCopied(t *testing.T, keys []map[string]json.RawMessage, health map[economics.HealthKey]economics.KeyHealth) {
	t.Helper()
	order := []economics.HealthKey{economics.HealthKeyExecutive, economics.HealthKeyBuild,
		economics.HealthKeyBulk, economics.HealthKeyAdversary, economics.HealthKeyLongContext}
	states := map[string]bool{"unavailable": true, "protected": true, "scarce": true,
		"abundant": true, "available": true, "healthy": true}
	unknown := 0
	for i, key := range order {
		var name, state string
		if json.Unmarshal(keys[i]["key"], &name) != nil || name != string(key) {
			t.Fatalf("key %d = %s, want %s", i, keys[i]["key"], key)
		}
		if json.Unmarshal(keys[i]["state"], &state) != nil || !states[state] || state != string(health[key].State) {
			t.Fatalf("%s state = %s, want %s", key, keys[i]["state"], health[key].State)
		}
		pct, present := keys[i]["percentage"]
		switch want := health[key].Percentage; {
		case want == nil && present:
			t.Fatalf("%s has unknown quota but percentage %s", key, pct)
		case want == nil:
			unknown++
		case !present:
			t.Fatalf("%s lost its percentage %v", key, *want)
		}
	}
	if unknown == 0 {
		t.Fatal("the fixture has no unknown-quota key; the omission is unproven")
	}
}
