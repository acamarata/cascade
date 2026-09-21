// Purpose: NewModule — this package's single assembly point, and
//   SubjectFromToken, the digest every bridge instance is identified by.
//
// Inputs: the bot token (resolved by the host from real vault custody), an
//   optional *http.Client, the egress gate, the elevation policy, the
//   cascadepa stores and a lockout sink.
//
// Outputs: a ready TelegramModule whose transport, pairing coordinator and
//   dispatch gate are all wired to the same subject.
//
// Constraints: THE TOKEN IS NEVER THE IDENTITY. SubjectFromToken hashes it,
//   so the subject that reaches the pairing store, the durable state row, the
//   device record and the "paired: <subject>" confirmation is a digest. The
//   raw token exists in exactly one place after this function returns: the
//   httpDoer's URL builder.
//
// SPORT: plugins/cascade-pa/telegram NewModule/ADDED,
//   SubjectFromToken/ADDED (P1-E23-W5-S48-T1).

package telegram

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// subjectDigestChars is how much of the token digest the subject carries.
// 16 hex characters is 64 bits: far beyond collision risk for the handful of
// bridge instances one host runs, and short enough to read in a chat reply.
const subjectDigestChars = 16

// SubjectFromToken derives a bridge instance's subject id from its bot
// token. It is one-way and stable, so the same bot always resolves to the
// same durable row and the same device record, and no caller has to handle a
// token to name a subject.
func SubjectFromToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "tg-" + hex.EncodeToString(sum[:])[:subjectDigestChars]
}

// NewModule assembles a ready module for the bot behind token.
//
// It is the one front door: a host calls this rather than separately
// constructing httpPoster/httpDoer/BotClient/pairCoordinator/TelegramModule,
// which is what keeps each of those off the test-only gate with a single real
// production caller (internal/plugins/cascadepa_bridge_wiring.go).
//
// secretScanner/quarantine back the R-21.203/R-21.105 refusal gate
// (refuse.go, quarantine.go); a nil scanner resolves to the fail-closed
// refusingSecretScanner default (T0 D1) rather than to admitting every
// message, so a caller that forgets it refuses instead of leaking. A nil
// quarantine does not change that refusal — it makes publishQuarantine
// report ErrNoQuarantineSink instead of silently dropping the record.
func NewModule(token string, httpClient *http.Client, egress EgressGate,
	elevation cascadepa.ElevationPolicy, stores *cascadepa.Stores, sink LockoutSink,
	secretScanner SecretScanner, quarantine QuarantineSink) *TelegramModule {
	subject := SubjectFromToken(token)
	doer := newAPIDoer(token, newHTTPPoster(httpClient))
	client := NewBotClient(subject, doer, egress, stores.Updates)
	pairer := newPairCoordinator(stores.Pairing, stores.Binding, sink, stores.Clock)
	module := NewTelegramModule(subject, client, stores.Binding, pairer, elevation, stores.Clock)
	module.secretScanner = secretScanner
	module.quarantine = quarantine
	return module
}
