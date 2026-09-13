// Package migration (migrate_cmd.go): MigrateV1, the orchestrator behind
// `cascade migrate v1 --from <dir> [--dry-run] [--yes]`. It runs every
// S-53.T1 v1 importer in sequence, consulting and updating the ledger
// (ledger.go) so an already-done domain is skipped and a re-run after a
// partial failure resumes only the domains that still need it.
//
// Inputs: a *LedgerStore, an ImporterFactory (the same factory shape
// cmd/cascade/migrate.go's productionMigrateFactory already implements),
// and MigrateV1Options.
// Outputs: a MigrateV1Report (per-domain outcome, --json-able) and a
// typed error when one or more domains failed, or when the caller's
// confirmation was refused.
// Constraints: dry-run never writes the ledger or any destination
// (Art.1); a domain already StatusDone is never re-invoked; a bad or
// missing v1 directory refuses before any ledger interaction (AC:
// "no ledger write" on that path, distinct from a genuine per-domain
// import failure, which DOES record StatusError for that one domain).
//
// SPORT: internal.migration.MigrateV1/ADD (P1-E26-W10-S53-T4).
package migration

import (
	"context"
	"os"
	"strings"

	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/pkg/cascade"
)

// DomainOrder is the fixed sequence MigrateV1 invokes v1 importers in,
// per this ticket's full_desc ("config -> vault.env -> accounts ->
// memory").
var DomainOrder = []migrationv1.Domain{
	migrationv1.DomainConfig,
	migrationv1.DomainVault,
	migrationv1.DomainAccounts,
	migrationv1.DomainMemory,
}

// ImporterFactory builds one domain's Importer and its store closer.
// cmd/cascade/migrate.go's productionMigrateFactory has exactly this
// shape and is passed here unmodified — the CLI factory is not
// duplicated, only reused (Art.1).
type ImporterFactory func(ctx context.Context, domain migrationv1.Domain) (migrationv1.Importer, func() error, error)

// MigrateV1Options configures one MigrateV1 call.
type MigrateV1Options struct {
	// SourceRoot is the v1 home directory to import from.
	SourceRoot string
	// DryRun computes every domain's delta without writing the ledger or
	// any destination.
	DryRun bool
	// Yes skips interactive confirmation.
	Yes bool
	// NoInput is CASCADE_NO_INPUT=1: without Yes, MigrateV1 refuses
	// rather than prompting.
	NoInput bool
	// Confirm is the injected interactive y/N prompt, called only when
	// neither Yes nor NoInput settles the question. A nil Confirm with
	// neither Yes nor NoInput set is itself a refusal (no honest default
	// exists).
	Confirm func() (bool, error)
}

// DomainOutcome is one domain's result within a MigrateV1Report.
type DomainOutcome struct {
	Domain  migrationv1.Domain       `json:"domain"`
	Skipped bool                     `json:"skipped"`
	Result  migrationv1.DryRunResult `json:"result"`
	Error   string                   `json:"error,omitempty"`
}

// MigrateV1Report is MigrateV1's full result.
type MigrateV1Report struct {
	SourceRoot string          `json:"source_root"`
	DryRun     bool            `json:"dry_run"`
	Refused    bool            `json:"refused,omitempty"`
	Domains    []DomainOutcome `json:"domains"`
}

// MigrateV1 runs every DomainOrder importer in sequence against ledger
// and factory. See the package doc comment for the full contract.
func MigrateV1(ctx context.Context, ledger *LedgerStore, factory ImporterFactory, opts MigrateV1Options) (MigrateV1Report, error) {
	report := MigrateV1Report{SourceRoot: opts.SourceRoot, DryRun: opts.DryRun}
	if factory == nil {
		return report, cascade.New(cascade.KindInternal, "cascade migrate v1: importer factory is not configured")
	}
	if ledger == nil {
		return report, cascade.New(cascade.KindInternal, "cascade migrate v1: ledger is not configured")
	}
	if err := validateSourceRoot(opts.SourceRoot); err != nil {
		return report, err
	}

	if !opts.DryRun {
		proceed, decideErr := decideMigrateProceed(opts)
		if !proceed {
			preview, _ := previewDomains(ctx, ledger, factory, opts.SourceRoot)
			report.Refused = true
			report.Domains = preview
			if decideErr == nil {
				decideErr = cascade.New(cascade.KindInvalidInput,
					"cascade migrate v1: refused; pass --yes to confirm")
			}
			return report, decideErr
		}
	}

	var firstErr error
	for _, domain := range DomainOrder {
		outcome, err := migrateOneDomain(ctx, ledger, factory, domain, opts)
		report.Domains = append(report.Domains, outcome)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		kind, ok := cascade.KindOf(firstErr)
		if !ok {
			kind = cascade.KindInternal
		}
		return report, cascade.Wrapf(kind, firstErr, "cascade migrate v1: one or more domains failed")
	}
	return report, nil
}

