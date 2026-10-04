// Purpose: `cascade provider add` (07-CLI-COMMAND-TREE §provider): the
//   composition-root wiring for the internal/providers/intake orchestrator
//   - cobra flags, the production Deps (real vault broker, real egress
//   engine, the durable registry resolved by resolveProviderRegistry --
//   see provider_registry_adapter.go), and the --json/table rendering.
// Inputs: cobra args/flags; a providerDeps injected at construction so a
//   test never touches the real keychain, network, or environment.
// Outputs: process output via internal/output.Writer.
// Constraints: a credential value is never accepted as a positional
//   argument; --key reads from stdin, matching vault.go's own rule.
// SPORT: cli.provider.add/ADD (P1-E10-W3-S20-T1).

package main

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// intakeHTTPTimeout bounds every shape-probe/verify call.
const intakeHTTPTimeout = 20 * time.Second

// providerDeps carries provider_cmd.go's external inputs, mirroring
// vault.go's vaultDeps pattern so a test never touches the real keychain,
// network, or environment - Doer and Registry are the seams a CLI test
// substitutes to avoid a live HTTP call and a shared process registry.
type providerDeps struct {
	Paths        runtime.PathProvider
	NewCustody   func() (secrets.Custody, error)
	Gate         secrets.ElevationGate
	Getenv       func(string) string
	ReadStdin    func() ([]byte, error)
	StdinIsPiped func() bool
	Doer         intake.Doer
	// Registry, when non-nil, replaces the durable providers.db registry
	// (resolveProviderRegistry). Production leaves it nil.
	Registry intake.Registry
	// NewOAuthBroker, when non-nil, replaces secrets.NewOAuthBroker so a
	// test drives the OAuth path without a browser. Production leaves it nil.
	NewOAuthBroker func(provider.ProviderOAuthConfig, secrets.OAuthDeps) (provider.OAuthBroker, error)
	// HealthHTTPDoer is `provider test`'s reachability transport
	// (httpReachabilityProber). Nil means a real *http.Client; tests
	// substitute a fake so TestProviderTest_* opens no socket (Art.7.2).
	HealthHTTPDoer httpDoer
}

// productionProviderDeps builds providerDeps against the real environment.
func productionProviderDeps() providerDeps {
	return providerDepsFor(lazyPaths{})
}

// mountProviderCmd attaches the `provider` command tree, following
// mountVaultCmd's pattern.
func mountProviderCmd(root *cobra.Command) {
	cmd := newProviderCmd(productionProviderDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// newProviderCmd builds the provider noun and mounts `add` plus the
// list/test/remove/health/usage leaves (P1-E10-W3-S21-T2,
// mountProviderQueryCmds in provider_health_cmd.go).
func newProviderCmd(deps providerDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Register and manage model providers",
		Long: "Register a model provider through the universal intake path.\n\n" +
			"`provider add` accepts a key (via --key or --key-env) or an OAuth grant\n" +
			"(--oauth), probes the credential's shape against anthropic-compat,\n" +
			"openai-compat and gemini in that order, enumerates models, and stores the\n" +
			"result. Every flag has a non-interactive equivalent: --key-env names an\n" +
			"environment variable (never a literal value), and CASCADE_NO_INPUT=1\n" +
			"makes --oauth's interactive browser step a hard error.\n\n" +
			"`provider list/test/remove/health/usage` operate on the local registry,\n" +
			"health and usage-accounting domains directly (no daemon RPC as of this\n" +
			"ticket) -- see docs/provider-guide.md and .github/wiki/Provider-Guide.md.",
		Annotations: map[string]string{"local": "true"},
	}
	cmd.AddCommand(newProviderAddCmd(deps))
	cmd.AddCommand(newProviderReauthCmd(deps))
	mountProviderQueryCmds(cmd, deps)
	return cmd
}

// providerAddFlags holds one invocation's flag values.
type providerAddFlags struct {
	credentialFlags
	baseURL  string
	kind     string
	noVerify bool
	pool     string
}

func newProviderAddCmd(deps providerDeps) *cobra.Command {
	var flags providerAddFlags
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Register a provider via the universal intake path",
		Long: "Register a provider named <name>.\n\n" +
			"Exactly one of --key (reads a value from stdin), --key-env <VAR> (reads\n" +
			"the named environment variable once, non-interactively), or --oauth\n" +
			"(runs a PKCE browser flow; refuses under CASCADE_NO_INPUT=1, citing\n" +
			"--key/--key-env as alternatives) selects the credential source.\n" +
			"--base-url overrides the driver's default API root. --kind pins the\n" +
			"driver shape, so the probe tries that one and a mismatch is reported\n" +
			"as a wrong pin rather than as a credential nothing matched.\n" +
			"--no-verify skips\n" +
			"the live 1-token verify call and records a warning. --pool <name> joins\n" +
			"a key-pool lane. Re-adding an existing name re-verifies and updates the\n" +
			"record rather than creating a duplicate.",
		Example: "  cascade vault set MY_KEY < key.txt\n" +
			"  cascade provider add myclaude --key-env MY_KEY\n" +
			"  cascade provider add myclaude --key-env MY_KEY --no-verify --json",
		Args:        usageArgs(cobra.ExactArgs(1)),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProviderAdd(cmd, deps, args[0], flags)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&flags.key, "key", false, "read the credential from stdin")
	f.StringVar(&flags.keyEnv, "key-env", "", "read the credential once from the named environment variable")
	f.BoolVar(&flags.oauth, "oauth", false, "run the PKCE loopback OAuth flow")
	f.StringVar(&flags.baseURL, "base-url", "", "override the driver's default API root")
	f.StringVar(&flags.kind, "kind", "", "pin the driver shape instead of letting the probe decide")
	f.BoolVar(&flags.noVerify, "no-verify", false, "skip the live micro-verify call")
	f.StringVar(&flags.pool, "pool", "", "join the named key-pool")
	return cmd
}

