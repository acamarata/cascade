// Purpose: fixtures for the fleet-topology doctor mount tests: a migrated
//
//	cascade.db seeded under t.TempDir() with one violation per check, a row
//	dump for before/after compares, the doctor deps driving the REAL
//	productionCheckRegistry and a decoder for the --json report.
//
// SPORT: cmd/cascade doctor topology mounts/ADDED (test fixtures).

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

// topologyCanary must never appear in doctor output.
const topologyCanary = "canary-topology-value-5c1f9e"

// topologyCheckNames are the four group-providers checks, sorted.
var topologyCheckNames = []string{
	"credential_without_domain", "discover_domain_above_public",
	"domain_without_credential", "free_domain_above_public",
}

// topologyDoctorDeps drives the REAL productionCheckRegistry over paths,
// with the canary in the environment the command sees.
func topologyDoctorDeps(t *testing.T, paths runtime.PathProvider) doctorDeps {
	t.Helper()
	useTempCustody(t)
	return doctorDeps{
		Registry: func(ctx context.Context) (*doctor.CheckRegistry, error) {
			return productionCheckRegistry(ctx, paths, doctorTestClock())
		},
		Paths:      paths,
		Getenv:     func(key string) string { return map[string]string{"CASCADE_CANARY": topologyCanary}[key] },
		Environ:    func() []string { return []string{"CASCADE_CANARY=" + topologyCanary} },
		Clock:      doctorTestClock(),
		ConfirmFix: func(*cobra.Command) (bool, error) { return true, nil },
		BundleDir:  t.TempDir(),
	}
}

// seedTopologyDB writes one violation per check into a migrated cascade.db.
func seedTopologyDB(t *testing.T, paths runtime.PathProvider) {
	t.Helper()
	ctx := context.Background()
	db := openSeedDB(t, paths)
	defer func() { _ = db.Close() }()
	store := topology.NewStore(db)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("seeding topology: %v", err)
		}
	}
	must(store.UpsertAccount(ctx, topology.Account{ID: "acct", Provider: "p",
		Billing: topology.BillingInfo{Kind: topology.BillingAPI}, Role: topology.AccountRoleWorkforce}))
	must(store.UpsertRuntimeProfile(ctx, topology.RuntimeProfile{ID: "rp", Runtime: topology.RuntimeAPI}))
	for id, tier := range map[topology.DomainID]topology.BillingTier{
		"d-free": topology.BillingTierFree, "d-disc": topology.BillingTierDiscover, "d-lonely": topology.BillingTierPaid,
	} {
		must(store.UpsertQuotaDomain(ctx, topology.QuotaDomain{ID: id, AccountRef: "acct",
			Kind: topology.QuotaDomainAPIProject, BillingTier: tier}))
	}
	for id, domain := range map[topology.CredentialID]topology.DomainID{
		"c-free": "d-free", "c-disc": "d-disc", "c-orphan": "d-missing",
	} {
		must(store.UpsertCredential(ctx, topology.Credential{ID: id, AccountRef: "acct", QuotaDomainRef: domain,
			SecretRef: "vault-key-name", RuntimeProfileRef: "rp", Health: topology.CredentialOK,
			QuarantineReason: topologyCanary}))
	}
	seedReservations(t, db, map[string]economics.ReservationState{
		"r-free": economics.ReservationHeld, "r-disc": economics.ReservationHeld,
	}, map[string]string{"r-free": "d-free", "r-disc": "d-disc"}, "internal")
}

// seedReservations inserts one ledger row per id in its state, on the
// domain domains names, at sensitivity.
func seedReservations(t *testing.T, db *sql.DB, states map[string]economics.ReservationState, domains map[string]string, sensitivity string) {
	t.Helper()
	ledger, err := economics.NewReservationStore(db, doctorTestClock())
	if err != nil {
		t.Fatal(err)
	}
	for id, state := range states {
		if !state.Valid() {
			t.Fatalf("seed state %q is not a valid ReservationState", state)
		}
		_, err := ledger.Insert(context.Background(), economics.Reservation{ID: id, ExecutionID: "x-" + id,
			DomainID: domains[id], Kind: economics.ReservationBatch, State: state, Sensitivity: sensitivity})
		if err != nil {
			t.Fatalf("seeding reservation %s: %v", id, err)
		}
	}
}

// openSeedDB opens and migrates cascade.db the way the daemon does.
func openSeedDB(t *testing.T, paths runtime.PathProvider) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(paths.DataDir(), "cascade.db"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, apply := range []func(context.Context, *sql.DB, migrate.Dialect, migrate.Clock, string, string) error{
		economics.ApplyReservationSchema, topology.ApplyMigrationSchema,
	} {
		if err := apply(context.Background(), db, migrate.SQLiteEmitter{}, doctorTestClock(), "", ""); err != nil {
			_ = db.Close()
			t.Fatalf("migrating the seed store: %v", err)
		}
	}
	return db
}

// topologySnapshot dumps every row the checks read, for a before/after compare.
func topologySnapshot(t *testing.T, paths runtime.PathProvider) string {
	t.Helper()
	src := topologyDoctorSourceFor(paths, doctorTestClock())
	ctx := context.Background()
	domains, derr := src.ListQuotaDomains(ctx)
	creds, cerr := src.ListCredentials(ctx)
	res, rerr := src.ListReservations(ctx)
	if derr != nil || cerr != nil || rerr != nil {
		t.Fatalf("reading the topology back: %v %v %v", derr, cerr, rerr)
	}
	b, err := json.Marshal([]any{domains, creds, res})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// doctorJSONEntries runs doctor with args under --json and returns the
// raw output and the report entries keyed by name.
func doctorJSONEntries(t *testing.T, deps doctorDeps, args ...string) (string, map[string]doctor.ReportEntry, error) {
	t.Helper()
	out, runErr := execRootDoctor(t, deps, append([]string{"--json", "doctor"}, args...)...)
	var env struct {
		Data doctor.RunReport `json:"data"`
	}
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&env); err != nil {
		t.Fatalf("decode envelope: %v (run error %v)\noutput:\n%s", err, runErr, out)
	}
	entries := map[string]doctor.ReportEntry{}
	for _, e := range env.Data.Entries {
		entries[e.Name] = e
	}
	return out, entries, runErr
}

// doctorEntryNames returns the sorted entry names of a --json doctor run.
func doctorEntryNames(t *testing.T, deps doctorDeps, args ...string) []string {
	t.Helper()
	_, entries, _ := doctorJSONEntries(t, deps, args...)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// assertTopologyStatus fails unless each of the four checks is present in
// entries with status want.
func assertTopologyStatus(t *testing.T, entries map[string]doctor.ReportEntry, want doctor.Status, out string) {
	t.Helper()
	for _, name := range topologyCheckNames {
		e, ok := entries[name]
		if !ok || e.Result.Status != want {
			t.Fatalf("%s: present=%v status=%q, want %q; output:\n%s", name, ok, e.Result.Status, want, out)
		}
	}
}
