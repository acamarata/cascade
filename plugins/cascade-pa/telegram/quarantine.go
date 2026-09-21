// Purpose (this file): the R-21.105 quarantine event — an opaque,
//   per-exposure identifier plus safe-to-log metadata, published once per
//   refusal refuse.go decides.
//
// Inputs: the refusal's origin (inbound text, inbound callback, or
//   outbound), the chat kind and the scanner's SecretScanOutcome.
//
// Outputs: one QuarantineEvent per refusal, handed to the module's
//   QuarantineSink.
//
// Constraints: THIS PACKAGE NEVER IMPORTS internal/ (see refuse.go's
//   header) — QuarantineKind is a plain string, not internal/events'
//   EventKind, so the composition root maps it onto the real bus's typed
//   Kind (mirroring LockoutSink's own translation in
//   internal/plugins/cascadepa_bridge_events.go's bridgeJournal, for the
//   identical reason). The payload carries no field that could hold a
//   vault-key or credential reference, a message body, or a digest of the
//   value — TestQuarantinePayloadHasNoCredentialRef proves it by
//   reflecting over QuarantineEvent's declared fields, not by inspecting
//   one populated value. Timestamps come from the module's injected clock
//   (Art.7.3): no bare time.Now anywhere in this file.
//
// SPORT: plugins/cascade-pa/telegram QuarantineEvent/ADDED,
//   QuarantineSink/ADDED, newExposureID/ADDED (P1-E23-W5-S48-T3).

package telegram

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// exposureIDBytes is how much randomness backs one exposure_id: 16 bytes
// (128 bits) is far beyond guessing range for a value that only has to be
// UNIQUE, never secret in itself — it resolves to nothing on any public
// surface (R-21.105); the exposure -> vault key mapping lives only in the
// elevated administrative lookup that ruling carves out, which this
// package does not implement.
const exposureIDBytes = 16

// QuarantineKind is the ONE event kind this file ever publishes.
const QuarantineKind = "bridge.secret.quarantined"

// The quarantine event's Origin/Severity vocabulary — the only values
// either field ever takes, so a consumer can switch on them exhaustively.
const (
	quarantineOriginInboundText     = "inbound-text"
	quarantineOriginInboundCallback = "inbound-callback"
	quarantineOriginOutbound        = "outbound"
	quarantineSeverityRefused       = "refused"
)

// QuarantineEvent is the R-21.105 payload: an opaque per-exposure id plus
// safe-to-log metadata, and nothing that could resolve to a value.
type QuarantineEvent struct {
	// ExposureID is a random per-exposure identifier (newExposureID).
	ExposureID string `json:"exposure_id"`
	// Namespace carries the detector's credential CLASS
	// (SecretScanOutcome.Class) — safe-to-log shape metadata, never a
	// vault key name or a value.
	Namespace string `json:"namespace"`
	// Origin is which gate refused: one of the quarantineOrigin*
	// constants above.
	Origin string `json:"origin"`
	// ChatKind is the Telegram chat kind ("private", "group", ...), or
	// "unknown"/"" when it could not be determined.
	ChatKind string `json:"chat_kind"`
	// At is the refusal's timestamp, from the module's injected clock.
	At time.Time `json:"at"`
	// Severity is fixed to quarantineSeverityRefused: every event this
	// file publishes is a refusal, not a graduated warning.
	Severity string `json:"severity"`
}

// QuarantineSink receives one QuarantineEvent per refusal. The host wires
// a real one over the C/S-04.T3 bus, translating QuarantineKind onto
// internal/events.EventKind (mirroring LockoutSink's own doc comment in
// pairing.go). An unwired sink does NOT silently swallow the event (T0
// D1's "refuse, never silently discard", mirroring
// internal/plugins/cascadepa_bridge_events.go's bridgeJournal, which
// documents "no discarding default in production" for the identical
// LockoutSink case): publishQuarantine reports ErrNoQuarantineSink instead.
// The refusal itself is never gated on this — refuse.go already decided it
// before publishQuarantine ever runs — so losing the record still cannot
// change a decision that already happened, the same property the discard
// used to provide, just no longer silent about it.
type QuarantineSink interface {
	EmitQuarantine(ctx context.Context, e QuarantineEvent)
}

// ErrNoQuarantineSink is the typed reason a refusal's quarantine event was
// not recorded: no QuarantineSink is wired (T0 D1). It never widens or
// narrows the refusal itself — refuse.go's refusal decision is final by
// the time publishQuarantine runs — it only names, instead of silently
// swallowing, the fact that this one exposure could not be journaled. The
// host composition root (internal/plugins/cascadepa_bridge_wiring.go)
// always wires a real sink alongside the real scanner, so a caller ever
// observing this on the production path is itself the defect: a missing
// wiring line, not a normal outcome.
var ErrNoQuarantineSink = cascade.New(cascade.KindUnavailable,
	"cascade-pa/telegram: no quarantine sink is wired; the exposure was refused but not recorded")

// randRead is crypto/rand.Read, indirected so a test can drive
// newExposureID's failure branch (TestQuarantinePublishesEvenWhenExposureIDGenerationFails)
// without touching the real CSPRNG.
var randRead = cryptorand.Read

// newExposureID mints a random, per-exposure identifier. A read failure
// is reported to the caller rather than silently returning a zero-filled
// id, which would look like a real, if unlucky, identifier.
func newExposureID() (string, error) {
	buf := make([]byte, exposureIDBytes)
	if _, err := randRead(buf); err != nil {
		return "", err
	}
	return "exp-" + hex.EncodeToString(buf), nil
}

// now reads the module's own injected clock (T0 D8): a required
// NewTelegramModule constructor argument, never m.pairer.clock — a
// nil-pairer module (nothing in this package guards against one) must not
// turn a quarantine timestamp into a nil-pointer panic inside the poll
// goroutine.
func (m *TelegramModule) now() time.Time {
	return m.clock.Now()
}

// publishQuarantine builds and emits one QuarantineEvent for a refusal,
// reporting ErrNoQuarantineSink instead of silently discarding when no
// sink is wired (T0 D1). Every call site in this package (refuse.go) is
// free to ignore the returned error exactly as
// cascadepa_bridge_events.go's bridgeJournal.publish documents for a lost
// lockout record: "a publish failure never changes a decision" — the
// refusal already happened before this runs. An exposure_id generation
// failure still publishes — with an empty id rather than skipping the
// record entirely, since "no record at all" is a worse audit gap than an
// id that failed to mint. The publish itself runs under
// context.WithoutCancel, matching bridgeJournal's precedent: a refusal
// that happened must not go unrecorded because the request driving it was
// cancelled.
func (m *TelegramModule) publishQuarantine(
	ctx context.Context, origin, chatKind string, outcome SecretScanOutcome,
) error {
	if m.quarantine == nil {
		return ErrNoQuarantineSink
	}
	id, _ := newExposureID()
	m.quarantine.EmitQuarantine(context.WithoutCancel(ctx), QuarantineEvent{
		ExposureID: id,
		Namespace:  outcome.Class,
		Origin:     origin,
		ChatKind:   chatKind,
		At:         m.now(),
		Severity:   quarantineSeverityRefused,
	})
	return nil
}
