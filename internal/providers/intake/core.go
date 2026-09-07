// Purpose: Add's orchestration (P1-E10-W3-S20-T1): resolve a credential
//   through the vault/OAuth broker seam, shape-probe and enumerate models,
//   optionally micro-verify, and upsert the resulting ProviderRecord
//   idempotently, joining a key-pool when asked.
// Inputs: a Deps (every side-effecting dependency injected) and a
//   validated AddRequest.
// Outputs: an AddResult, or a typed refusal.
// Constraints: the resolved credential VALUE lives only in local variables
//   for as long as a probe/verify call needs it; never placed on
//   AddResult, ProviderRecord, or any returned error.
// SPORT: provider.intake/ADD (P1-E10-W3-S20-T1).

package intake

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// AddResult is Add's success shape, the --json output body.
type AddResult struct {
	// Status is "converged" (first add) or "updated" (idempotent re-add).
	Status string `json:"status"`
	// Warnings carries non-fatal notices, e.g. a --no-verify skip.
	Warnings []string `json:"warnings,omitempty"`
	// Record is the resulting registry row.
	Record ProviderRecord `json:"record"`
}

// Deps carries every side-effecting dependency Add needs. Every field is
// required except Getenv (defaults to os.Getenv).
type Deps struct {
	Doer     Doer
	Clock    Clock
	Vault    *secrets.Broker
	Egress   *egress.Engine
	Registry Registry
	// NewOAuthBroker constructs the per-call OAuth broker. Production
	// wires secrets.NewOAuthBroker; a test substitutes a fake that never
	// opens a real loopback listener.
	NewOAuthBroker func(cfg provider.ProviderOAuthConfig, deps secrets.OAuthDeps) (provider.OAuthBroker, error)
	Getenv         func(string) string
}

func (d Deps) getenv() func(string) string {
	if d.Getenv != nil {
		return d.Getenv
	}
	return os.Getenv
}

func (d Deps) validate() error {
	switch {
	case d.Doer == nil, d.Clock == nil, d.Vault == nil, d.Egress == nil, d.Registry == nil, d.NewOAuthBroker == nil:
		return cascade.New(cascade.KindInvalidInput, "intake: Deps is missing a required dependency")
	}
	return nil
}

// Add is the command's single entry point: resolve credential -> probe ->
// enumerate -> verify -> upsert -> pool join.
func Add(ctx context.Context, deps Deps, req AddRequest) (AddResult, error) {
	if err := deps.validate(); err != nil {
		return AddResult{}, err
	}
	if err := req.Validate(); err != nil {
		return AddResult{}, err
	}
	cred, driverHint, authType, authRef, err := resolveCredential(ctx, deps, req)
	if err != nil {
		return AddResult{}, err
	}
	kind, base, models, err := probeAndEnumerate(ctx, deps, cred, req.BaseURL, driverHint)
	if err != nil {
		return AddResult{}, err
	}
	var warnings []string
	if req.NoVerify {
		warnings = append(warnings, "micro-verify skipped: --no-verify set")
	} else if verr := microVerify(ctx, deps, kind, base, cred, firstOrEmpty(models)); verr != nil {
		return AddResult{}, verr
	}
	now := deps.Clock.Now()
	existing, getErr := deps.Registry.GetProvider(ctx, req.Name)
	status := "converged"
	created := now
	if getErr == nil {
		status = "updated"
		created = existing.CreatedAt
	}
	record := ProviderRecord{
		Name: req.Name, Driver: kind, BaseURL: req.BaseURL, Auth: authType, AuthRef: authRef,
		KnownModels: models, Capabilities: unknownCapabilities(), CapabilitiesProbedAt: now,
		Pool: req.Pool, VerifySkipped: req.NoVerify, CreatedAt: created, UpdatedAt: now,
	}
	if req.Pool != "" {
		record.PoolIndex = poolJoinIndex(ctx, deps.Registry, req.Pool)
	}
	if err := deps.Registry.UpsertProvider(ctx, record); err != nil {
		return AddResult{}, err
	}
	return AddResult{Status: status, Warnings: warnings, Record: record}, nil
}

