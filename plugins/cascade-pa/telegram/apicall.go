// Purpose: httpDoer — the production Doer. It builds the Bot API URL, pins
//   the host, refuses any method outside the allowlist, and is the one place
//   a bot token is ever concatenated into a string.
//
// Inputs: the bot token, the API base, and a poster (the single HTTP
//   primitive; net/http lives in transport.go so this file and its tests
//   stay net-free per Art.7.2).
//
// Outputs: the decoded envelope, or a typed error that NEVER carries the
//   token.
//
// Constraints:
//   - THE TOKEN NEVER LEAVES. The Bot API puts the token in the PATH, so
//     net/http's *url.Error — which embeds the whole URL — is a token
//     disclosure in any error string it reaches. An earlier draft wrapped it
//     directly, so one connection refusal put the bot token into the error
//     an operator sees and any log line that error reached. Nothing here
//     wraps a transport error: transport.go hands back a fixed sentinel
//     instead, and postFailed below is built from a fixed message plus the
//     METHOD name only.
//   - METHOD ALLOWLIST. Only the three constants wire.go declares may be
//     called; anything else (getFile above all) is a typed refusal. This is
//     what gives "no getFile call exists" a way to fail: the assertion is
//     enforced, not merely unexercised.
//
// SPORT: plugins/cascade-pa/telegram httpDoer/ADDED, methodAllowed/ADDED
//   (P1-E23-W5-S48-T1).

package telegram

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// poster is the single HTTP primitive httpDoer needs. It exists so the URL
// construction, the host pin, the method allowlist and the token-redaction
// rule are all testable without importing net/http into this package's
// untagged tests.
type poster interface {
	// Post sends body to url and returns the response bytes. An
	// implementation MUST NOT return an error whose text contains url: the
	// url carries the bot token.
	Post(ctx context.Context, url string, body []byte) ([]byte, error)
}

// httpDoer is the production Doer over api.telegram.org.
type httpDoer struct {
	token     string
	base      string
	transport poster
}

// newAPIDoer builds the doer. The base is always this package's pinned
// constant — there is no parameter for it, so no caller can repoint the
// dialed host, and a test proves the pin by asserting on the URL the poster
// received.
func newAPIDoer(token string, transport poster) *httpDoer {
	return &httpDoer{token: token, base: telegramAPIBase, transport: transport}
}

// methodAllowed reports whether method is one of the three this module may
// call.
func methodAllowed(method string) bool {
	switch method {
	case MethodGetUpdates, MethodSendMessage, MethodAnswerCallbackQuery:
		return true
	default:
		return false
	}
}

// errMethodNotAllowed is the refusal for any method outside the allowlist.
func errMethodNotAllowed(method string) error {
	return cascade.Newf(cascade.KindUnsupported,
		"cascade-pa/telegram: %q is not one of the three Bot API methods this module may call", method)
}

// postFailed is the ONLY error a transport failure produces. It names the
// method and nothing else: no URL, no *url.Error text, no token.
func postFailed(method string) error {
	return cascade.Newf(cascade.KindUnavailable,
		"cascade-pa/telegram: the %s call did not reach the Bot API", method)
}

// Do posts params as JSON to method's endpoint and decodes the envelope.
func (d *httpDoer) Do(ctx context.Context, method string, params, out any) error {
	if !methodAllowed(method) {
		return errMethodNotAllowed(method)
	}
	body, err := json.Marshal(params)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "cascade-pa/telegram: encode request")
	}
	if d.transport == nil {
		return cascade.New(cascade.KindUnavailable,
			"cascade-pa/telegram: no HTTP transport is configured")
	}
	raw, err := d.transport.Post(ctx, d.methodURL(method), body)
	if err != nil {
		// Deliberately NOT wrapped: see this file's doc comment. The
		// underlying error may embed the token-bearing URL.
		return postFailed(method)
	}
	return decodeEnvelope(raw, out)
}

// methodURL builds the Bot API endpoint. The base is this package's pinned
// constant and never a caller-supplied host.
func (d *httpDoer) methodURL(method string) string {
	return d.base + "/bot" + d.token + "/" + method
}

// tokenAbsent reports whether s contains no part of token. It is exported to
// the package (not the module) so both the doer's own tests and the module's
// can assert the same property one way.
func tokenAbsent(s, token string) bool {
	return token == "" || !strings.Contains(s, token)
}