// validateSourceRoot refuses a missing or unreadable v1 directory before
// any domain or ledger interaction begins — distinct from a single
// domain's own import failure (migrateOneDomain), which DOES record a
// ledger row. A bad v1 directory records nothing.
func validateSourceRoot(sourceRoot string) error {
	if strings.TrimSpace(sourceRoot) == "" {
		return cascade.New(cascade.KindInvalidInput, "cascade migrate v1: --from is required")
	}
	info, err := os.Stat(sourceRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return cascade.Newf(cascade.KindNotFound,
				"cascade migrate v1: v1 directory %q does not exist", sourceRoot)
		}
		return cascade.Wrapf(cascade.KindUnavailable, err,
			"cascade migrate v1: could not read v1 directory %q", sourceRoot)
	}
	if !info.IsDir() {
		return cascade.Newf(cascade.KindInvalidInput,
			"cascade migrate v1: %q is not a directory", sourceRoot)
	}
	return nil
}

// decideMigrateProceed is the CASCADE_NO_INPUT/--yes/interactive truth
// table, mirroring instruction_regen.go's decideApply and
// cmd/cascade/backup_elevation.go's confirmBackupOperation.
func decideMigrateProceed(opts MigrateV1Options) (bool, error) {
	if opts.Yes {
		return true, nil
	}
	if opts.NoInput {
		return false, cascade.New(cascade.KindInvalidInput,
			"cascade migrate v1: refusing to run non-interactively without --yes")
	}
	if opts.Confirm == nil {
		return false, cascade.New(cascade.KindInvalidInput,
			"cascade migrate v1: no confirmation available; pass --yes")
	}
	ok, err := opts.Confirm()
	if err != nil {
		return false, err
	}
	if !ok {
		return false, cascade.New(cascade.KindPermissionDenied,
			"cascade migrate v1: operation was not confirmed")
	}
	return true, nil
}

// previewDomains computes every domain's dry-run outcome for the refusal
// report, without writing the ledger or any destination.
func previewDomains(ctx context.Context, ledger *LedgerStore, factory ImporterFactory, sourceRoot string) ([]DomainOutcome, error) {
	var outcomes []DomainOutcome
	var firstErr error
	for _, domain := range DomainOrder {
		outcome, err := migrateOneDomain(ctx, ledger, factory, domain,
			MigrateV1Options{SourceRoot: sourceRoot, DryRun: true})
		outcomes = append(outcomes, outcome)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return outcomes, firstErr
}

// migrateOneDomain runs (or skips) exactly one domain. A domain already
// StatusDone is skipped with no importer call at all. Otherwise the
// importer's own DryRun-aware Import path runs — real, zero-write when
// opts.DryRun is true (T1's TestImporter_DryRun already proves this for
// every domain) — and the ledger is written only when !opts.DryRun.
func migrateOneDomain(ctx context.Context, ledger *LedgerStore, factory ImporterFactory, domain migrationv1.Domain, opts MigrateV1Options) (DomainOutcome, error) {
	outcome := DomainOutcome{Domain: domain}
	row, found, err := ledger.ReadDomain(ctx, domain)
	if err != nil {
		outcome.Error = err.Error()
		return outcome, err
	}
	if found && row.Done() {
		outcome.Skipped = true
		outcome.Result = migrationv1.DryRunResult{Domain: domain}.Normalize()
		return outcome, nil
	}

	importer, closeStore, err := factory(ctx, domain)
	if err != nil {
		outcome.Error = err.Error()
		recordDomainFailure(ctx, ledger, domain, opts.DryRun, err)
		return outcome, err
	}
	result, importErr := importer.Import(ctx, migrationv1.Request{SourceRoot: opts.SourceRoot, DryRun: opts.DryRun})
	closeErr := closeStore()
	if importErr != nil {
		outcome.Error = importErr.Error()
		recordDomainFailure(ctx, ledger, domain, opts.DryRun, importErr)
		return outcome, importErr
	}
	outcome.Result = result.Normalize()
	if closeErr != nil {
		outcome.Error = closeErr.Error()
		recordDomainFailure(ctx, ledger, domain, opts.DryRun, closeErr)
		return outcome, closeErr
	}
	if !opts.DryRun {
		if werr := ledger.WriteDomain(ctx, LedgerRow{
			Domain: domain, Status: StatusDone, RecordCount: result.DeltaCount(),
		}); werr != nil {
			return outcome, werr
		}
	}
	return outcome, nil
}

// recordDomainFailure writes a StatusError ledger row for domain, unless
// this was a dry run (which never writes the ledger). The write's own
// error is deliberately swallowed: the real failure being reported is
// importErr/err, and a ledger-write failure on top of an already-failing
// domain must not mask it.
func recordDomainFailure(ctx context.Context, ledger *LedgerStore, domain migrationv1.Domain, dryRun bool, cause error) {
	if dryRun {
		return
	}
	_ = ledger.WriteDomain(ctx, LedgerRow{Domain: domain, Status: StatusError, Error: cause.Error()})
}
