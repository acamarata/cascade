// Purpose: the two platform-probe refusals SelectCustody never turns into a
// file-vault fallback.
// Inputs: none; these are fixed sentinels compared by identity.
// Outputs: ErrProbeCleanupFailed and ErrProbeTimeout.
// Constraints: compared by identity, never errors.Is (a cascade error's Is
// compares Kind only, and a plain unavailable must still reach the file
// vault). Neither message carries an account, service, nonce or value.
//
// SPORT: internal/secrets Custody probe refusals (P1-SEC-28, P1-SEC-42).

package secrets

import "github.com/acamarata/cascade/pkg/cascade"

// ErrProbeCleanupFailed is the platform probe's refusal when it wrote its
// probe item and could not prove the item removed. KindUnavailable, and a
// refusal rather than a reason to fall back: the keychain accepted a write,
// so moving new secrets to the weaker file vault would lower custody on a
// host whose keychain works. It carries no account, service or value.
var ErrProbeCleanupFailed = cascade.New(cascade.KindUnavailable,
	"platform keychain probe could not clean up; custody refused")

// ErrProbeTimeout is the platform probe's refusal when it reached its
// deadline. KindUnavailable, and a refusal rather than a reason to fall
// back: a keychain that answers slowly is not proven locked, so selecting
// the weaker file vault on a timeout would lower custody on a host whose
// keychain may work. It carries no account, service, nonce or value.
var ErrProbeTimeout = cascade.New(cascade.KindUnavailable,
	"custody_probe_timeout: platform keychain probe hit its deadline; custody refused")
