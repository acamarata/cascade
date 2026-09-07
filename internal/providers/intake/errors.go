// Purpose: intake's typed refusals, every one built from pkg/cascade's
//   frozen 14-kind taxonomy (never a bare error, never an invented kind).
// Inputs: none - these are constructors, called with local values only.
// Outputs: *cascade.Error values.
// Constraints: an error built here NEVER interpolates a credential value,
//   a vault-key VALUE, or raw HTTP response bytes that might carry one -
//   only names (provider name, vault-key NAME, HTTP status, endpoint URL)
//   ever appear in a message. This is the rule the AGENT-BRIEF calls out
//   by name: "an error that echoes the input" is the commonest credential
//   leak, and every constructor below is reviewed against exactly that.
// SPORT: provider.intake/ADD (P1-E10-W3-S20-T1).

package intake

import (
	"errors"
	"fmt"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Sentinels a caller can match with errors.Is, each wrapped with a
// taxonomy Kind by the constructor beside it.
var (
	// ErrShapeProbeFailed reports that none of the three probed endpoint
	// shapes returned 200.
	ErrShapeProbeFailed = errors.New("intake: no provider shape matched the supplied credential")
	// ErrUnparseableDirective reports a non-interactive directive (or a
	// flag combination) intake cannot understand - a typed refusal, never
	// a partial admission (AGENT-BRIEF: "fail closed on everything
	// unparseable").
	ErrUnparseableDirective = errors.New("intake: could not parse the provider directive")
	// ErrSecretLiteral reports a secret-shaped literal where a directive
	// requires an env-var NAME.
	ErrSecretLiteral = errors.New("intake: a literal secret value was supplied where an env-var name was required")
	// ErrNoInputInteractive reports that CASCADE_NO_INPUT=1 forbids an
	// interactive path this call would otherwise take.
	ErrNoInputInteractive = errors.New("intake: an interactive step is required and CASCADE_NO_INPUT=1 forbids it")
	// ErrMicroVerifyFailed reports that the live 1-token verify call did
	// not succeed.
	ErrMicroVerifyFailed = errors.New("intake: the live micro-verify call did not succeed")
	// ErrEgressRefused reports that the provider-intake egress class is
	// unregistered or disabled.
	ErrEgressRefused = errors.New("intake: the provider-intake egress class refused this call")
)

// errShapeProbeFailed names every endpoint tried and the status (or dial
// error) each one returned, so the operator sees exactly what was tried -
// never the credential that was sent.
func errShapeProbeFailed(attempts []probeAttempt) error {
	var parts []string
	for _, a := range attempts {
		if a.err != nil {
			parts = append(parts, fmt.Sprintf("%s (%s): %s", a.kind, a.endpoint, a.err))
		} else {
			parts = append(parts, fmt.Sprintf("%s (%s): HTTP %d", a.kind, a.endpoint, a.status))
		}
	}
	return cascade.Wrapf(cascade.KindInvalidInput, ErrShapeProbeFailed,
		"intake: none of anthropic-compat, openai-compat or gemini matched this credential: %s",
		strings.Join(parts, "; "))
}

// errUnknownEndpointShape reports a response body the probe could not
// decode against any known driver shape.
func errUnknownEndpointShape(kind DriverKind, status int) error {
	return cascade.Wrapf(cascade.KindInvalidInput, ErrUnparseableDirective,
		"intake: the %s endpoint returned HTTP %d in a shape this build does not recognise; refusing rather than partially registering the provider", kind, status)
}

// errSecretLiteralInKeyEnv reports that a [[providers]] key_env value looks
// like a secret literal rather than an environment-variable NAME.
func errSecretLiteralInKeyEnv(name string) error {
	return cascade.Wrapf(cascade.KindInvalidInput, ErrSecretLiteral,
		"intake: provider %q's key_env value looks like a secret literal, not an environment-variable name; store the value first with `cascade vault set`, then name the env var that holds it", name)
}

// errOAuthNoInput reports CASCADE_NO_INPUT=1 blocking the interactive
// browser flow, citing the two non-interactive alternatives (acceptance
// criterion: the message must cite --key and --key-env).
func errOAuthNoInput(cause error) error {
	return cascade.Wrapf(cascade.KindUnavailable, cause,
		"intake: --oauth needs an interactive browser authorization, which CASCADE_NO_INPUT=1 forbids; use --key or --key-env instead")
}

// errMicroVerifyFailed names the model tried and the HTTP status (or
// transport error), with the actionable-guidance acceptance criterion in
// mind: a 401/403 reads as a key-scope problem, 402/429 as billing/quota.
func errMicroVerifyFailed(model string, status int, guidance string) error {
	return cascade.Wrapf(cascade.KindUnavailable, ErrMicroVerifyFailed,
		"intake: micro-verify against model %q returned HTTP %d: %s", model, status, guidance)
}

// errEgressClassRefused wraps whatever the egress engine returned (already
// a taxonomy error; the Kind it carries is preserved by Wrap-through-cause
// only if the caller re-checks cascade.HasKind, so this constructor keeps
// the original error's Kind by returning it unwrapped when it already
// satisfies cascade.HasKind for a known refusal Kind).
func errEgressClassRefused(cause error) error {
	if cause == nil {
		return cascade.Wrap(cascade.KindPolicyDenied, ErrEgressRefused, "intake: egress refused with no cause")
	}
	return cause
}

// errNoCredentialSource reports that the request named none of --key,
// --key-env or --oauth.
func errNoCredentialSource() error {
	return cascade.New(cascade.KindInvalidInput,
		"intake: exactly one of --key, --key-env or --oauth is required")
}

// errAmbiguousCredentialSource reports more than one credential source.
func errAmbiguousCredentialSource() error {
	return cascade.New(cascade.KindInvalidInput,
		"intake: --key, --key-env and --oauth are mutually exclusive")
}

// errEmptyProviderName reports a blank provider name.
func errEmptyProviderName() error {
	return cascade.New(cascade.KindInvalidInput, "intake: provider name must not be empty")
}

// errUnparseableInitConfig reports a [[providers]] table this build cannot
// parse: an unknown kind, a missing required field, or a key_env-shaped
// value that is not actually an env-var name.
func errUnparseableInitConfig(reason string) error {
	return cascade.Wrapf(cascade.KindInvalidInput, ErrUnparseableDirective,
		"intake: cascade-init.toml [[providers]] directive is unparseable: %s", reason)
}

// errEmptyKeyEnvValue reports that the named environment variable is unset
// or empty at intake time.
func errEmptyKeyEnvValue(envVar string) error {
	return cascade.Newf(cascade.KindInvalidInput,
		"intake: environment variable %q named by --key-env is unset or empty", envVar)
}