// unknownCapabilities returns the tri-state default: every dimension
// CapabilityUnknown. R-14.88 forbids a compile-time constant asserting
// support for a hosted provider; this ticket's capability probe runs no
// additional live call beyond micro-verify, so every dimension it has not
// actually observed stays honestly unresolved rather than assumed true.
func unknownCapabilities() provider.Capabilities { return provider.Capabilities{} }

// firstOrEmpty returns models[0], or "" for an empty list.
func firstOrEmpty(models []string) string {
	if len(models) == 0 {
		return ""
	}
	return models[0]
}

// poolJoinIndex advances pool's round-robin index for a MemoryRegistry, or
// returns 0 for any other Registry implementation (a future S-20.T2
// registry owns its own pool-index rebalancing on join).
func poolJoinIndex(_ context.Context, reg Registry, pool string) int {
	if mr, ok := reg.(*MemoryRegistry); ok {
		return mr.nextPoolIndex(pool)
	}
	return 0
}

// resolveCredential resolves req's credential source to a live value, a
// vault-key ref it is stored under, and (for --oauth) the driver kind the
// built-in OAuth configuration names. --key and --key-env return "" for
// driverHint: the shape probe determines the kind from the credential.
func resolveCredential(ctx context.Context, deps Deps, req AddRequest) (value string, driverHint DriverKind, authType AuthType, ref VaultKeyRef, err error) {
	switch req.Credential {
	case CredentialKey:
		ref = keyRefFor(req.Name, "key")
		if _, err = deps.Vault.Set(ctx, ref.String(), req.KeyValue, secrets.SetUpdate); err != nil {
			return "", "", "", "", err
		}
		return string(req.KeyValue), "", AuthKey, ref, nil
	case CredentialKeyEnv:
		return resolveKeyEnv(ctx, deps, req)
	case CredentialOAuth:
		return resolveOAuth(ctx, deps, req)
	case CredentialUnset:
		return "", "", "", "", errNoCredentialSource()
	default:
		return "", "", "", "", errNoCredentialSource()
	}
}

// resolveKeyEnv reads req.KeyEnvVar once and delegates storage to the
// vault broker, per H/S-15.T1.
func resolveKeyEnv(ctx context.Context, deps Deps, req AddRequest) (string, DriverKind, AuthType, VaultKeyRef, error) {
	v := deps.getenv()(req.KeyEnvVar)
	if strings.TrimSpace(v) == "" {
		return "", "", "", "", errEmptyKeyEnvValue(req.KeyEnvVar)
	}
	ref := keyRefFor(req.Name, "key")
	if _, err := deps.Vault.Set(ctx, ref.String(), []byte(v), secrets.SetUpdate); err != nil {
		return "", "", "", "", err
	}
	return v, "", AuthKey, ref, nil
}

// resolveOAuth runs the PKCE loopback flow through the H/S-15.T2 broker.
// CASCADE_NO_INPUT=1 is enforced by that broker's Start BEFORE any
// listener opens; this function adds nothing but the CLI-facing guidance
// the acceptance criterion requires (--key/--key-env alternatives).
func resolveOAuth(ctx context.Context, deps Deps, req AddRequest) (string, DriverKind, AuthType, VaultKeyRef, error) {
	cfg, kind, err := builtinOAuthConfig(req.Name)
	if err != nil {
		return "", "", "", "", err
	}
	broker, err := deps.NewOAuthBroker(cfg, secrets.OAuthDeps{Vault: deps.Vault, Clock: oauthClockAdapter{deps.Clock}, LookupEnv: lookupEnvAdapter(deps.getenv())})
	if err != nil {
		return "", "", "", "", err
	}
	rec, err := broker.Start(ctx)
	if err != nil {
		return "", "", "", "", errOAuthNoInput(err)
	}
	val, err := deps.Vault.Get(ctx, rec.AccessRef)
	if err != nil {
		return "", "", "", "", err
	}
	return string(val), kind, AuthOAuth, VaultKeyRef(rec.AccessRef), nil
}

