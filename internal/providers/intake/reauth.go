// Purpose: `provider reauth <name>` (P1-WID-01, legacy S-123.T1): re-run
//   the intake credential path against an EXISTING provider record. Skips
//   the shape probe and model enumeration entirely -- the existing record's
//   Driver/BaseURL/KnownModels are authoritative -- re-runs the live
//   micro-verify, and only on success writes the credential and upserts the
//   record IN PLACE through the SAME intake.Registry seam Add uses. Only
//   Auth, AuthRef, VerifySkipped and UpdatedAt change; every other field is
//   carried over from GetProvider unchanged. A CredentialUnset request is
//   the flagless form: InferReauthMode (reauth_mode.go) picks the mode.
//   Lane state is not a record field and Reauth writes none (R-14.324). The
//   composition root's Registry (cmd/cascade registryAdapter.UpsertProvider
//   -> upsertProviderLane) writes the lane from the upserted record:
//   available after a verified reauth, unknown under --no-verify. A failed
//   verify upserts nothing, so the lane keeps its prior state.
// Inputs: a Deps (identical seam Add uses) and a ReauthRequest.
// Outputs: an AddResult with Status "reauthorized" (the V3-31 signature),
//   or a typed refusal.
// Constraints: FAIL-CLOSED -- for --key/--key-env the vault Set happens
//   ONLY after a successful micro-verify, never before, so a failed verify
//   leaves the existing vault value and the existing registry record
//   untouched. This is why Reauth cannot reuse Add's own resolveCredential
//   unchanged: that function persists a --key/--key-env value immediately,
//   before any verify. --oauth is refused under CASCADE_NO_INPUT=1 before
//   the broker is built, and BEFORE the broker runs when the OAuth family's
//   driver differs from the record's, so a token minted for one vendor is
//   never sent to (or stored against) another's endpoint. A credential
//   value never appears in any returned error.
// SPORT: cli.provider.reauth/ADD (P1-WID-01).

package intake

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// reauthStatus is AddResult.Status on every successful Reauth.
const reauthStatus = "reauthorized"

// ReauthRequest is Reauth's input: the same three mutually-exclusive
// credential sources AddRequest offers (config.go), reduced to what
// re-authorizing an EXISTING record needs. No Kind pin, BaseURL override or
// Pool join -- the existing record's Driver/BaseURL/Pool are authoritative,
// and changing them is Add's job, not reauth's.
type ReauthRequest struct {
	// Name is the existing provider's name; GetProvider must find it.
	Name string
	// Credential selects the auth path, exactly as AddRequest.Credential.
	Credential CredentialMode
	// KeyValue is the literal key value for CredentialKey; empty refuses.
	KeyValue []byte
	// KeyEnvVar names the environment variable for CredentialKeyEnv.
	KeyEnvVar string
	// NoVerify skips the live micro-verify and records a warning instead,
	// matching AddRequest.NoVerify.
	NoVerify bool
}

// Validate fails closed exactly as AddRequest.Validate, reusing the same
// constructors so the messages never drift, and additionally refuses an
// empty --key value (parity with errEmptyKeyEnvValue for --key-env): with
// --no-verify an empty value would otherwise overwrite a working key.
func (r ReauthRequest) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return errEmptyProviderName()
	}
	switch r.Credential {
	case CredentialKey:
		if strings.TrimSpace(string(r.KeyValue)) == "" {
			return errEmptyKeyValue()
		}
		return nil
	case CredentialKeyEnv, CredentialOAuth:
		return nil
	case CredentialUnset:
		return errNoCredentialSource()
	default:
		return errNoCredentialSource()
	}
}

// errEmptyKeyValue reports an empty --key value, in errEmptyKeyEnvValue's
// message family.
func errEmptyKeyValue() error {
	return cascade.New(cascade.KindInvalidInput,
		"intake: the value --key read from stdin is empty")
}

// errOAuthDriverMismatch reports that --oauth for name would authorize a
// different vendor family than the record's driver. Names only kinds.
func errOAuthDriverMismatch(name string, oauthKind, recordKind DriverKind) error {
	return cascade.Newf(cascade.KindInvalidInput,
		"intake: --oauth for %q authorizes the %s family, but the provider's driver is %s; "+
			"use --key or --key-env, or re-add the provider with the matching driver",
		name, oauthKind, recordKind)
}

