// Purpose: the read-only data source behind the four group-providers
//
//	fleet-topology doctor checks of internal/doctor/topologycheck mounted in
//	productionCheckRegistry, and the `doctor --providers` flag that narrows
//	a run to that group through doctor.RunOptions.ProvidersOnly.
//
// Inputs: the path provider and clock doctor holds. Every read opens
//
//	<data dir>/cascade.db read-only in the mode readOnlyMode picks, queries
//	the real topology store or reservation ledger, and closes it again.
//
// Outputs: topologyDoctorSource, satisfying topologycheck.TopologyReader
//
//	and topologycheck.ReservationReader; addDoctorProvidersFlag;
//	runDoctorChecks.
//
// Constraints: no migrator, no DDL, no write: --fix can never change a
//
//	topology or ledger row. A cascade.db not yet created holds no rows; an
//	unresolved data directory, a store that exists but cannot be read and
//	an absent table are errors the checks report as StatusError, never an
//	empty "no violations" (Art.1).
//
// SPORT: cmd/cascade doctor topology mounts/ADDED.

package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/doctor/topologycheck"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// topologyDoctorStore names the store failures in doctor output.
const topologyDoctorStore = "cascade doctor topology-store"

// topologyReservationStates is every ledger state, live and terminal: the
// sensitivity checks cover released and rolled_back rows too.
var topologyReservationStates = []economics.ReservationState{
	economics.ReservationHeld, economics.ReservationParked, economics.ReservationCommitted,
	economics.ReservationReleased, economics.ReservationRolledBack,
}

// topologyDoctorSource reads the topology rows and the reservation ledger
// out of cascade.db, read-only, once per call.
type topologyDoctorSource struct {
	paths runtime.PathProvider
	clock runtime.Clock
}

var (
	_ topologycheck.TopologyReader    = topologyDoctorSource{}
	_ topologycheck.ReservationReader = topologyDoctorSource{}
)

// topologyDoctorSourceFor builds the source the topology checks read.
func topologyDoctorSourceFor(paths runtime.PathProvider, clock runtime.Clock) topologyDoctorSource {
	return topologyDoctorSource{paths: paths, clock: clock}
}

// ListQuotaDomains returns every quota-domain row.
func (s topologyDoctorSource) ListQuotaDomains(ctx context.Context) ([]topology.QuotaDomain, error) {
	var out []topology.QuotaDomain
	err := s.withDB(ctx, func(db *sql.DB) (err error) {
		if ok, err := topologyTableExists(ctx, db, "config_quota_domain"); err != nil || !ok {
			return err
		}
		out, err = topology.NewStore(db).ListQuotaDomains(ctx)
		return err
	})
	return out, err
}

// ListCredentials returns every credential row.
func (s topologyDoctorSource) ListCredentials(ctx context.Context) ([]topology.Credential, error) {
	var out []topology.Credential
	err := s.withDB(ctx, func(db *sql.DB) (err error) {
		if ok, err := topologyTableExists(ctx, db, "config_credential"); err != nil || !ok {
			return err
		}
		out, err = topology.NewStore(db).ListCredentials(ctx)
		return err
	})
	return out, err
}

// ListReservations returns every reservation row in each of the five
// ledger states.
func (s topologyDoctorSource) ListReservations(ctx context.Context) ([]topologycheck.ReservationRow, error) {
	var out []topologycheck.ReservationRow
	err := s.withDB(ctx, func(db *sql.DB) error {
		if ok, err := topologyTableExists(ctx, db, "jobs_reservation"); err != nil || !ok {
			return err
		}
		store, err := economics.NewReservationStore(db, s.clock)
		if err != nil {
			return err
		}
		for _, state := range topologyReservationStates {
			rows, err := store.ListByState(ctx, state)
			if err != nil {
				return err
			}
			for _, r := range rows {
				out = append(out, topologycheck.ReservationRow{
					ID: r.ID, DomainID: r.DomainID, State: string(r.State), Sensitivity: r.Sensitivity,
				})
			}
		}
		return nil
	})
	return out, err
}

// withDB opens cascade.db read-only, runs fn and closes the handle. A
// cascade.db that does not exist yet holds no rows, so fn is not run and
// the readers answer empty (the openDoctorStore precedent); any other
// failure to reach the store is an error.
func (s topologyDoctorSource) withDB(ctx context.Context, fn func(*sql.DB) error) error {
	if s.paths == nil || s.paths.DataDir() == "" {
		return cascade.New(cascade.KindUnavailable, topologyDoctorStore+": could not resolve the cascade data directory")
	}
	path := filepath.Join(s.paths.DataDir(), "cascade.db")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, topologyDoctorStore)
	}
	mode, err := readOnlyMode(path)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, topologyDoctorStore)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+mode+"&_pragma=busy_timeout(5000)")
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, topologyDoctorStore+": open")
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, topologyDoctorStore+": unreachable")
	}
	if err := fn(db); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, topologyDoctorStore+": read")
	}
	return nil
}

// addDoctorProvidersFlag registers `doctor --providers`.
func addDoctorProvidersFlag(cmd *cobra.Command, f *doctorFlags) {
	cmd.Flags().BoolVar(&f.providers, "providers", false,
		"run only the provider and fleet-topology checks (group providers)")
}

// runDoctorChecks runs checks under opts. A --providers run that selected
// nothing is refused rather than rendered as a clean report (Art.1).
func runDoctorChecks(ctx context.Context, checks []doctor.Check, opts doctor.RunOptions) (doctor.RunReport, error) {
	report := doctor.Run(ctx, checks, opts)
	if opts.ProvidersOnly && len(report.Entries) == 0 {
		return doctor.RunReport{}, cascade.New(cascade.KindNotFound,
			"cascade doctor: no provider checks are registered for this run")
	}
	return report, nil
}

// topologyTableExists reports whether cascade.db holds table yet. A home whose
// daemon has never started (a fresh v1 migration) has a cascade.db without the
// topology tables; it holds no rows, the same as a missing cascade.db.
func topologyTableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n)
	return n > 0, err
}
