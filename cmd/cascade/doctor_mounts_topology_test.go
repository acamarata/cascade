// Purpose: tests for the fleet-topology doctor mounts and `doctor
//
//	--providers`: the REAL productionCheckRegistry mounts the four
//	group-providers checks of internal/doctor/topologycheck over a real
//	cascade.db seeded under t.TempDir(), they report the seeded violations,
//	--fix leaves every row as it was, no canary value reaches the output,
//	the reservation reader covers every ledger state and --providers runs
//	exactly them.
//
// SPORT: cmd/cascade doctor topology mounts/ADDED (tests).

package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// topologycheckPkg is the only package the four mounted checks may come from.
const topologycheckPkg = "github.com/acamarata/cascade/internal/doctor/topologycheck"

// assertTopologyProvenance fails unless reg holds the four checks and each
// registered value is a type of package topologycheck.
func assertTopologyProvenance(t *testing.T, reg *doctor.CheckRegistry) {
	t.Helper()
	for _, name := range topologyCheckNames {
		check, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered on the real productionCheckRegistry", name)
		}
		v := reflect.ValueOf(check)
		if v.Kind() == reflect.Pointer {
			v = v.Elem()
		}
		if got := v.Type().PkgPath(); got != topologycheckPkg {
			t.Fatalf("%s comes from package %q, want %q", name, got, topologycheckPkg)
		}
	}
}

// assertUnreadableStoreFailsClosed writes a cascade.db that is not a
// database, asserts all four checks report StatusError, removes it again
// and returns the output.
func assertUnreadableStoreFailsClosed(t *testing.T, deps doctorDeps, paths runtime.PathProvider) string {
	t.Helper()
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, entries, err := doctorJSONEntries(t, deps, "--providers")
	if err == nil {
		t.Fatalf("doctor --providers over an unreadable cascade.db exited ok; output:\n%s", out)
	}
	assertTopologyStatus(t, entries, doctor.StatusError, out)
	if err := os.Remove(dbPath); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestDoctorMountsTopologyChecks drives the REAL registry and `doctor`
// entry point: the four topologycheck checks are mounted, are ok with no
// cascade.db, fail closed over an unreadable one, report every seeded
// violation, leave every row unchanged under --fix and never print the
// canary.
func TestDoctorMountsTopologyChecks(t *testing.T) {
	paths := doctorTestPaths(t)
	deps := topologyDoctorDeps(t, paths)
	reg, err := productionCheckRegistry(context.Background(), paths, doctorTestClock())
	if err != nil {
		t.Fatalf("productionCheckRegistry: %v", err)
	}
	assertTopologyProvenance(t, reg)

	fresh, entries, err := doctorJSONEntries(t, deps, "--providers")
	if err != nil {
		t.Fatalf("with no cascade.db yet, doctor --providers = %v, want ok; output:\n%s", err, fresh)
	}
	assertTopologyStatus(t, entries, doctor.StatusOK, fresh)
	unreadable := assertUnreadableStoreFailsClosed(t, deps, paths)

	seedTopologyDB(t, paths)
	before := topologySnapshot(t, paths)
	out, entries, err := doctorJSONEntries(t, deps, "--providers", "--fix")
	if err == nil {
		t.Fatalf("doctor --providers over seeded violations exited ok; output:\n%s", out)
	}
	for _, want := range []string{"r-free", "r-disc", "c-orphan", "d-lonely", "vault-key-name"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor --providers did not report %s; output:\n%s", want, out)
		}
	}
	for _, name := range topologyCheckNames {
		if entries[name].Fixed {
			t.Fatalf("%s was marked fixed; output:\n%s", name, out)
		}
	}
	if after := topologySnapshot(t, paths); after != before {
		t.Fatalf("doctor --providers --fix changed the topology:\nbefore %s\nafter  %s", before, after)
	}
	full, _ := execRootDoctor(t, deps, "--json", "doctor")
	for _, text := range []string{unreadable, out, full} {
		if strings.Contains(text, topologyCanary) {
			t.Fatalf("doctor output carries the canary value:\n%s", text)
		}
	}
}

