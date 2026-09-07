// Purpose: the credential seam for the anthropic driver (P1-E10-W3-S19-T2):
//   key auth and OAuth auth, both resolved through an injected broker - never
//   a literal, an inline env read, or a settable raw-key field. AuthConfig
//   carries only vault-key names and a KeyResolver/OAuthBroker pair; the
//   driver dereferences a name to a secret value only at the instant a
//   request needs it and never persists the byte value itself.
// Inputs: an AuthConfig built by the driver's caller (the eventual S-20.T1
//   intake, or a test fake here) and, per call, a context for cancellation.
// Outputs: the one outbound header name/value pair a request needs.
// Constraints: providers/** may import pkg/** only, never internal/** - the
//   real vault broker (internal/secrets) is out of this file's reach by
//   construction, so KeyResolver and provider.OAuthBroker are the only two
//   seams through which a credential can arrive. On-401 refresh collapses
//   concurrent callers into one broker call (R-16.10/06 §5 rule 23).
// SPORT: placeholder: providers/anthropic driver (ADD) - see anthropic.go.

package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// tokenExpirySkew is subtracted from an OAuth token's declared expiry so a
// token that would expire mid-request is refreshed ahead of time.
const tokenExpirySkew = 30 * time.Second

// AuthMode names which of the anthropic driver's two auth modes an
// AuthConfig configures.
type AuthMode uint8

const (
	// AuthModeKey is static API-key auth ("x-api-key").
	AuthModeKey AuthMode = iota
	// AuthModeOAuth is broker-driven OAuth ("authorization: Bearer ...").
	AuthModeOAuth
)

// Valid reports whether m is one of the two declared AuthMode members.
func (m AuthMode) Valid() bool { return m <= AuthModeOAuth }

// String returns m's stable lowercase name.
func (m AuthMode) String() string {
	if m == AuthModeOAuth {
		return "oauth"
	}
	return "key"
}

// KeyResolver dereferences a vault-key NAME to its current secret value. A
// production implementation lives in internal/secrets (out of this
// package's reach); tests substitute a fake that never touches a real
// keychain.
type KeyResolver interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

// AuthConfig configures one of the driver's two auth modes. It never holds
// a raw credential: KeyRef and the OAuth token record's *Ref fields are
// vault-key names, dereferenced only through Resolver at request time.
type AuthConfig struct {
	// Mode selects AuthModeKey or AuthModeOAuth.
	Mode AuthMode
	// KeyRef is the vault key name holding the API key (AuthModeKey).
	KeyRef string
	// OAuth is the provider's OAuth configuration (AuthModeOAuth).
	OAuth provider.ProviderOAuthConfig
	// Broker runs the loopback PKCE flow and refreshes tokens
	// (AuthModeOAuth). internal/secrets implements it in production.
	Broker provider.OAuthBroker
	// Account is the local account label the OAuth grant is filed under
	// (AuthModeOAuth).
	Account string
	// Resolver dereferences a vault-key name to its secret value. Required
	// for both modes.
	Resolver KeyResolver
}

