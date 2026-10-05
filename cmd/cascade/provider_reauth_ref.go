// Purpose: resolves what `cascade provider reauth <arg>` was given to the one
//   provider it names: the provider called <arg>, or the provider whose
//   widget ref (capacity.WidgetRef) is <arg>. The status widget's rows carry
//   an opaque ref instead of a provider name that is PII-shaped (an email, a
//   host), so the widget's "re-authorize" action hands the CLI a ref the CLI
//   must be able to map back (P1-WID-08, orchestrator decision 4).
// Inputs: the context, providerDeps and the argument.
// Outputs: the provider name to re-authorize, or a refusal.
// Constraints: no daemon verb maps a ref to a name; the mapping runs in the
//   CLI over the registry it already opens. It runs BEFORE buildIntakeDeps,
//   over a registry-only handle on providers.db (openProviderStorage builds
//   the health egress gate, which selects custody), so a refusal reads no
//   stdin and selects no custody. Candidates are collected by name and
//   de-duplicated: exactly one distinct provider resolves; none is
//   KindNotFound; more than one is KindConflict naming every candidate, and
//   nothing is written. A provider literally named like another provider's
//   ref is therefore a conflict at resolution, not a name refused at
//   `provider add` (R9b m-3), so `provider add` is unchanged. A
//   deps.Registry injected by a test has no listing (intake.Registry has no
//   List), so the argument passes through unchanged there.
// SPORT: cli.provider.reauth (CHANGE, P1-WID-08).

package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// reauthRefOf computes a provider's widget ref. A test replaces it to force
// two names onto one ref; production never does.
var reauthRefOf = capacity.WidgetRef

// resolveReauthTarget returns the provider name arg stands for.
func resolveReauthTarget(ctx context.Context, deps providerDeps, arg string) (string, error) {
	if deps.Registry != nil {
		return arg, nil
	}
	names, err := listProviderNames(ctx, deps)
	if err != nil {
		return "", err
	}
	return pickReauthTarget(arg, names, reauthRefOf)
}

// pickReauthTarget applies the resolution rule to names: arg is the target
// when it names a provider or equals exactly one provider's ref.
func pickReauthTarget(arg string, names []string, refOf func(string) string) (string, error) {
	seen := map[string]bool{}
	var hits []string
	for _, n := range names {
		if (n == arg || refOf(n) == arg) && !seen[n] {
			seen[n] = true
			hits = append(hits, n)
		}
	}
	sort.Strings(hits)
	switch len(hits) {
	case 0:
		// The same refusal intake.Reauth returns for a name with no record,
		// raised here so it comes before custody is selected.
		return "", cascade.Wrapf(cascade.KindNotFound, intake.ErrUnknownProvider,
			"intake: unknown provider %q: run `cascade provider add` first", arg)
	case 1:
		return hits[0], nil
	}
	quoted := make([]string, len(hits))
	for i, h := range hits {
		quoted[i] = `"` + h + `"`
	}
	return "", cascade.Newf(cascade.KindConflict,
		"provider reauth: %q is ambiguous: it matches providers %s (a name, or a widget ref); nothing was changed, name one provider by its exact name",
		arg, strings.Join(quoted, " and "))
}

// listProviderNames returns every provider name in providers.db through a
// registry-only handle (see this file's Constraints).
func listProviderNames(ctx context.Context, deps providerDeps) ([]string, error) {
	dataDir := deps.Paths.DataDir()
	if dataDir == "" {
		return nil, cascade.New(cascade.KindUnavailable, "provider: could not resolve the cascade data directory")
	}
	// As flaglessReauthMode does: a fresh install has no data directory yet.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "provider: create data directory")
	}
	clock := runtime.NewSystemClock()
	db, err := openMigratedDB(ctx, filepath.Join(dataDir, providerRegistryDBFile),
		func(ctx context.Context, db *sql.DB) error {
			return registry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", "")
		})
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	recs, err := registry.NewRegistry(db, clock).ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(recs))
	for i, r := range recs {
		names[i] = r.Name
	}
	return names, nil
}