// runProviderAdd builds the AddRequest and production Deps and runs Add,
// writing through resolveProviderRegistry's registry -- the fix for the
// disclosed add/list split (provider_registry_adapter.go's header comment).
func runProviderAdd(cmd *cobra.Command, deps providerDeps, name string, flags providerAddFlags) error {
	req, err := buildAddRequest(deps, name, flags)
	if err != nil {
		return err
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
	result, err := intake.Add(cmd.Context(), intakeDeps, req)
	if err != nil {
		return err
	}
	return vaultOutputWriter(cmd).Result(providerAddView{AddResult: result})
}

// resolveProviderRegistry returns the intake.Registry `add` writes through
// and a closer the caller MUST defer. deps.Registry, when set, is used
// unchanged (the CLI-unit-test seam); production opens the durable
// providerStorage (provider_health_cmd.go's openProviderStorage, the same
// composition root list/test/remove/health/usage already use) and wraps
// its Registry in a registryAdapter, so an added provider is durable and
// visible to every other subcommand.
func resolveProviderRegistry(ctx context.Context, deps providerDeps) (intake.Registry, func() error, error) {
	if deps.Registry != nil {
		return deps.Registry, func() error { return nil }, nil
	}
	store, err := openProviderStorage(ctx, deps)
	if err != nil {
		return nil, nil, err
	}
	return newRegistryAdapter(store.Registry), store.Close, nil
}

// buildAddRequest resolves the credential flags (resolveCredentialFlags,
// shared with `provider reauth`) into an intake.AddRequest.
func buildAddRequest(deps providerDeps, name string, flags providerAddFlags) (intake.AddRequest, error) {
	mode, keyValue, keyEnv, err := resolveCredentialFlags(deps, "provider add", flags.credentialFlags, false)
	if err != nil {
		return intake.AddRequest{}, err
	}
	return intake.AddRequest{
		Name: name, Credential: mode, KeyValue: keyValue, KeyEnvVar: keyEnv,
		BaseURL: flags.baseURL, Kind: intake.DriverKind(flags.kind),
		NoVerify: flags.noVerify, Pool: flags.pool,
	}, nil
}

// credentialFlags is the credential-source trio add and reauth share.
type credentialFlags struct {
	key    bool
	keyEnv string
	oauth  bool
}

// resolveCredentialFlags maps exactly one set credential flag to its intake
// mode (--key reads stdin, never argv); verb prefixes every refusal. With
// flagless, no flag at all yields CredentialUnset and reads nothing.
func resolveCredentialFlags(deps providerDeps, verb string, f credentialFlags, flagless bool) (intake.CredentialMode, []byte, string, error) {
	set := 0
	for _, on := range []bool{f.key, f.keyEnv != "", f.oauth} {
		if on {
			set++
		}
	}
	if set == 0 && flagless {
		return intake.CredentialUnset, nil, "", nil
	}
	if set != 1 {
		return intake.CredentialUnset, nil, "", cascade.Newf(cascade.KindInvalidInput, "%s: %s of --key, --key-env or --oauth",
			verb, map[bool]string{false: "pass exactly one", true: "pass at most one"}[flagless])
	}
	switch {
	case f.key:
		v, err := readKeyFlagValue(deps, verb)
		return intake.CredentialKey, v, "", err
	case f.keyEnv != "":
		return intake.CredentialKeyEnv, nil, strings.TrimSpace(f.keyEnv), nil
	default:
		return intake.CredentialOAuth, nil, "", nil
	}
}

// readKeyFlagValue reads --key from stdin, refusing a TTY under CASCADE_NO_INPUT=1
// and an empty value (parity with --key-env: never overwrite a working key).
func readKeyFlagValue(deps providerDeps, verb string) ([]byte, error) {
	piped := deps.StdinIsPiped == nil || deps.StdinIsPiped()
	if deps.Getenv != nil && deps.Getenv("CASCADE_NO_INPUT") == "1" && !piped {
		return nil, cascade.Wrap(cascade.KindUnavailable, intake.ErrNoInputInteractive,
			verb+": --key would block on an interactive TTY, which CASCADE_NO_INPUT=1 forbids; use --key-env instead")
	}
	raw, err := deps.ReadStdin()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, verb+": reading --key from stdin")
	}
	v := trimValue(raw)
	if strings.TrimSpace(string(v)) == "" {
		return nil, cascade.New(cascade.KindInvalidInput, verb+": the value --key read from stdin is empty")
	}
	return v, nil
}

