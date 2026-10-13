// Purpose: NewModuleOver — NewModule's assembly over an injected byte-level
//   Bot API transport (Poster) rather than an *http.Client (fix18 round 3).
//
// WHY A SECOND FRONT DOOR. The host that assembles this module
//   (internal/plugins) may not import net/http (internal/build's egress import
//   allowlist), and no untagged test anywhere may either (the no-network gate).
//   A host composition test therefore cannot hand NewModule an *http.Client,
//   and without a transport it cannot prove "bridge.send made exactly one
//   sendMessage call" at all. Poster sits BELOW httpDoer, so the URL builder,
//   the host pin, the method allowlist, the JSON envelope decoding and the
//   token-redaction rule all still run on every call a test drives.
//
// Inputs: the same as NewModule, with transport in place of the HTTP client.
//   A nil transport is the production default: the real HTTPS poster.
//
// Outputs: a ready TelegramModule, identical in every other respect to the
//   one NewModule builds (NewModule delegates here).
//
// Constraints: THE HOST IS STILL PINNED. transport receives the full URL, and
//   newAPIDoer builds it from telegramAPIBase alone; a Poster cannot repoint
//   the host, it can only answer the call.
//
// SPORT: plugins/cascade-pa/telegram NewModuleOver/ADDED, Poster/ADDED (fix18).

package telegram

import (
	"context"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// Poster is the byte-level Bot API transport NewModuleOver dials through: the
// same single primitive httpDoer already depends on (apicall.go's poster).
// An implementation MUST NOT return an error whose text contains url, because
// the url carries the bot token.
type Poster interface {
	Post(ctx context.Context, url string, body []byte) ([]byte, error)
}

// NewModuleOver assembles a ready module for the bot behind token over
// transport. See NewModule for every other parameter's contract.
func NewModuleOver(token string, transport Poster, egress EgressGate,
	elevation cascadepa.ElevationPolicy, stores *cascadepa.Stores, sink LockoutSink,
	secretScanner SecretScanner, quarantine QuarantineSink) *TelegramModule {
	if transport == nil {
		transport = newHTTPPoster(nil)
	}
	subject := SubjectFromToken(token)
	doer := newAPIDoer(token, transport)
	client := NewBotClient(subject, doer, egress, stores.Updates)
	pairer := newPairCoordinator(stores.Pairing, stores.Binding, sink, stores.Clock)
	module := NewTelegramModule(subject, client, stores.Binding, pairer, elevation, stores.Clock)
	module.secretScanner = secretScanner
	module.quarantine = quarantine
	return module
}
