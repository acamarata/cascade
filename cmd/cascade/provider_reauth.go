// Purpose: `cascade provider reauth <name>` (07-CLI-COMMAND-TREE §provider
//   Round-16, P1-WID-01, legacy S-123.T1): the composition-root wiring for
//   internal/providers/intake.Reauth -- cobra flags mirroring `provider
//   add`'s --oauth/--key/--key-env/--no-verify, the SAME production Deps
//   `add` builds (buildIntakeDeps) over the SAME durable registry
//   (resolveProviderRegistry), and the --json/table rendering. With no
//   credential flag (the form the status widget launches) the mode is
//   inferred from the stored record by intake.InferReauthMode BEFORE
//   custody is selected, so a refusal reads no stdin and opens no vault.
// Inputs: cobra args/flags; providerDeps (provider_cmd.go), unchanged.
// Outputs: process output via internal/output.Writer.
// Constraints: a credential value is never accepted as a positional
//   argument -- identical rule to `provider add` (--key reads stdin), and
//   never rendered: the view names the vault REFERENCE only. intake.Reauth
//   writes no lane state (R-14.324); the only lane write is the one
//   `provider add` also makes, registryAdapter.UpsertProvider ->
//   upsertProviderLane: available when verified, unknown under --no-verify.
// SPORT: cli.provider.reauth/ADD (P1-WID-01).

package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// providerReauthFlags holds one invocation's flag values.
type providerReauthFlags struct {
	credentialFlags
	noVerify bool
}

func newProviderReauthCmd(deps providerDeps) *cobra.Command {
	var flags providerReauthFlags
	cmd := &cobra.Command{
		Use:   "reauth <name> [--key | --key-env VAR | --oauth] [--no-verify]",
		Short: "Re-authorize an existing provider's credential",
		Long: "Re-run the intake auth path against an EXISTING provider record named\n" +
			"<name> -- the same --key/--key-env/--oauth sources `provider add` uses,\n" +
			"against a record that must already exist (`provider add` first for a new\n" +
			"one). Skips the shape probe and model enumeration: the existing record's\n" +
			"driver and endpoint are authoritative. Re-runs the live micro-verify, and\n" +
			"only after it passes replaces the credential and its vault reference in\n" +
			"place; every other field (driver, endpoint, models, pool) is unchanged. A\n" +
			"failed verify changes neither the record nor the stored credential.\n" +
			"Reauth itself stores no lane state: like `provider add`, a verified reauth\n" +
			"marks the provider's lane available, --no-verify marks it unknown, and a\n" +
			"failed verify leaves it as it was. --no-verify skips the live check and\n" +
			"records a warning. --oauth refuses a provider whose driver is not the\n" +
			"OAuth family's.\n\n" +
			"With no credential flag the mode comes from the stored record: an OAuth\n" +
			"record runs the OAuth flow; a key record is refused with the --key-env\n" +
			"remedy (a key is never read from the terminal); any other record is\n" +
			"refused. CASCADE_NO_INPUT=1 makes --oauth and the flagless form a hard\n" +
			"error, citing --key/--key-env, with no silent fallback to another mode.",
		Example: "  cascade provider reauth myclaude --key-env MY_KEY\n" +
			"  cascade provider reauth anthropic-main",
		Args:        usageArgs(cobra.ExactArgs(1)),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProviderReauth(cmd, deps, args[0], flags)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&flags.key, "key", false, "read the credential from stdin")
	f.StringVar(&flags.keyEnv, "key-env", "", "read the credential once from the named environment variable")
	f.BoolVar(&flags.oauth, "oauth", false, "run the PKCE loopback OAuth flow")
	f.BoolVar(&flags.noVerify, "no-verify", false, "skip the live micro-verify call")
	return cmd
}

// runProviderReauth builds the ReauthRequest and production Deps, over the
// SAME durable registry `provider add` writes through, and runs Reauth. The
// flagless form resolves its mode first, so every refusal precedes custody.
func runProviderReauth(cmd *cobra.Command, deps providerDeps, name string, flags providerReauthFlags) error {
	name, err := resolveReauthTarget(cmd.Context(), deps, name) // a widget ref names one provider (P1-WID-08)
	if err != nil {
		return err
	}
	req, err := buildReauthRequest(deps, name, flags)
	if err != nil {
		return err
	}
	if req.Credential == intake.CredentialUnset {
		if req.Credential, err = flaglessReauthMode(cmd.Context(), deps, name); err != nil {
			return err
		}
	}
	reg, closeReg, err := resolveProviderRegistry(cmd.Context(), deps)
	if err != nil {
		return err
	}
	defer func() { _ = closeReg() }()

	intakeDeps, err := buildIntakeDeps(deps, reg)
	if err != nil {
		return err
	}
	result, err := intake.Reauth(cmd.Context(), intakeDeps, req)
	if err != nil {
		return err
	}
	// AddResult with Status "reauthorized": the same credential-safe view
	// `provider add` renders (provider_view.go).
	return vaultOutputWriter(cmd).Result(providerAddView{AddResult: result})
}

// buildReauthRequest resolves the credential flags through the SAME helper
// `provider add` uses (resolveCredentialFlags, provider_cmd.go).
func buildReauthRequest(deps providerDeps, name string, flags providerReauthFlags) (intake.ReauthRequest, error) {
	mode, keyValue, keyEnv, err := resolveCredentialFlags(deps, "provider reauth", flags.credentialFlags, true)
	if err != nil {
		return intake.ReauthRequest{}, err
	}
	return intake.ReauthRequest{
		Name: name, Credential: mode, KeyValue: keyValue, KeyEnvVar: keyEnv, NoVerify: flags.noVerify,
	}, nil
}

// flaglessReauthMode infers the flagless form's mode (intake.InferReauthMode)
// through a registry-only handle on providers.db. openProviderStorage is not
// used here because it builds the health egress gate, which selects custody;
// a flagless refusal must come before any custody selection.
func flaglessReauthMode(ctx context.Context, deps providerDeps, name string) (intake.CredentialMode, error) {
	if deps.Registry != nil {
		return intake.InferReauthMode(ctx, deps.Registry, deps.Getenv, name)
	}
	dataDir := deps.Paths.DataDir()
	if dataDir == "" {
		return intake.CredentialUnset, cascade.New(cascade.KindUnavailable, "provider: could not resolve the cascade data directory")
	}
	// As openProviderStorage does: a fresh install has no data directory yet.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return intake.CredentialUnset, cascade.Wrap(cascade.KindUnavailable, err, "provider: create data directory")
	}
	clock := runtime.NewSystemClock()
	db, err := openMigratedDB(ctx, filepath.Join(dataDir, providerRegistryDBFile),
		func(ctx context.Context, db *sql.DB) error {
			return registry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", "")
		})
	if err != nil {
		return intake.CredentialUnset, err
	}
	defer func() { _ = db.Close() }()
	return intake.InferReauthMode(ctx, newRegistryAdapter(registry.NewRegistry(db, clock)), deps.Getenv, name)
}