// buildIntakeDeps wires the production intake.Deps: a real vault broker
// over deps' custody/gate, the process egress engine, and reg (resolved by
// resolveProviderRegistry).
func buildIntakeDeps(deps providerDeps, reg intake.Registry) (intake.Deps, error) {
	custody, err := deps.NewCustody()
	if err != nil {
		return intake.Deps{}, err
	}
	broker, err := secrets.NewBroker(custody, deps.Gate)
	if err != nil {
		return intake.Deps{}, err
	}
	engine, err := buildProviderEgressEngine(broker)
	if err != nil {
		return intake.Deps{}, err
	}
	newOAuth := deps.NewOAuthBroker
	if newOAuth == nil {
		newOAuth = func(cfg provider.ProviderOAuthConfig, oa secrets.OAuthDeps) (provider.OAuthBroker, error) {
			return secrets.NewOAuthBroker(cfg, oa)
		}
	}
	return intake.Deps{
		Doer: deps.Doer, Clock: intakeClockAdapter{runtime.NewSystemClock()},
		Vault: broker, Egress: engine, Registry: reg,
		NewOAuthBroker: newOAuth, Getenv: deps.Getenv,
	}, nil
}

// buildProviderEgressEngine mirrors internal/mcp.NewDefaultResponseMarshaler's
// wiring: the same detector and the process's default egress registry
// (which already carries the `provider-intake` class this ticket's
// classes.go entry registers) bound to this broker's egress vault.
func buildProviderEgressEngine(broker *secrets.Broker) (*egress.Engine, error) {
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		return nil, err
	}
	vault, err := secrets.NewEgressVault(broker)
	if err != nil {
		return nil, err
	}
	return egress.NewEngine(egress.DefaultRegistry(), vault, detector)
}

// intakeClockAdapter satisfies intake.Clock over runtime.Clock.
type intakeClockAdapter struct{ c runtime.Clock }

func (a intakeClockAdapter) Now() time.Time { return a.c.Now() }
