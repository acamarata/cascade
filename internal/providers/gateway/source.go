// Purpose: resolves a gateway account's token at call time from either
// an environment variable or a digest-pinned credential helper. Tokens
// are never cached: every call re-reads the env or re-runs the helper
// so a rotated credential is picked up without restart. The caller
// zeroes the returned slice after use.
// Inputs: an Account, a Getenv func, HOME/USER overrides and a timeout.
// Outputs: Source.Token returning the raw token bytes or a typed error.
// Constraints: no vault, no logging of secret bytes, no caching.
// SPORT: internal/providers/gateway source (P1-GWY-02).

package gateway

import (
	"context"
	"os"
	"time"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

const defaultHelperTimeout = 10 * time.Second

// Source resolves tokens for gateway accounts. It carries no state and
// never caches: every Token call re-reads the environment or re-runs
// the pinned helper executable.
type Source struct {
	// Getenv reads an environment variable. Nil means os.Getenv.
	Getenv func(string) string
	// Home sets HOME for the helper process without inheriting it.
	Home string
	// User overrides USER/LOGNAME for the helper process.
	User string
	// Timeout bounds helper execution. Zero means 10s.
	Timeout time.Duration
}

func (s Source) getenv() func(string) string {
	if s.Getenv != nil {
		return s.Getenv
	}
	return os.Getenv
}

func (s Source) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return defaultHelperTimeout
}

// Token returns the credential bytes for account a. For env-sourced
// accounts an empty variable yields KindUnavailable naming the variable.
// For helper-sourced accounts the helper is run fresh every call; the
// returned slice must be zeroed by the caller after use.
func (s Source) Token(ctx context.Context, a Account) ([]byte, error) {
	switch a.Credential.Kind {
	case CredentialEnv:
		return s.tokenEnv(a)
	case CredentialHelper:
		return s.tokenHelper(ctx, a)
	default:
		return nil, cascade.Newf(cascade.KindInvalidInput, "gateway %s: unsupported credential kind", a.Name)
	}
}

func (s Source) tokenEnv(a Account) ([]byte, error) {
	getenv := s.getenv()
	if getenv == nil {
		return nil, cascade.Newf(cascade.KindUnavailable, "gateway %s: environment variable reader is not configured", a.Name)
	}
	v := getenv(a.Credential.EnvVar)
	if v == "" {
		return nil, cascade.Newf(cascade.KindUnavailable, "gateway %s: environment variable %s is empty", a.Name, a.Credential.EnvVar)
	}
	return []byte(v), nil
}

func (s Source) tokenHelper(ctx context.Context, a Account) ([]byte, error) {
	spec := secrets.HelperSpec{
		Path:    a.Credential.HelperPath,
		SHA256:  a.Credential.HelperSHA256,
		Home:    s.Home,
		User:    s.User,
		Timeout: s.timeout(),
	}
	return secrets.RunCredentialHelper(ctx, spec)
}
