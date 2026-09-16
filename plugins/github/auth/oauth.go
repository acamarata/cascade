// Package auth is cascade-github's OAuth broker: the PKCE loopback flow
// that obtains a GitHub token, and the single-flight refresh that renews it
// after a 401.
//
// Purpose: obtain and renew a GitHub token without the plugin ever holding
//
//	a client secret or writing a token into config.
//
// Inputs: a provider.ProviderOAuthConfig, the process environment, and the
//
//	callback query the authorization server redirects to.
//
// Outputs: an authorization URL, a verified callback code, and a token
//
//	handed to the host for storage.
//
// Constraints: imports pkg/** and stdlib ONLY, never internal/**
//
//	(Art.10.2). The token is stored through a host_secret_ref host-fn call
//	and NEVER as a manifest or config literal. Every decision this file
//	makes — PKCE derivation, URL construction, callback verification, the
//	no-input refusal, refresh single-flighting — is a pure function of its
//	inputs so the whole flow is testable without binding a socket, which
//	the tree-wide no-network unit rule requires of this package's tests.
//
// SPORT: plugins/github auth (ADD) — P1-E25-W5-S51-T1.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// GitHub's authorization server. Both are fixed by GitHub, not chosen here.
const (
	// AuthEndpoint is GitHub's authorization endpoint.
	AuthEndpoint = "https://github.com/login/oauth/authorize"
	// TokenEndpoint is GitHub's token endpoint.
	TokenEndpoint = "https://github.com/login/oauth/access_token"
	// RequiredScope is the classic scope covering repository, issue and
	// pull-request read/write. The fine-grained-token equivalents are
	// documented in the plugin README; classic `repo` is the superset the
	// manifest declares.
	RequiredScope = "repo"
)

// CallbackTimeout bounds how long the broker waits for the authorization
// server to redirect back. Five minutes is the contract's figure: long
// enough for a human to finish a consent screen, short enough that an
// abandoned flow does not hold a listening socket open indefinitely.
const CallbackTimeout = 5 * time.Minute

// NoInputEnv is the variable that declares this process cannot prompt.
const NoInputEnv = "CASCADE_NO_INPUT"

// Config returns the GitHub ProviderOAuthConfig for clientID.
//
// RedirectURI carries no port on purpose: the broker binds an OS-assigned
// ephemeral port and substitutes it. A hard-coded port either collides with
// whatever already holds it or lets another process squat the callback and
// receive the authorization code.
func Config(clientID string) (provider.ProviderOAuthConfig, error) {
	if strings.TrimSpace(clientID) == "" {
		return provider.ProviderOAuthConfig{}, cascade.New(cascade.KindInvalidInput,
			"github: an OAuth client id is required")
	}
	return provider.ProviderOAuthConfig{
		ProviderID:    "cascade-github",
		ClientID:      clientID,
		Scopes:        []string{RequiredScope},
		RedirectURI:   "http://" + provider.LoopbackHost + "/callback",
		AuthEndpoint:  AuthEndpoint,
		TokenEndpoint: TokenEndpoint,
		PKCEMethod:    provider.PKCEMethodS256,
	}, nil
}

// PKCE is one flow's proof-key pair plus the state that binds the callback
// to this flow.
type PKCE struct {
	// Verifier is the secret held in memory for the token exchange.
	Verifier string
	// Challenge is the S256 hash sent to the authorization server.
	Challenge string
	// State is the opaque value the callback must echo back.
	State string
}

