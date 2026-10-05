//go:build darwin

// Purpose: the macOS keychain availability probe - write one item, remove
// it, prove it gone - and its fail-closed verdicts.
// Inputs: a keychainCustody (service label, command runner, resolved
// keychain) and the caller's context.
// Outputs: nil (the keychain holds a secret), a plain unavailable (the
// write was refused, so SelectCustody may fall back to the file vault),
// ErrProbeTimeout or ErrProbeCleanupFailed (both refuse custody outright).
// Constraints: every probe call draws its own nonce account, so concurrent
// probes never touch each other's item. The deadline is read from the
// probe's own context, never inferred from a runner error: exec reports a
// killed process as "signal: killed" without wrapping ctx.Err(). Nothing
// here logs, and no error carries an account, nonce, service or value.
//
// SPORT: internal/secrets Custody availability probe (P1-SEC-28, P1-SEC-42).

package secrets

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// probeTimeout bounds one probe, retry included.
const probeTimeout = 2 * time.Second

// availabilityProbeAccount is the account prefix every probe item carries;
// each probe call appends its own nonce (newProbeAccount). A probe killed
// or timed out mid-flight can leave one such item behind. Those items are
// not in the List index and cascade does not remove them: finding them
// needs dump-keychain, which prompts once per item.
const availabilityProbeAccount = keychainAccountPrefix + availabilityProbeName

// newProbeAccount returns a fresh account for one probe call. It is drawn
// per call, never stored on keychainCustody, so two probes on one instance
// (a CLI command racing daemon start) never share an item.
func newProbeAccount() string {
	return availabilityProbeAccount + "." + rand.Text()
}

// probe writes the probe item and removes it, all under one bounded ctx
// and one nonce account. Precedence: a deadline reached on that ctx is
// ErrProbeTimeout (a slow keychain is not a locked one, so no fallback); a
// failed write is a plain unavailable (nothing was written; a locked or
// headless keychain keeps its file-vault fallback, R-14.270); a write whose
// item cannot be proven gone is ErrProbeCleanupFailed. A cancelled parent
// is not a deadline and stays a plain unavailable.
func (k *keychainCustody) probe(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, probeTimeout)
	defer cancel()
	keychain, err := k.resolveKeychain(ctx)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrProbeTimeout
	}
	if err != nil {
		return err
	}
	account := newProbeAccount()
	_, setErr := k.run(ctx, securityBin, "add-generic-password",
		"-a", account, "-s", k.service, "-U", "-X", hex.EncodeToString([]byte{0}), keychain)
	// Cleanup runs even after a failed write, so a probe never accumulates.
	cleanErr := k.removeProbe(ctx, keychain, account)
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return ErrProbeTimeout
	case setErr != nil:
		return ErrCustodyUnavailable(darwinCustodyName, redactRunner(setErr))
	case cleanErr != nil:
		return ErrProbeCleanupFailed
	}
	return nil
}

// removeProbe deletes the probe item, retrying exactly once with the same
// identity, then confirms it is gone. Only a genuine not-found is absence.
func (k *keychainCustody) removeProbe(ctx context.Context, keychain, account string) error {
	del := []string{"delete-generic-password", "-a", account, "-s", k.service, keychain}
	_, err := k.run(ctx, securityBin, del...)
	if err != nil && !probeAbsent(err) {
		_, err = k.run(ctx, securityBin, del...)
	}
	if err != nil && !probeAbsent(err) {
		return err
	}
	// Checked even after a clean delete; find without -w reads no value.
	_, err = k.run(ctx, securityBin, "find-generic-password",
		"-a", account, "-s", k.service, keychain)
	switch {
	case err == nil:
		return errors.New("the probe item is still stored")
	case probeAbsent(err):
		return nil
	}
	return err
}

// probeAbsent is isKeychainNotFound minus any stderr that also reports a
// denial: a refusal quoting the not-found phrase is not proof of absence.
func probeAbsent(err error) bool {
	var re *runnerError
	if !isKeychainNotFound(err) || !errors.As(err, &re) {
		return false
	}
	text := strings.ToLower(re.stderr)
	for _, denial := range []string{"not allowed", "not permitted", "denied", "permission", "locked", "passphrase"} {
		if strings.Contains(text, denial) {
			return false
		}
	}
	return true
}
