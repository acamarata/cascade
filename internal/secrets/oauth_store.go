// Purpose: where an OAuth credential lives, and how it is replaced without
//
//	ever existing half-updated.
//
// Inputs: the vault Broker from S-15.T1 and a provider/account pair.
// Outputs: a stored provider.TokenRecord plus the generation counter that
//
//	makes replacement atomic.
//
// Constraints: there is NO fourth storage path here. Access and refresh
//
//	tokens are ordinary vault entries under the SAME Broker, and therefore
//	the same Custody backend (keychain / secret service / encrypted file
//	vault) and the same fail-closed rules as every other secret. Two
//	consequences follow and are deliberate. (1) Reading a token is an
//	ELEVATED verb, because Broker.Get is: minting a fresh bearer token off
//	a stored refresh token is exactly as disclosing as reading a secret,
//	so it carries the same authorisation, and a broker wired without an
//	ElevationGate refuses. (2) A release binary is built CGO_ENABLED=0, so
//	the custody backend that answers is a pure-Go one - on macOS the
//	/usr/bin/security keychain, on Linux the D-Bus secret service, and
//	elsewhere the age-encrypted file vault. A token in a release binary
//	goes to whichever of those three SelectCustody picked; it never sits
//	in a config file, an environment variable, or process memory beyond
//	the single-use window.
//
//	Replacement is committed by ONE write. New tokens are stored under a
//	new generation's key names first; the record entry - which names those
//	keys - is then overwritten in a single Set. A failure before that Set
//	leaves the previous record, and therefore the previous working
//	credential, completely intact. That is what "a refresh failure must
//	not leave a half-updated credential" means here, rather than a
//	best-effort rollback that can itself fail halfway.
//
// SPORT: OAUTH_BROKER: ADD (internal/secrets OAuth vault persistence).

package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// oauthKeyPrefix namespaces every vault entry this broker owns, so
// `vault list` shows what OAuth holds and a purge can be exact.
const oauthKeyPrefix = "oauth."

// storedRecord is a TokenRecord plus the generation counter. The record
// carries vault KEY NAMES only - re-read pkg/provider.TokenRecord: there is
// no field a token could occupy - so it is safe to serialize.
type storedRecord struct {
	provider.TokenRecord
	// Gen is the generation whose keys this record points at. Bumping it
	// is what makes a refresh atomic.
	Gen int `json:"gen"`
}

// oauthStore is the broker's view of the vault.
type oauthStore struct {
	vault      *Broker
	providerID string
}

// recordKey is the single entry whose overwrite commits a refresh.
func (s oauthStore) recordKey(account string) string {
	return oauthKeyPrefix + s.providerID + "." + account + ".record"
}

// tokenKey names one generation's access or refresh entry.
func (s oauthStore) tokenKey(account, role string, gen int) string {
	return oauthKeyPrefix + s.providerID + "." + account + "." + role + "." + strconv.Itoa(gen)
}

// load reads the stored record. Elevated, because Broker.Get is.
func (s oauthStore) load(ctx context.Context, account string) (storedRecord, error) {
	raw, err := s.vault.Get(ctx, s.recordKey(account))
	if err != nil {
		return storedRecord{}, err
	}
	var rec storedRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return storedRecord{}, cascade.Wrapf(cascade.KindIntegrity, err,
			"secrets: the stored %s OAuth record for account %q could not be decoded", s.providerID, account)
	}
	return rec, nil
}

// token reads one of the record's referenced token entries. Elevated.
//
// The bytes are COPIED before they are wrapped. The Custody contract does
// not promise that Get returns a slice the caller owns, and an oauthSecret
// is zeroed by whoever holds it - so wrapping the backend's own buffer
// would have this package erase another component's memory. Found by
// TestOAuthCommitKeepsThePreviousRefreshRefWhenNoneIsReturned, where
// zeroing a still-referenced refresh token destroyed it in the store.
func (s oauthStore) token(ctx context.Context, ref string) (oauthSecret, error) {
	if ref == "" {
		return oauthSecret{}, ErrSecretNotFound(ref)
	}
	raw, err := s.vault.Get(ctx, ref)
	if err != nil {
		return oauthSecret{}, err
	}
	return newOAuthSecret(append([]byte(nil), raw...)), nil
}

