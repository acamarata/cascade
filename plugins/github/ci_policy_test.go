// Purpose: the one assertion that keeps the CI provider policy resolver's
// token-presence probe wired to the token this plugin actually stores.
//
// The resolver itself moved one directory down, to
// plugins/github/cipolicy, for the reason that package's doc comment
// states: plugins/github is `package main`, so nothing outside it can
// import a decision declared here, and a decision with no caller is the
// gap CR-B rejected. What stays here is the coupling that cannot be
// asserted from either side alone -- the plugin writes its OAuth token
// under TokenKey(), and the host-side never-pay resolver probes the vault
// for cipolicy.TokenVaultKey. If either literal changes, the resolver
// starts reporting "no token configured" for an authorized host and every
// unmatched repo silently routes local. This test is what makes that
// drift a red build instead of a silent routing change.
//
// SPORT: plugins.github.TokenKey/TESTED (P1-E25-W5-S51-T5).
package main

import (
	"testing"

	"github.com/acamarata/cascade/plugins/github/cipolicy"
)

func TestTokenKeyMatchesPolicyVaultKey(t *testing.T) {
	if got := TokenKey(); got != cipolicy.TokenVaultKey {
		t.Errorf("TokenKey() = %q, cipolicy.TokenVaultKey = %q; the never-pay resolver probes a vault key this plugin never writes",
			got, cipolicy.TokenVaultKey)
	}
}