// NewPKCE derives a fresh verifier, its S256 challenge and a state value.
//
// All three come from crypto/rand. The verifier is 64 raw bytes, well above
// RFC 7636's 32-byte floor, and every value is base64url without padding
// because the authorization server compares them as strings.
func NewPKCE() (PKCE, error) {
	verifier, err := randomURLSafe(64)
	if err != nil {
		return PKCE{}, err
	}
	state, err := randomURLSafe(32)
	if err != nil {
		return PKCE{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{
		Verifier:  verifier,
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
		State:     state,
	}, nil
}

// randomURLSafe returns n random bytes base64url-encoded without padding,
// drawn from crypto/rand.
func randomURLSafe(n int) (string, error) {
	// nolint:forbidigo // crypto/rand.Reader, not math/rand — shared
	// selector text (internal/rpc/sse.go: same limitation). PKCE
	// randomness must be cryptographic and must NOT be seedable: an
	// injectable SEEDED source here would be a security defect, not the
	// reproducibility Art.7.3 asks for.
	return randomURLSafeFrom(rand.Reader, n)
}

// randomURLSafeFrom is randomURLSafe over an explicit source.
//
// The source is a parameter so the FAILURE path is reachable in a test — a
// randomness source that cannot answer must produce a typed error rather
// than a short or predictable value. It is not a seam for choosing WHICH
// randomness the flow uses: every production caller passes crypto/rand's
// reader, and a seedable source here would be a security defect.
func randomURLSafeFrom(src io.Reader, n int) (string, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(src, buf); err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "github: generating OAuth randomness")
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// AuthorizeURL builds the URL the user's browser is sent to. redirectURI
// must already carry the bound ephemeral port.
func AuthorizeURL(cfg provider.ProviderOAuthConfig, p PKCE, redirectURI string) (string, error) {
	if redirectURI == "" {
		return "", cascade.New(cascade.KindInvalidInput,
			"github: the redirect URI must carry the bound loopback port")
	}
	if p.Challenge == "" || p.State == "" {
		return "", cascade.New(cascade.KindInvalidInput,
			"github: the PKCE challenge and state must both be set")
	}
	q := url.Values{}
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", strings.Join(cfg.Scopes, " "))
	q.Set("state", p.State)
	q.Set("code_challenge", p.Challenge)
	q.Set("code_challenge_method", "S256")
	return cfg.AuthEndpoint + "?" + q.Encode(), nil
}

// VerifyCallback validates the authorization server's redirect and returns
// the authorization code.
//
// The state comparison is what makes the callback trustworthy: without it
// this listener would accept an authorization code from anyone who could
// reach the loopback port, which is every process on the machine. An
// `error` parameter is honoured first, because a server that declined has
// no code to give and its reason is the useful thing to report.
func VerifyCallback(query url.Values, p PKCE) (code string, err error) {
	if declined := query.Get("error"); declined != "" {
		detail := query.Get("error_description")
		if detail == "" {
			detail = declined
		}
		return "", cascade.Newf(cascade.KindPermissionDenied,
			"github: authorization was declined (%s)", detail)
	}
	got := query.Get("state")
	if got == "" {
		return "", cascade.New(cascade.KindPermissionDenied,
			"github: the OAuth callback carried no state; refusing a code this flow cannot prove it asked for")
	}
	if got != p.State {
		return "", cascade.New(cascade.KindPermissionDenied,
			"github: the OAuth callback's state does not match this flow; refusing the code")
	}
	code = query.Get("code")
	if code == "" {
		return "", cascade.New(cascade.KindInvalidInput,
			"github: the OAuth callback carried no authorization code")
	}
	return code, nil
}

// RequireInteractive refuses the flow when the process cannot prompt.
//
// An OAuth authorization needs a human at a browser. Under CASCADE_NO_INPUT
// there is nobody to consent, so this is a hard error naming the variable
// and what to do instead — never a silent skip that leaves the caller
// wondering why no token appeared.
func RequireInteractive(getenv func(string) string) error {
	if getenv == nil {
		return cascade.New(cascade.KindInternal, "github: RequireInteractive needs an environment reader")
	}
	if getenv(NoInputEnv) == "1" {
		return cascade.Newf(cascade.KindElevationRequired,
			"github: OAuth authorization needs a browser and a person to approve it, but %s=1 declares this "+
				"process cannot prompt; run `cascade plugin add cascade-github` from an interactive terminal",
			NoInputEnv)
	}
	return nil
}

// TokenResponse is GitHub's token-endpoint response shape.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	Error        string `json:"error,omitempty"`
	ErrorDesc    string `json:"error_description,omitempty"`
}

// Validate checks a token response before anything is stored.
//
// GitHub answers a failed exchange with HTTP 200 and an `error` member, so
// a caller checking only the status code would store an empty token and
// discover it one 401 at a time.
func (t TokenResponse) Validate() error {
	if t.Error != "" {
		// Both halves are reported. The code is the machine-readable
		// identifier a support thread turns on ("bad_verification_code"
		// says the code was reused or expired); the description is the
		// human sentence. Dropping the code for the prose loses the half
		// that says what actually happened.
		detail := t.Error
		if t.ErrorDesc != "" {
			detail = t.Error + ": " + t.ErrorDesc
		}
		return cascade.Newf(cascade.KindPermissionDenied, "github: token exchange failed (%s)", detail)
	}
	if strings.TrimSpace(t.AccessToken) == "" {
		return cascade.New(cascade.KindIntegrity,
			"github: the token endpoint returned no access token and no error")
	}
	return nil
}

// Refresher single-flights token renewal.
//
// Several in-flight API calls can meet a 401 at the same instant. Without
// this, each would start its own refresh, and every exchange after the
// first would present an already-redeemed refresh token — which GitHub
// rejects, turning one recoverable 401 into a broken session. The zero
// value is ready to use.
type Refresher struct {
	// onJoin, when set, is called each time a caller JOINS an in-flight
	// refresh rather than starting one.
	//
	// It exists so the single-flight invariant can be asserted
	// deterministically. Without it a test can only launch goroutines and
	// hope they overlap — and a test that hopes is a test that passes for
	// the wrong reason, which this one did until the race was found. Nil
	// in production: nothing outside a test ever sets it.
	onJoin func()

	mu      sync.Mutex
	running *refreshCall
}

// refreshCall is one in-flight refresh every waiter shares.
type refreshCall struct {
	done  chan struct{}
	token string
	err   error
}

// Refresh runs exchange at most once concurrently, returning the same
// result to every caller that arrived while it was running.
//
// A caller that arrives DURING a refresh waits for it rather than starting
// another; a caller that arrives after one finished starts a new one, since
// its own 401 is evidence the previous result is already stale.
func (r *Refresher) Refresh(exchange func() (string, error)) (string, error) {
	r.mu.Lock()
	if call := r.running; call != nil {
		r.mu.Unlock()
		if r.onJoin != nil {
			r.onJoin()
		}
		<-call.done
		return call.token, call.err
	}
	call := &refreshCall{done: make(chan struct{})}
	r.running = call
	r.mu.Unlock()

	call.token, call.err = exchange()
	close(call.done)

	r.mu.Lock()
	r.running = nil
	r.mu.Unlock()
	return call.token, call.err
}
