// Purpose: the OAuth broker's credential-lifetime half - single-flight
//
//	refresh, the expiry-aware access-token accessor, and revocation.
//	Split out of oauth.go under Art.10.3's 300-line file cap; the type
//	and its authorization flow live there.
//
// Inputs: an account label and the vault-stored record for it.
// Outputs: refreshed provider.TokenRecord values, or a typed refusal.
// Constraints: single flight is not an optimisation here, it is
//
//	correctness: the trigger is a 401 storm, and a provider that rotates
//	its refresh token on every use would have ten concurrent refreshes
//	invalidate each other. A refresh that the provider answers with
//	invalid_grant PURGES the stored credential rather than keeping it, so
//	no later call can serve a token whose grant is already gone. This
//	file imports no network package.
//
// SPORT: OAUTH_BROKER: ADD (internal/secrets.OAuthBroker refresh/revoke).

package secrets

import (
	"context"
	"errors"
	"net/url"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Refresh exchanges the stored refresh token for a new access token, under
// single flight: concurrent callers for one account collapse into exactly
// one outbound request and all receive its result. That matters because the
// trigger is a 401 storm - every in-flight request to the provider fails at
// once - and ten simultaneous refreshes against a rotating-refresh-token
// provider would invalidate each other.
func (b *OAuthBroker) Refresh(ctx context.Context, account string) (provider.TokenRecord, error) {
	if err := validateSecretName(account); err != nil {
		return provider.TokenRecord{}, err
	}
	b.mu.Lock()
	if call, running := b.refreshing[account]; running {
		b.mu.Unlock()
		if b.joined != nil {
			b.joined <- struct{}{}
		}
		select {
		case <-call.done:
			return call.rec, call.err
		case <-ctx.Done():
			return provider.TokenRecord{}, cascade.Wrap(cascade.KindCanceled, ctx.Err(),
				"secrets: gave up waiting for the in-flight OAuth refresh")
		}
	}
	call := &refreshCall{done: make(chan struct{})}
	b.refreshing[account] = call
	b.mu.Unlock()

	call.rec, call.err = b.doRefresh(ctx, account)
	b.mu.Lock()
	delete(b.refreshing, account)
	b.mu.Unlock()
	close(call.done)
	return call.rec, call.err
}

// doRefresh is the single-flight leader's work.
func (b *OAuthBroker) doRefresh(ctx context.Context, account string) (provider.TokenRecord, error) {
	rec, err := b.store.load(ctx, account)
	if err != nil {
		return provider.TokenRecord{}, err
	}
	if rec.RefreshRef == "" {
		return provider.TokenRecord{}, errOAuthTokenExpired(b.cfg.ProviderID, account)
	}
	refresh, err := b.store.token(ctx, rec.RefreshRef)
	if err != nil {
		return provider.TokenRecord{}, err
	}
	defer refresh.Zero()
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", string(refresh.Bytes()))
	form.Set("client_id", b.cfg.ClientID)
	tokens, err := b.postTokenEndpoint(ctx, form)
	if err != nil {
		return provider.TokenRecord{}, b.handleRefreshFailure(ctx, account, rec, err)
	}
	return b.store.commit(ctx, account, &tokens, rec.Gen+1, rec, b.clock.Now())
}

// handleRefreshFailure purges the credential when the provider says the
// grant is gone. Keeping it would mean a later call serves a token the
// provider has already revoked - the "papered over by a cached token"
// failure - so the stored credential is removed and the caller told why.
func (b *OAuthBroker) handleRefreshFailure(ctx context.Context, account string, rec storedRecord, cause error) error {
	if !errors.Is(cause, ErrInvalidGrant) {
		return cause
	}
	if purgeErr := b.store.purge(ctx, account, rec); purgeErr != nil {
		return errOAuthCredentialInconsistent(b.cfg.ProviderID, account, purgeErr)
	}
	return errOAuthGrantRevoked(b.cfg.ProviderID, account)
}

// AccessToken returns a usable access token for account, refreshing first
// when the stored one has expired. This is the single-use window: the bytes
// exist in the caller's hands and nowhere else, and the caller is expected
// to send them and drop them.
//
// An expired record with no refresh token is refused outright rather than
// returned with a caveat - handing back a token that is known not to work
// only moves the failure somewhere with less context.
func (b *OAuthBroker) AccessToken(ctx context.Context, account string) ([]byte, error) {
	if err := validateSecretName(account); err != nil {
		return nil, err
	}
	rec, err := b.store.load(ctx, account)
	if err != nil {
		return nil, err
	}
	if rec.Expired(b.clock.Now(), b.skew) {
		if rec.RefreshRef == "" {
			return nil, errOAuthTokenExpired(b.cfg.ProviderID, account)
		}
		refreshed, refreshErr := b.Refresh(ctx, account)
		if refreshErr != nil {
			return nil, refreshErr
		}
		rec.TokenRecord = refreshed
	}
	token, err := b.store.token(ctx, rec.AccessRef)
	if err != nil {
		return nil, err
	}
	return token.Bytes(), nil
}

// Revoke forgets the credential. When a revocation endpoint is configured
// the refresh token is posted to it first; the local entries are removed
// either way, because a revocation cascade cannot confirm must still not
// leave a usable token on this machine.
func (b *OAuthBroker) Revoke(ctx context.Context, account string) error {
	if err := validateSecretName(account); err != nil {
		return err
	}
	rec, err := b.store.load(ctx, account)
	if err != nil {
		return err
	}
	var remoteErr error
	if b.cfg.RevocationEndpoint != "" && rec.RefreshRef != "" {
		remoteErr = b.postRevocation(ctx, rec.RefreshRef)
	}
	if purgeErr := b.store.purge(ctx, account, rec); purgeErr != nil {
		return purgeErr
	}
	return remoteErr
}

// postRevocation sends the refresh token to the RFC 7009 endpoint.
func (b *OAuthBroker) postRevocation(ctx context.Context, ref string) error {
	token, err := b.store.token(ctx, ref)
	if err != nil {
		return err
	}
	defer token.Zero()
	form := url.Values{}
	form.Set("token", string(token.Bytes()))
	form.Set("token_type_hint", "refresh_token")
	form.Set("client_id", b.cfg.ClientID)
	body, status, err := b.exchange.Exchange(ctx, b.cfg.RevocationEndpoint, form)
	for i := range body {
		body[i] = 0
	}
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "secrets: the OAuth revocation endpoint could not be reached")
	}
	if status < 200 || status > 299 {
		return cascade.Newf(cascade.KindUnavailable, "secrets: the OAuth revocation endpoint answered HTTP %d", status)
	}
	return nil
}
