// Purpose: the OAuth broker's authorization half - Start, the PKCE/state
//
//	session it mints, the browser hand-off, and the callback completion
//	including the CSRF state check. Split out of oauth.go under Art.10.3's
//	300-line cap; the broker type and its constructor live there, and the
//	credential-lifetime half lives in oauth_refresh.go.
//
// Inputs: a bound loopback port, and the redirect query the far end sends.
// Outputs: a stored provider.TokenRecord, or a typed refusal.
// Constraints: the state check runs BEFORE the callback's contents are
//
//	used for anything at all, and consumes the session as it reads it, so
//	a replayed callback cannot be compared - there is nothing left to
//	compare it against. This file imports no network package.
//
// SPORT: OAUTH_BROKER: ADD (internal/secrets.OAuthBroker.Start).

package secrets

import (
	"context"
	"errors"
	"io"
	"net/url"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Start runs one full authorization. It refuses under CASCADE_NO_INPUT
// before binding a port or starting a goroutine, so the non-interactive
// caller gets a typed error rather than a five-minute hang.
//
// The listener wait is bounded by a context TIMEOUT (a duration), while the
// session's own expiry is measured against the injected clock. The two are
// deliberately different mechanisms: context deadlines are wall-clock by
// construction and cannot be frozen, whereas the state-expiry rule must be
// testable without waiting five minutes.
func (b *OAuthBroker) Start(ctx context.Context) (provider.TokenRecord, error) {
	if b.noInput {
		return provider.TokenRecord{}, errOAuthNoInput(b.cfg.ProviderID)
	}
	listener, err := b.listen(ctx)
	if err != nil {
		return provider.TokenRecord{}, err
	}
	defer func() { _ = listener.Close() }()

	session, err := b.beginSession(listener.Port())
	if err != nil {
		return provider.TokenRecord{}, err
	}
	defer b.dropSession(session.state)

	if err := b.sendToAuthorization(ctx, session); err != nil {
		return provider.TokenRecord{}, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, b.deadline)
	defer cancel()
	rawQuery, err := listener.Wait(waitCtx)
	if err != nil {
		return provider.TokenRecord{}, b.classifyWaitError(waitCtx, err)
	}
	return b.completeAuthorization(ctx, rawQuery)
}

// beginSession mints the PKCE pair and the CSRF state and registers them.
func (b *OAuthBroker) beginSession(port string) (*authSession, error) {
	redirectURI, err := b.cfg.RedirectURIForPort(port)
	if err != nil {
		return nil, err
	}
	codes, err := newPKCECodes(b.rand)
	if err != nil {
		return nil, err
	}
	state, err := newState(b.rand)
	if err != nil {
		return nil, err
	}
	session := &authSession{
		state: state, codes: codes, redirectURI: redirectURI,
		expiresAt: b.clock.Now().Add(b.deadline),
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessions[state] = session
	return session, nil
}

// sendToAuthorization opens the browser, or prints the URL under NoBrowser.
// A browser that fails to launch is NOT fatal: the URL is printed and the
// listener keeps waiting, because the operator can still paste it.
func (b *OAuthBroker) sendToAuthorization(ctx context.Context, session *authSession) error {
	rawURL, err := session.authURL(b.cfg.AuthEndpoint, b.cfg.ClientID, b.cfg.Scopes)
	if err != nil {
		return err
	}
	if !b.cfg.NoBrowser {
		if openErr := b.open(ctx, rawURL); openErr == nil {
			return nil
		}
	}
	_, err = io.WriteString(b.diag, "open this URL to authorize cascade:\n"+rawURL+"\n")
	return err
}

// classifyWaitError turns a listener failure into a typed refusal, so a
// five-minute deadline reads as a timeout rather than a generic I/O error.
func (b *OAuthBroker) classifyWaitError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errOAuthCallbackTimeout(err)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return cascade.Wrap(cascade.KindCanceled, err, "secrets: the OAuth authorization was canceled")
	}
	return err
}

// completeAuthorization parses the callback, enforces the state check, and
// exchanges the code. State is validated BEFORE the error= branch and
// before the code is used for anything: an unsolicited callback must be
// refused whatever it carries.
func (b *OAuthBroker) completeAuthorization(ctx context.Context, rawQuery string) (provider.TokenRecord, error) {
	callback, err := parseOAuthCallback(rawQuery)
	if err != nil {
		return provider.TokenRecord{}, err
	}
	session, err := b.consumeState(callback.state)
	if err != nil {
		return provider.TokenRecord{}, err
	}
	defer session.codes.verifier.Zero()
	defer func() { callback.code.Zero() }()
	if callback.errorCode != "" {
		return provider.TokenRecord{}, errOAuthAuthorizationDenied(callback.errorCode)
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", string(callback.code.Bytes()))
	form.Set("redirect_uri", session.redirectURI)
	form.Set("client_id", b.cfg.ClientID)
	form.Set("code_verifier", string(session.codes.verifier.Bytes()))
	tokens, err := b.postTokenEndpoint(ctx, form)
	if err != nil {
		if errors.Is(err, ErrInvalidGrant) {
			return provider.TokenRecord{}, errOAuthPKCEMismatch(err)
		}
		return provider.TokenRecord{}, err
	}
	return b.store.commit(ctx, defaultAccount, &tokens, 1, storedRecord{}, b.clock.Now())
}

// defaultAccount is the account label Start files a new grant under. A
// provider that supports several accounts names them at Refresh time; the
// authorization flow itself has no account identity until the token is in
// hand, and inventing one from the provider's userinfo endpoint is a Wave 3
// driver concern, not a broker concern.
const defaultAccount = "default"

// consumeState is the CSRF check. It is single-use by construction: the
// session is REMOVED from the table as it is read, so a replayed callback
// finds nothing and is refused rather than compared.
func (b *OAuthBroker) consumeState(state string) (*authSession, error) {
	b.mu.Lock()
	session, ok := b.sessions[state]
	delete(b.sessions, state)
	b.mu.Unlock()
	if !ok {
		return nil, errOAuthStateMismatch()
	}
	if !b.clock.Now().Before(session.expiresAt) {
		session.codes.verifier.Zero()
		return nil, errOAuthStateExpired()
	}
	return session, nil
}

// dropSession removes an abandoned session so a failed authorization does
// not leave a live verifier in the table.
func (b *OAuthBroker) dropSession(state string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if session, ok := b.sessions[state]; ok {
		session.codes.verifier.Zero()
		delete(b.sessions, state)
	}
}

// postTokenEndpoint performs one exchange and decodes the body. A non-2xx
// status with a decodable error body is classified from the body; anything
// else becomes a status-only refusal, because a non-JSON error page is
// provider prose that must not be echoed.
func (b *OAuthBroker) postTokenEndpoint(ctx context.Context, form url.Values) (tokenResponse, error) {
	body, status, err := b.exchange.Exchange(ctx, b.cfg.TokenEndpoint, form)
	if err != nil {
		return tokenResponse{}, cascade.Wrap(cascade.KindUnavailable, err,
			"secrets: the OAuth token endpoint could not be reached")
	}
	tokens, decodeErr := decodeTokenResponse(body)
	if status >= 200 && status <= 299 {
		return tokens, decodeErr
	}
	tokens.access.Zero()
	tokens.refresh.Zero()
	if decodeErr != nil && !errors.Is(decodeErr, ErrMalformedTokenResponse) {
		return tokenResponse{}, decodeErr
	}
	return tokenResponse{}, errOAuthExchangeFailed(status)
}