// commit writes tr as generation gen and then flips the record to point at
// it. The flip is the commit point: everything before it is invisible to a
// reader, everything after it is the new credential in full.
//
// tr's secrets are zeroed by this function on every path, success or
// failure - the caller hands ownership over.
func (s oauthStore) commit(ctx context.Context, account string, tr *tokenResponse, gen int, prev storedRecord, now time.Time) (provider.TokenRecord, error) {
	defer func() {
		tr.access.Zero()
		tr.refresh.Zero()
	}()
	accessRef := s.tokenKey(account, "access", gen)
	if _, err := s.vault.Set(ctx, accessRef, tr.access.Bytes(), SetUpdate); err != nil {
		return provider.TokenRecord{}, errOAuthCredentialInconsistent(s.providerID, account, err)
	}
	refreshRef := prev.RefreshRef
	if !tr.refresh.Empty() {
		refreshRef = s.tokenKey(account, "refresh", gen)
		if _, err := s.vault.Set(ctx, refreshRef, tr.refresh.Bytes(), SetUpdate); err != nil {
			return provider.TokenRecord{}, errOAuthCredentialInconsistent(s.providerID, account, err)
		}
	}
	rec := storedRecord{TokenRecord: provider.TokenRecord{
		Provider: s.providerID, Account: account, Scopes: prev.Scopes,
		AccessRef: accessRef, RefreshRef: refreshRef,
	}, Gen: gen}
	if tr.scope != "" {
		rec.Scopes = splitScopes(tr.scope)
	}
	if tr.expiresIn > 0 {
		rec.ExpiresAt = now.Add(time.Duration(tr.expiresIn) * time.Second)
	}
	return s.flip(ctx, account, rec, prev)
}

// flip performs the single committing Set and then best-effort-removes the
// superseded generation's entries. A cleanup failure is NOT an error: the
// new credential is already committed and working, and reporting a stale
// leftover as a credential failure would turn a tidy-up problem into an
// outage. The leftovers are removed on the next successful refresh.
func (s oauthStore) flip(ctx context.Context, account string, rec, prev storedRecord) (provider.TokenRecord, error) {
	raw, err := json.Marshal(rec)
	if err != nil {
		return provider.TokenRecord{}, errOAuthCredentialInconsistent(s.providerID, account, err)
	}
	if _, err := s.vault.Set(ctx, s.recordKey(account), raw, SetUpdate); err != nil {
		return provider.TokenRecord{}, errOAuthCredentialInconsistent(s.providerID, account, err)
	}
	for _, stale := range []string{prev.AccessRef, prev.RefreshRef} {
		if stale == "" || stale == rec.AccessRef || stale == rec.RefreshRef {
			continue
		}
		_ = s.vault.Delete(ctx, stale)
	}
	return rec.TokenRecord, nil
}

// purge removes the record and both token entries. The RECORD goes first:
// once it is gone nothing can find the token entries, so an interruption
// after that point leaves unreachable bytes rather than a live credential
// that cascade believes it revoked.
func (s oauthStore) purge(ctx context.Context, account string, rec storedRecord) error {
	var errs []error
	if err := s.vault.Delete(ctx, s.recordKey(account)); err != nil && !cascade.HasKind(err, cascade.KindNotFound) {
		errs = append(errs, err)
	}
	for _, ref := range []string{rec.AccessRef, rec.RefreshRef} {
		if ref == "" {
			continue
		}
		if err := s.vault.Delete(ctx, ref); err != nil && !cascade.HasKind(err, cascade.KindNotFound) {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return cascade.Wrapf(cascade.KindUnavailable, errors.Join(errs...),
			"secrets: the %s credential for account %q was not fully removed", s.providerID, account)
	}
	return nil
}

// splitScopes splits the space-delimited scope string RFC 6749 §3.3 defines.
func splitScopes(scope string) []string { return strings.Fields(scope) }
