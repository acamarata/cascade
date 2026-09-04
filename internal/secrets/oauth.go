// Purpose: the loopback PKCE OAuth broker - internal/secrets' implementation
//
//	of pkg/provider.OAuthBroker. It runs an authorization, exchanges the
//	code, stores the result through the vault broker, refreshes under
//	single flight, and revokes.
//
// Inputs: a validated provider.ProviderOAuthConfig, the vault Broker from
//
//	S-15.T1, an injected Clock and entropy source, a diagnostics writer,
//	and three seams (token exchange, loopback listener, browser opener)
//	whose production implementations live in oauth_transport.go. Nothing
//	in THIS file imports "net" or "net/http", so every protocol rule
//	below is asserted in the default unit lane.
//
// Outputs: provider.TokenRecord values, which name vault keys and cannot
//
//	hold a token, plus typed refusals from oauth_errors.go.
//
// Constraints: a bearer token is radioactive. It exists in this process
//
//	only between the token endpoint's response and the vault Set that
//	stores it, and only inside an oauthSecret, which no formatting verb
//	and no JSON encoder can render. The diagnostics writer is handed
//	nothing but the authorization URL and fixed prose. CASCADE_NO_INPUT
//	is checked before a port is bound or a goroutine is started, so a
//	non-interactive run fails fast instead of hanging on a browser that
//	will never open.
//
// SPORT: OAUTH_BROKER: ADD (internal/secrets.OAuthBroker, Start, Refresh,
//
//	Revoke).

package secrets

import (
	"context"
	"io"
	"net/url"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// noInputEnv is the environment variable that forbids interactive prompts.
const noInputEnv = "CASCADE_NO_INPUT"

// Broker timing defaults.
const (
	// defaultAuthDeadline is the 5-minute window an authorization has to
	// complete. It bounds how long a verifier and a listener stay alive.
	defaultAuthDeadline = 5 * time.Minute
	// defaultExpirySkew treats a token as expired slightly early, so one
	// that would expire while a request is in flight is refreshed first.
	defaultExpirySkew = 30 * time.Second
)

// tokenExchanger performs one form POST against a provider endpoint and
// returns the raw body and HTTP status. It is the ONLY network seam in the
// flow: keeping it an interface is what lets every protocol rule in this
// file be tested with no socket, the way internal/client keeps its decode
// path socket-free.
type tokenExchanger interface {
	Exchange(ctx context.Context, endpoint string, form url.Values) (body []byte, status int, err error)
}

// callbackListener is a bound loopback listener awaiting one redirect.
type callbackListener interface {
	// Port is the OS-assigned ephemeral port, as a decimal string.
	Port() string
	// Wait blocks for the redirect's raw query string, or until ctx ends.
	Wait(ctx context.Context) (string, error)
	// Close releases the port. Safe to call more than once.
	Close() error
}

// listenerFactory binds a fresh loopback listener.
type listenerFactory func(ctx context.Context) (callbackListener, error)

// browserOpener sends the operator's browser to rawURL.
type browserOpener func(ctx context.Context, rawURL string) error

// OAuthDeps carries the broker's injected dependencies. Every field has a
// safe production default except Vault, which has none: a broker with no
// vault could only "store" a token by discarding it.
type OAuthDeps struct {
	// Vault is the secrets broker every token is stored through.
	Vault *Broker
	// Clock is the time source. Required (Art.7.3: no bare time.Now).
	Clock Clock
	// Rand is the entropy source for verifiers and state. Nil means
	// crypto/rand.
	Rand io.Reader
	// Diagnostics receives the authorization URL under NoBrowser and
	// nothing else. Nil means io.Discard.
	Diagnostics io.Writer
	// Deadline bounds one authorization. Zero means defaultAuthDeadline.
	Deadline time.Duration
	// ExpirySkew is how early a token counts as expired. Zero means
	// defaultExpirySkew.
	ExpirySkew time.Duration
	// LookupEnv reads the environment. Nil means os.LookupEnv.
	LookupEnv func(string) (string, bool)
}

// refreshCall is one in-flight refresh other callers wait on.
type refreshCall struct {
	done chan struct{}
	rec  provider.TokenRecord
	err  error
}

// OAuthBroker implements provider.OAuthBroker for one provider config.
type OAuthBroker struct {
	cfg      provider.ProviderOAuthConfig
	store    oauthStore
	clock    Clock
	rand     io.Reader
	diag     io.Writer
	deadline time.Duration
	skew     time.Duration
	noInput  bool

	exchange tokenExchanger
	listen   listenerFactory
	open     browserOpener

	mu         sync.Mutex
	sessions   map[string]*authSession
	refreshing map[string]*refreshCall
	// joined, when non-nil, receives one value each time a caller joins an
	// already-running refresh instead of starting its own. Production
	// leaves it nil and pays one nil check; the single-flight test sets it
	// so it can prove the collapse deterministically - "ten goroutines all
	// reached the gate" is otherwise unobservable from outside, and a test
	// that cannot observe it is really testing the scheduler.
	joined chan<- struct{}
}

// newOAuthBroker is the seam-injecting core constructor. oauth_transport.go's
// NewOAuthBroker wires the production seams into it.
func newOAuthBroker(cfg provider.ProviderOAuthConfig, deps OAuthDeps, exchange tokenExchanger, listen listenerFactory, open browserOpener) (*OAuthBroker, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	switch {
	case deps.Vault == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: the OAuth broker needs a vault broker to store tokens in")
	case deps.Clock == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: the OAuth broker needs an injected clock")
	case exchange == nil || listen == nil || open == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: the OAuth broker needs all three of its transport seams")
	}
	b := &OAuthBroker{
		cfg: cfg, store: oauthStore{vault: deps.Vault, providerID: cfg.ProviderID},
		clock: deps.Clock, rand: deps.Rand, diag: deps.Diagnostics,
		deadline: deps.Deadline, skew: deps.ExpirySkew,
		exchange: exchange, listen: listen, open: open,
		sessions: map[string]*authSession{}, refreshing: map[string]*refreshCall{},
	}
	b.applyDefaults(deps.LookupEnv)
	return b, nil
}

// applyDefaults fills the optional dependencies and reads CASCADE_NO_INPUT
// once, at construction, so Start's refusal costs nothing at call time.
func (b *OAuthBroker) applyDefaults(lookupEnv func(string) (string, bool)) {
	if b.rand == nil {
		b.rand = defaultEntropy()
	}
	if b.diag == nil {
		b.diag = io.Discard
	}
	if b.deadline <= 0 {
		b.deadline = defaultAuthDeadline
	}
	if b.skew < 0 {
		b.skew = 0
	} else if b.skew == 0 {
		b.skew = defaultExpirySkew
	}
	if lookupEnv == nil {
		lookupEnv = defaultLookupEnv
	}
	if v, ok := lookupEnv(noInputEnv); ok && v == "1" {
		b.noInput = true
	}
}