// lookupEnvAdapter turns getenv into the LookupEnv shape secrets.OAuthDeps
// expects.
func lookupEnvAdapter(getenv func(string) string) func(string) (string, bool) {
	return func(name string) (string, bool) { v := getenv(name); return v, v != "" }
}

// oauthClockAdapter satisfies internal/secrets.Clock over this package's
// own Clock, so callers never need internal/runtime just to build Deps.
type oauthClockAdapter struct{ c Clock }

func (a oauthClockAdapter) Now() time.Time { return a.c.Now() }

// probeAndEnumerate runs the shape probe (or, when driverHint is set by
// the OAuth path, probes only that one candidate) and returns the
// enumerated model list.
func probeAndEnumerate(ctx context.Context, deps Deps, cred, baseOverride string, driverHint DriverKind) (DriverKind, string, []string, error) {
	kind, base, body, err := shapeProbe(ctx, deps.Doer, deps.Egress, cred, baseOverride)
	if err != nil {
		return "", "", nil, err
	}
	if driverHint != "" && driverHint != kind {
		kind = driverHint
	}
	models, err := modelsFromProbe(kind, body)
	if err != nil {
		return "", "", nil, err
	}
	return kind, base, models, nil
}

// microVerify fires the 1-token completion request and asserts a
// non-error 200. model=="" (no models enumerated) is itself a verify
// failure: there is nothing to verify against.
func microVerify(ctx context.Context, deps Deps, kind DriverKind, base, cred, model string) error {
	if model == "" {
		return errMicroVerifyFailed("", 0, "no model was enumerated to verify against")
	}
	if _, err := acquireIntakeGate(deps.Egress, egress.TierInternal); err != nil {
		return err
	}
	req := microVerifyRequest(kind, base, cred, model)
	resp, err := deps.Doer.Do(ctx, req)
	if err != nil {
		return errMicroVerifyFailed(model, 0, err.Error())
	}
	if resp.Status != 200 {
		return errMicroVerifyFailed(model, resp.Status, microVerifyGuidance(resp.Status))
	}
	return nil
}

// microVerifyGuidance maps a non-200 micro-verify status to actionable
// operator guidance.
func microVerifyGuidance(status int) string {
	switch status {
	case 401, 403:
		return "the credential's scope looks insufficient for this endpoint"
	case 402, 429:
		return "billing may be inactive or the account's quota is exhausted"
	default:
		return "the provider rejected the verify request"
	}
}

// builtinOAuthConfig names the two provider families P1 supports over
// OAuth (06-FORGE-SPEC §5 rule 23: anthropic + generic PKCE + gemini
// official). Any other name under --oauth is a typed, unparseable-input
// refusal rather than a guess at an unregistered client.
func builtinOAuthConfig(name string) (provider.ProviderOAuthConfig, DriverKind, error) {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "gemini"):
		return provider.ProviderOAuthConfig{
			ProviderID: "gemini", ClientID: "cascade-gemini-oauth-client",
			Scopes:        []string{"https://www.googleapis.com/auth/generative-language.retriever"},
			RedirectURI:   "http://127.0.0.1/callback",
			AuthEndpoint:  "https://accounts.google.com/o/oauth2/v2/auth",
			TokenEndpoint: "https://oauth2.googleapis.com/token",
			PKCEMethod:    provider.PKCEMethodS256,
		}, DriverGemini, nil
	case strings.Contains(lower, "anthropic"):
		return provider.ProviderOAuthConfig{
			ProviderID: "anthropic", ClientID: "cascade-anthropic-oauth-client",
			Scopes:        []string{"org:model_execute"},
			RedirectURI:   "http://127.0.0.1/callback",
			AuthEndpoint:  "https://console.anthropic.com/oauth/authorize",
			TokenEndpoint: "https://console.anthropic.com/v1/oauth/token",
			PKCEMethod:    provider.PKCEMethodS256,
		}, DriverAnthropic, nil
	default:
		return provider.ProviderOAuthConfig{}, "", cascade.Newf(cascade.KindUnsupported,
			"intake: --oauth is supported only for a provider name containing \"anthropic\" or \"gemini\" in this build; use --key or --key-env for %q", name)
	}
}
