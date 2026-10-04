// Purpose: the flagless form of `provider reauth <name>` (P1-WID-01). The
//   status widget launches `<cli> provider reauth <ref>` with no credential
//   flag, so the credential mode is inferred from the stored record.
// Inputs: a Registry, the process getenv, and a provider name.
// Outputs: the CredentialMode Reauth runs, or a typed refusal.
// Constraints: fail closed. Only an Auth=oauth record infers a mode (the
//   OAuth flow, whose browser step is the interactive part). An Auth=key
//   record is refused with a remedy naming --key-env and --key, because
//   reading a key from the terminal would be a new credential-entry
//   surface; this path reads nothing. An empty or unknown Auth is refused.
//   CASCADE_NO_INPUT=1 makes the flagless form a hard error before the
//   record is read. Nothing here selects custody or touches the vault, so a
//   caller can refuse before either exists.
// SPORT: cli.provider.reauth/ADD (P1-WID-01).

package intake

import (
	"context"
	"errors"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

var (
	// ErrUnknownProvider is the cause of every reauth refusal for a name
	// with no stored record. Compare by identity; the message names the fix.
	ErrUnknownProvider = errors.New("no provider record under this name")
	// ErrFlaglessRefused is the cause of every flagless-form refusal that
	// depends on the stored record's auth type.
	ErrFlaglessRefused = errors.New("the flagless reauth form cannot run for this record")
)

// InferReauthMode returns the credential mode the flagless form runs for
// name's stored record. It is the only inference path: the CLI calls it
// before building custody, and Reauth calls it for a CredentialUnset request.
func InferReauthMode(ctx context.Context, reg Registry, getenv func(string) string, name string) (CredentialMode, error) {
	if strings.TrimSpace(name) == "" {
		return CredentialUnset, errEmptyProviderName()
	}
	if noInput(getenv) {
		return CredentialUnset, errFlaglessNoInput(name)
	}
	if reg == nil {
		return CredentialUnset, cascade.New(cascade.KindInvalidInput, "intake: reauth needs a provider registry")
	}
	rec, err := getReauthRecord(ctx, reg, name)
	if err != nil {
		return CredentialUnset, err
	}
	switch rec.Auth {
	case AuthOAuth:
		return CredentialOAuth, nil
	case AuthKey:
		return CredentialUnset, errFlaglessKeyRecord(name)
	default:
		return CredentialUnset, errFlaglessUnknownAuth(name)
	}
}

// getReauthRecord reads name's record, turning a not-found into the
// add-first refusal every reauth path returns.
func getReauthRecord(ctx context.Context, reg Registry, name string) (ProviderRecord, error) {
	rec, err := reg.GetProvider(ctx, name)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return ProviderRecord{}, errReauthUnknownProvider(name)
		}
		return ProviderRecord{}, err
	}
	return rec, nil
}

// errReauthUnknownProvider reports that name has no stored record and names
// the fix.
func errReauthUnknownProvider(name string) error {
	return cascade.Wrapf(cascade.KindNotFound, ErrUnknownProvider,
		"intake: unknown provider %q: run `cascade provider add` first", name)
}

// errFlaglessNoInput reports CASCADE_NO_INPUT=1 refusing the flagless form.
func errFlaglessNoInput(name string) error {
	return cascade.Wrapf(cascade.KindUnavailable, ErrNoInputInteractive,
		"intake: `provider reauth %s` without a credential flag is interactive, which CASCADE_NO_INPUT=1 forbids; "+
			"use --key-env VAR, or --key with the value on stdin", name)
}

// errFlaglessKeyRecord reports a key record under the flagless form, with
// the remedy. It never offers to read the key.
func errFlaglessKeyRecord(name string) error {
	return cascade.Wrapf(cascade.KindInvalidInput, ErrFlaglessRefused,
		"intake: provider %q stores an API key, and the flagless form never reads a key from the terminal; "+
			"run `cascade provider reauth %s --key-env VAR`, or --key with the value on stdin", name, name)
}

// errFlaglessUnknownAuth reports a record whose auth type is empty or not
// one this build knows. The stored value is not echoed.
func errFlaglessUnknownAuth(name string) error {
	return cascade.Wrapf(cascade.KindInvalidInput, ErrFlaglessRefused,
		"intake: provider %q has no recognised auth type, so the flagless form cannot infer one; "+
			"pass --key-env VAR, --key or --oauth", name)
}
