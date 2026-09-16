package auth

import (
	"context"
	"net/url"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the DECISIONS of the loopback callback leg — what
//
//	the redirect path is, what the browser is told, which redirect wins,
//	and how long the flow waits before giving up.
//
// Inputs: a prepared callback query and the PKCE values that started the
//
//	flow.
//
// Outputs: the verified authorization code, or a typed refusal.
// Constraints: this file binds NO socket and imports no net transport. The
//
//	listener that receives the redirect lives at the plugin's composition
//	root, which is the half that needs net/http; it calls in here for every
//	decision. Art.7.2 forbids an untagged test from importing net at all,
//	so keeping the decisions on this side is what makes the timeout, the
//	state check and the first-redirect-wins rule testable without a socket.
//
// SPORT: plugins/github/auth:loopback (ADD) — P1-E25-W5-S51-T1.

// CallbackPath is the path the authorization server redirects to. It is
// fixed rather than random: the listener is already private to this
// process by virtue of its ephemeral loopback port.
const CallbackPath = "/callback"

// CallbackPageBody is what the human's browser shows. It deliberately says
// nothing about whether the flow succeeded: the state check happens in
// Wait, and a browser page is not a trustworthy place to report an
// authorization result to a machine.
const CallbackPageBody = "Cascade received the GitHub response. You can close this tab."

// RecordCallback takes the FIRST redirect and drops any that follow.
//
// Separated from the HTTP handler because it is the decision — first one
// wins, never block — and the handler around it is plumbing. A second
// redirect arriving is not an error worth reporting: the flow is already
// decided by the first, and the browser has nowhere useful to show a
// complaint anyway.
func RecordCallback(incoming chan<- url.Values, query url.Values) (recorded bool) {
	select {
	case incoming <- query:
		return true
	default:
		return false
	}
}

// AwaitCallback is Wait's decision half, over a plain channel.
//
// Split out because it is the part worth testing and the only part that
// needs no socket: the bound, the state check, and what each outcome
// returns. Art.7.2 forbids an untagged test from importing net at all, so
// leaving this inside Wait would have made the timeout and verification
// paths reachable only from the integration lane — which is to say,
// unmeasured in the lane that gates coverage.
func AwaitCallback(ctx context.Context, incoming <-chan url.Values, p PKCE, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case query := <-incoming:
		return VerifyCallback(query, p)
	case <-ctx.Done():
		return "", cascade.Wrapf(cascade.KindTimeout, ctx.Err(),
			"github: no OAuth callback arrived within %s", timeout)
	}
}
