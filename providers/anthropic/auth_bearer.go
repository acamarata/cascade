// Purpose: static bearer-key auth mode for the anthropic driver (P1-GWY-07):
//   sends "authorization: Bearer <token>" instead of x-api-key, never
//   refreshes on 401 (the token is static), and classifies 401 as
//   KindPermissionDenied so callers can distinguish bad credentials from
//   transport failures. Split out of auth.go to keep that file under the
//   300-line cap.
// Inputs: an AuthConfig with Mode == AuthModeBearerKey and a non-empty
//   KeyRef; the Resolver dereferences the credential reference at call time.
// Outputs: one outbound header name/value pair per request.
// Constraints: providers/** may import pkg/** only, never internal/**.
//   The token is never cached here; every request re-resolves through the
//   injected KeyResolver so a rotated credential is picked up without
//   restart. No refresh path exists: a bearer-key token is static by
//   definition, and a 401 means the credential itself is wrong.
// SPORT: providers.anthropic.auth-bearer/ADD (P1-GWY-07).

package anthropic

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// bearerKeyHeaderName is the HTTP header bearer-key auth writes.
const bearerKeyHeaderName = "authorization"

// bearerKeyValidate checks that a bearer-key AuthConfig has a non-empty
// KeyRef. Called from auth.go's validate when Mode == AuthModeBearerKey.
func bearerKeyValidate(cfg AuthConfig) error {
	if strings.TrimSpace(cfg.KeyRef) == "" {
		return cascade.New(cascade.KindInvalidInput,
			"anthropic: bearer-key auth requires a non-empty key_ref")
	}
	return nil
}

// bearerKeyHeader resolves the bearer-key token through cfg.Resolver and
// returns the outbound header. Never caches; never refreshes on 401.
func bearerKeyHeader(ctx context.Context, cfg AuthConfig) (name, value string, err error) {
	v, rerr := cfg.Resolver.Resolve(ctx, cfg.KeyRef)
	if rerr != nil {
		return "", "", mapErr(rerr, cascade.KindUnavailable,
			"anthropic: resolving bearer-key token")
	}
	return bearerKeyHeaderName, "Bearer " + v, nil
}

// postureFor maps an AuthMode to its compliance posture. Bearer-key is
// programmatic-only; key and OAuth keep their existing postures.
func postureFor(m AuthMode) provider.CompliancePosture {
	switch m {
	case AuthModeBearerKey:
		return provider.NewCompliancePosture(
			[]string{"bearer-key"},
			false, // interactive
			true,  // programmatic
			nil,   // automation modes
			"",    // pacing
			false, // multi-profile
		)
	case AuthModeKey, AuthModeOAuth:
		fallthrough
	default: // Preserve the existing key and OAuth posture.
		return provider.NewCompliancePosture(
			[]string{"api-key", "oauth"},
			true, // interactive
			true, // programmatic
			[]string{"batch", "agent", "scheduled"},
			"steady-with-burst",
			true,
		)
	}
}