// Reauth re-runs the S-20.T1 auth path against name's EXISTING record. It
// never creates one -- see this file's header for the FAIL-CLOSED ordering.
// A CredentialUnset request is the flagless form (InferReauthMode).
func Reauth(ctx context.Context, deps Deps, req ReauthRequest) (AddResult, error) {
	if err := deps.validate(); err != nil {
		return AddResult{}, err
	}
	if req.Credential == CredentialUnset {
		mode, err := InferReauthMode(ctx, deps.Registry, deps.getenv(), req.Name)
		if err != nil {
			return AddResult{}, err
		}
		req.Credential = mode
	}
	if err := req.Validate(); err != nil {
		return AddResult{}, err
	}
	existing, err := getReauthRecord(ctx, deps.Registry, req.Name)
	if err != nil {
		return AddResult{}, err
	}
	cred, authType, ref, err := resolveReauthCredential(ctx, deps, req, existing.Driver)
	if err != nil {
		return AddResult{}, err
	}
	var warnings []string
	if req.NoVerify {
		warnings = append(warnings, "micro-verify skipped: --no-verify set")
	} else if verr := microVerify(ctx, deps, existing.Driver, existingVerifyBase(existing), cred, firstOrEmpty(existing.KnownModels)); verr != nil {
		// FAIL-CLOSED: nothing has been written for --key/--key-env.
		return AddResult{}, verr
	}
	if req.Credential != CredentialOAuth {
		if _, err := deps.Vault.Set(ctx, ref.String(), []byte(cred), secrets.SetUpdate); err != nil {
			return AddResult{}, err
		}
	}
	updated := existing
	updated.Auth = authType
	updated.AuthRef = ref
	updated.VerifySkipped = req.NoVerify
	updated.UpdatedAt = deps.Clock.Now()
	if err := deps.Registry.UpsertProvider(ctx, updated); err != nil {
		return AddResult{}, err
	}
	return AddResult{Status: reauthStatus, Warnings: warnings, Record: updated}, nil
}

// existingVerifyBase resolves the base URL a re-verify should call against:
// record.BaseURL, or defaultBases[record.Driver] when empty (the contract's
// own rule; ProviderRecord.BaseURL stores only an add-time override).
func existingVerifyBase(rec ProviderRecord) string {
	if rec.BaseURL != "" {
		return rec.BaseURL
	}
	return defaultBases[rec.Driver]
}

// resolveReauthCredential resolves req's credential source to a live value
// and the vault-key ref it belongs under, WITHOUT writing it for the key
// modes. --oauth first refuses CASCADE_NO_INPUT=1 and a driver-family mismatch against
// recordDriver, then delegates to resolveOAuth (core.go, S-20.T1)
// unchanged: the H/S-15.T2 broker persists the grant under its own
// AccessRef and enforces CASCADE_NO_INPUT=1 with no silent fallback.
func resolveReauthCredential(ctx context.Context, deps Deps, req ReauthRequest, recordDriver DriverKind) (value string, authType AuthType, ref VaultKeyRef, err error) {
	switch req.Credential {
	case CredentialKey:
		return string(req.KeyValue), AuthKey, keyRefFor(req.Name, "key"), nil
	case CredentialKeyEnv:
		v := deps.getenv()(req.KeyEnvVar)
		if strings.TrimSpace(v) == "" {
			return "", "", "", errEmptyKeyEnvValue(req.KeyEnvVar)
		}
		return v, AuthKey, keyRefFor(req.Name, "key"), nil
	case CredentialOAuth:
		// Refused here, not left to the broker: no silent fallback and no
		// dependence on a broker implementation honouring the variable.
		if noInput(deps.getenv()) {
			return "", "", "", errOAuthNoInput(ErrNoInputInteractive)
		}
		// builtinOAuthConfig is the same lookup resolveOAuth performs, so
		// the kind checked here is the kind the broker would authorize.
		_, oauthKind, cerr := builtinOAuthConfig(req.Name)
		if cerr != nil {
			return "", "", "", cerr
		}
		if oauthKind != recordDriver {
			return "", "", "", errOAuthDriverMismatch(req.Name, oauthKind, recordDriver)
		}
		val, _, at, r, oerr := resolveOAuth(ctx, deps, AddRequest{Name: req.Name, Credential: CredentialOAuth})
		return val, at, r, oerr
	case CredentialUnset:
		return "", "", "", errNoCredentialSource()
	default:
		return "", "", "", errNoCredentialSource()
	}
}