// seedFreeDomainLedger seeds one free-tier quota domain and one ledger row
// in each of the five states on it: the live rows public, the released
// and rolled_back rows internal.
func seedFreeDomainLedger(t *testing.T, paths runtime.PathProvider) map[string]economics.ReservationState {
	t.Helper()
	ctx := context.Background()
	db := openSeedDB(t, paths)
	defer func() { _ = db.Close() }()
	store := topology.NewStore(db)
	if err := store.UpsertAccount(ctx, topology.Account{ID: "acct", Provider: "p",
		Billing: topology.BillingInfo{Kind: topology.BillingAPI}, Role: topology.AccountRoleWorkforce}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertQuotaDomain(ctx, topology.QuotaDomain{ID: "d-free", AccountRef: "acct",
		Kind: topology.QuotaDomainAPIProject, BillingTier: topology.BillingTierFree}); err != nil {
		t.Fatal(err)
	}
	live := map[string]economics.ReservationState{
		"res-held": economics.ReservationHeld, "res-parked": economics.ReservationParked,
		"res-committed": economics.ReservationCommitted,
	}
	terminal := map[string]economics.ReservationState{
		"res-released": economics.ReservationReleased, "res-rolled-back": economics.ReservationRolledBack,
	}
	domains := map[string]string{}
	all := map[string]economics.ReservationState{}
	for _, set := range []map[string]economics.ReservationState{live, terminal} {
		for id, state := range set {
			domains[id], all[id] = "d-free", state
		}
	}
	seedReservations(t, db, live, domains, "public")
	seedReservations(t, db, terminal, domains, "internal")
	return all
}

// TestDoctorTopologyReservationsCoverAllStates pins ListReservations to
// every ledger state and the free-tier check to the terminal rows.
func TestDoctorTopologyReservationsCoverAllStates(t *testing.T) {
	paths := doctorTestPaths(t)
	useTempCustody(t)
	want := seedFreeDomainLedger(t, paths)
	rows, err := topologyDoctorSourceFor(paths, doctorTestClock()).ListReservations(context.Background())
	if err != nil {
		t.Fatalf("ListReservations: %v", err)
	}
	if len(rows) != len(want) {
		t.Fatalf("ListReservations returned %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for _, r := range rows {
		state, ok := want[r.ID]
		sens := map[bool]string{true: "internal", false: "public"}[state == economics.ReservationReleased || state == economics.ReservationRolledBack]
		if !ok || r.State != string(state) || r.DomainID != "d-free" || r.Sensitivity != sens {
			t.Fatalf("row %+v does not match the seeded %s row", r, state)
		}
	}
	reg, err := productionCheckRegistry(context.Background(), paths, doctorTestClock())
	if err != nil {
		t.Fatalf("productionCheckRegistry: %v", err)
	}
	check, ok := reg.Lookup("free_domain_above_public")
	if !ok {
		t.Fatal("free_domain_above_public is not registered")
	}
	res, err := check.Run(context.Background())
	if err != nil || res.Status != doctor.StatusWarn {
		t.Fatalf("free_domain_above_public = %+v, %v; want a warning", res, err)
	}
	for _, id := range []string{"res-released", "res-rolled-back"} {
		if !strings.Contains(res.Detail, "reservation "+id+" ") {
			t.Fatalf("free_domain_above_public did not report %s: %q", id, res.Detail)
		}
	}
	for _, id := range []string{"res-held", "res-parked", "res-committed"} {
		if strings.Contains(res.Detail, id) {
			t.Fatalf("free_domain_above_public reported the public row %s: %q", id, res.Detail)
		}
	}
}

// TestDoctorProvidersFlagSelectsGroup pins `doctor --providers` to exactly
// the four group-providers checks, paired with the default run, which
// carries those four and more, and refuses a --providers run that selects
// nothing.
func TestDoctorProvidersFlagSelectsGroup(t *testing.T) {
	paths := doctorTestPaths(t)
	deps := topologyDoctorDeps(t, paths)
	seedTopologyDB(t, paths)

	if got := doctorEntryNames(t, deps, "--providers"); !reflect.DeepEqual(got, topologyCheckNames) {
		t.Fatalf("doctor --providers ran %v, want exactly %v", got, topologyCheckNames)
	}
	all := doctorEntryNames(t, deps)
	if len(all) <= len(topologyCheckNames) {
		t.Fatalf("the default run carries only %v; --providers selected nothing narrower", all)
	}
	seen := map[string]bool{}
	for _, name := range all {
		seen[name] = true
	}
	for _, name := range topologyCheckNames {
		if !seen[name] {
			t.Fatalf("the default run is missing %s; got %v", name, all)
		}
	}

	only := testDoctorDeps(t, doctor.NewDoctorSelfCheck())
	out, err := execRootDoctor(t, only, "--json", "doctor", "--providers")
	if !cascade.HasKind(err, cascade.KindNotFound) || !strings.Contains(err.Error(), "no provider checks are registered") {
		t.Fatalf("--providers over a registry with no provider check: err = %v, want KindNotFound", err)
	}
	if strings.Contains(out, doctor.NewDoctorSelfCheck().Name()) {
		t.Fatalf("the refused --providers run rendered a report:\n%s", out)
	}
}
