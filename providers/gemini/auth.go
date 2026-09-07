// Purpose: the credential seam for the gemini driver (P1-E10-W3-S19-T4):
//   key auth and official OAuth auth (§D-9), both resolved through an
//   injected broker - never a literal, an inline env read, or a settable
//   raw-key field. AuthConfig carries only vault-key names and a
//   KeyResolver/OAuthBroker pair. This is also the seam key-POOL lanes ride
//   on: the registry resolves ONE pool-member lane to a single vault key
//   ref before construction; this file never sees more than that one ref
//   and holds no pool index. On-401 refresh collapses concurrent callers
//   into one broker call (R-16.10/06 §5 rule 23). Embed also lives here
//   (file-size balance under Art.10.3's 300-line cap; it needs none of the
//   auth types above beyond going through doJSON like every other verb).
// SPORT: placeholder: providers/gemini driver (ADD) - see gemini.go.

package gemini

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// tokenExpirySkew is subtracted from an OAuth token's declared expiry so a
// token that would expire mid-request is refreshed ahead of time.
const tokenExpirySkew = 30 * time.Second

// AuthMode names which of the gemini driver's two OFFICIAL auth modes
// (§D-9) an AuthConfig configures. There is deliberately no third mode: the
// consumer-sub cloudcode path is a private config-side extension outside
// core (§D-9/Q-4) and this driver carries no knowledge of it.
type AuthMode uint8

const (
	// AuthModeKey is static API-key auth ("x-goog-api-key"). This is the
	// mode key-pool lanes (GF pattern) run under: each pool member is one
	// KeyRef, resolved by the caller before this Driver is constructed.
	AuthModeKey AuthMode = iota
	// AuthModeOAuth is broker-driven official OAuth
	// ("authorization: Bearer ...").
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

// AuthConfig configures one of the driver's two OFFICIAL auth modes. It
// never holds a raw credential: KeyRef and the OAuth token record's *Ref
// fields are vault-key names, dereferenced only through Resolver at request
// time.
type AuthConfig struct {
	// Mode selects AuthModeKey or AuthModeOAuth.
	Mode AuthMode
	// KeyRef is the vault key name holding the API key (AuthModeKey). For
	// a key-pool lane this is the single vault ref the registry resolved
	// for this lane - this driver never sees the whole pool.
	KeyRef string
	// OAuth is the provider's official OAuth configuration (AuthModeOAuth).
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
		return cascade.Newf(cascade.KindInvalidInput, "gemini: auth mode %d is not one of key/oauth", a.Mode)
	}
	if a.Resolver == nil {
		return cascade.New(cascade.KindInvalidInput, "gemini: auth resolver must not be nil")
	}
	switch a.Mode {
	case AuthModeKey:
		if strings.TrimSpace(a.KeyRef) == "" {
			return cascade.New(cascade.KindInvalidInput, "gemini: key auth requires a non-empty key_ref")
		}
	case AuthModeOAuth:
		if a.Broker == nil {
			return cascade.New(cascade.KindInvalidInput, "gemini: oauth auth requires a broker")
		}
		if strings.TrimSpace(a.Account) == "" {
			return cascade.New(cascade.KindInvalidInput, "gemini: oauth auth requires a non-empty account")
		}
		if err := a.OAuth.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// authState is the driver's mutable OAuth cache: the last TokenRecord it
// received (vault-key refs only, never token bytes) and the in-flight
// refresh, if any, that concurrent callers collapse onto. This is NOT
// pool state: it caches one account's access token, never a set of keys or
// a rotation index - those stay the registry's (S-20.T2/T3).
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
			return "", "", mapErr(rerr, cascade.KindUnavailable, "gemini: resolving api key")
		}
		return "x-goog-api-key", v, nil
	case AuthModeOAuth:
		rec, terr := d.currentToken(ctx)
		if terr != nil {
			return "", "", terr
		}
		tok, rerr := d.cfg.Auth.Resolver.Resolve(ctx, rec.AccessRef)
		if rerr != nil {
			return "", "", mapErr(rerr, cascade.KindUnavailable, "gemini: resolving oauth access token")
		}
		return "authorization", "Bearer " + tok, nil
	default:
		return "", "", cascade.Newf(cascade.KindInternal, "gemini: unknown auth mode %d", d.cfg.Auth.Mode)
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
		return provider.TokenRecord{}, mapErr(err, cascade.KindPermissionDenied, "gemini: oauth "+verb)
	}
	return rec, nil
}

// joinRefresh waits for an in-flight refresh other callers started.
func joinRefresh(ctx context.Context, wait *authRefresh) (provider.TokenRecord, error) {
	select {
	case <-wait.done:
		return wait.rec, wait.err
	case <-ctx.Done():
		return provider.TokenRecord{}, mapErr(ctx.Err(), cascade.KindCanceled, "gemini: waiting for oauth refresh")
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

// wireEmbedContentRequest is one element of a batchEmbedContents request.
type wireEmbedContentRequest struct {
	Model   string      `json:"model"`
	Content wireContent `json:"content"`
}

// wireBatchEmbedRequest is the batchEmbedContents request body.
type wireBatchEmbedRequest struct {
	Requests []wireEmbedContentRequest `json:"requests"`
}

// wireEmbedding is one vector in a batchEmbedContents response.
type wireEmbedding struct {
	Values []float32 `json:"values"`
}

// wireBatchEmbedResponse is the batchEmbedContents response body.
type wireBatchEmbedResponse struct {
	Embeddings []wireEmbedding `json:"embeddings"`
}

// Embed implements provider.ModelProvider.Embed against the real
// batchEmbedContents endpoint. Gemini genuinely offers embeddings, so this
// never fabricates a vector (Art.1): every Vectors entry is the API's own.
func (d *Driver) Embed(ctx context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	if len(req.Inputs) == 0 {
		return provider.ModelEmbedResponse{}, cascade.New(cascade.KindInvalidInput, "gemini: embed request has no inputs")
	}
	model := d.cfg.embedModel(req.Model)
	wireReq := wireBatchEmbedRequest{Requests: make([]wireEmbedContentRequest, len(req.Inputs))}
	for i, text := range req.Inputs {
		wireReq.Requests[i] = wireEmbedContentRequest{
			Model:   "models/" + model,
			Content: wireContent{Parts: []wirePart{{Text: text}}},
		}
	}
	var resp wireBatchEmbedResponse
	path := "/" + apiVersion + "/models/" + model + ":batchEmbedContents"
	if err := d.doJSON(ctx, path, wireReq, &resp); err != nil {
		return provider.ModelEmbedResponse{}, err
	}
	if len(resp.Embeddings) != len(req.Inputs) {
		return provider.ModelEmbedResponse{}, cascade.Newf(cascade.KindIntegrity,
			"gemini: batchEmbedContents returned %d vectors for %d inputs", len(resp.Embeddings), len(req.Inputs))
	}
	vectors := make([][]float32, len(resp.Embeddings))
	for i, e := range resp.Embeddings {
		vectors[i] = e.Values
	}
	return provider.ModelEmbedResponse{Vectors: vectors}, nil
}