// validate fails closed on any AuthConfig this driver cannot safely use.
func (a AuthConfig) validate() error {
	if !a.Mode.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "anthropic: auth mode %d is not one of key/oauth", a.Mode)
	}
	if a.Resolver == nil {
		return cascade.New(cascade.KindInvalidInput, "anthropic: auth resolver must not be nil")
	}
	switch a.Mode {
	case AuthModeKey:
		if strings.TrimSpace(a.KeyRef) == "" {
			return cascade.New(cascade.KindInvalidInput, "anthropic: key auth requires a non-empty key_ref")
		}
	case AuthModeOAuth:
		if a.Broker == nil {
			return cascade.New(cascade.KindInvalidInput, "anthropic: oauth auth requires a broker")
		}
		if strings.TrimSpace(a.Account) == "" {
			return cascade.New(cascade.KindInvalidInput, "anthropic: oauth auth requires a non-empty account")
		}
		if err := a.OAuth.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// authState is the driver's mutable OAuth cache: the last TokenRecord it
// received (vault-key refs only, never token bytes) and the in-flight
// refresh, if any, that concurrent callers collapse onto.
type authState struct {
	mu       sync.Mutex
	token    provider.TokenRecord
	have     bool
	inflight *authRefresh
}

// authRefresh is one in-flight Start/Refresh call other goroutines wait on.
type authRefresh struct {
	done chan struct{}
	rec  provider.TokenRecord
	err  error
}

// headerFor resolves the single outbound auth header this request needs.
func (d *Driver) headerFor(ctx context.Context) (name, value string, err error) {
	switch d.cfg.Auth.Mode {
	case AuthModeKey:
		v, rerr := d.cfg.Auth.Resolver.Resolve(ctx, d.cfg.Auth.KeyRef)
		if rerr != nil {
			return "", "", mapErr(rerr, cascade.KindUnavailable, "anthropic: resolving api key")
		}
		return "x-api-key", v, nil
	case AuthModeOAuth:
		rec, terr := d.currentToken(ctx)
		if terr != nil {
			return "", "", terr
		}
		tok, rerr := d.cfg.Auth.Resolver.Resolve(ctx, rec.AccessRef)
		if rerr != nil {
			return "", "", mapErr(rerr, cascade.KindUnavailable, "anthropic: resolving oauth access token")
		}
		return "authorization", "Bearer " + tok, nil
	default:
		return "", "", cascade.Newf(cascade.KindInternal, "anthropic: unknown auth mode %d", d.cfg.Auth.Mode)
	}
}

// currentToken returns a non-expired TokenRecord, starting or refreshing
// through the broker as needed.
func (d *Driver) currentToken(ctx context.Context) (provider.TokenRecord, error) {
	d.auth.mu.Lock()
	have, rec := d.auth.have, d.auth.token
	expired := have && rec.Expired(d.cfg.Clock.Now(), tokenExpirySkew)
	d.auth.mu.Unlock()
	if have && !expired {
		return rec, nil
	}
	return d.refreshToken(ctx, have)
}

// forceRefresh drops any cached freshness assumption and re-derives a token
// through the broker; the caller on-401 retry path uses this.
func (d *Driver) forceRefresh(ctx context.Context) (provider.TokenRecord, error) {
	d.auth.mu.Lock()
	have := d.auth.have
	d.auth.mu.Unlock()
	return d.refreshToken(ctx, have)
}

// refreshToken runs (or joins) exactly one broker Start/Refresh call, per
// R-16.10's single-flight requirement, and updates the cache on success.
func (d *Driver) refreshToken(ctx context.Context, haveExisting bool) (provider.TokenRecord, error) {
	d.auth.mu.Lock()
	if wait := d.auth.inflight; wait != nil {
		d.auth.mu.Unlock()
		return joinRefresh(ctx, wait)
	}
	refresh := &authRefresh{done: make(chan struct{})}
	d.auth.inflight = refresh
	d.auth.mu.Unlock()

	rec, err := d.callBroker(ctx, haveExisting)
	d.auth.mu.Lock()
	if err == nil {
		d.auth.token, d.auth.have = rec, true
	}
	d.auth.inflight = nil
	d.auth.mu.Unlock()

	refresh.rec, refresh.err = rec, err
	close(refresh.done)
	return rec, err
}

// callBroker performs the single outbound Start or Refresh call.
func (d *Driver) callBroker(ctx context.Context, haveExisting bool) (provider.TokenRecord, error) {
	var (
		rec provider.TokenRecord
		err error
	)
	if haveExisting {
		rec, err = d.cfg.Auth.Broker.Refresh(ctx, d.cfg.Auth.Account)
	} else {
		rec, err = d.cfg.Auth.Broker.Start(ctx)
	}
	if err != nil {
		verb := "start"
		if haveExisting {
			verb = "refresh"
		}
		return provider.TokenRecord{}, mapErr(err, cascade.KindPermissionDenied, "anthropic: oauth "+verb)
	}
	return rec, nil
}

// joinRefresh waits for an in-flight refresh other callers started.
func joinRefresh(ctx context.Context, wait *authRefresh) (provider.TokenRecord, error) {
	select {
	case <-wait.done:
		return wait.rec, wait.err
	case <-ctx.Done():
		return provider.TokenRecord{}, mapErr(ctx.Err(), cascade.KindCanceled, "anthropic: waiting for oauth refresh")
	}
}

// mapErr preserves an already-typed cascade error's kind; anything else is
// wrapped under fallback. Used where a lower seam (KeyResolver, OAuthBroker)
// may already return a properly classified error.
func mapErr(err error, fallback cascade.Kind, msg string) error {
	var ce *cascade.Error
	if errors.As(err, &ce) {
		return err
	}
	return cascade.Wrap(fallback, err, msg)
}

// wireErrorBody is the Messages API's error envelope, e.g.
// {"type":"error","error":{"type":"rate_limit_error","message":"..."}}.
type wireErrorBody struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// mapTransportError classifies a network-layer failure: context
// cancellation/deadline first (they are the caller's own signal, not the
// server's), everything else falls back to KindUnavailable.
func mapTransportError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return cascade.Wrap(cascade.KindCanceled, err, "anthropic: request canceled")
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return cascade.Wrap(cascade.KindTimeout, err, "anthropic: request deadline exceeded")
	}
	return cascade.Wrap(cascade.KindUnavailable, err, "anthropic: sending request")
}

// errorMessage extracts the vendor's error message from body, falling back
// to a generic message when the body is not the documented error envelope.
func errorMessage(status int, body []byte) string {
	var wireErr wireErrorBody
	if err := json.Unmarshal(body, &wireErr); err == nil && wireErr.Error.Message != "" {
		return fmt.Sprintf("anthropic: http %d %s: %s", status, wireErr.Error.Type, wireErr.Error.Message)
	}
	return fmt.Sprintf("anthropic: http %d", status)
}
