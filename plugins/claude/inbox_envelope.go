// Package claude (this file): the R-21.143/R-21.154/R-21.178 inbox push
// envelope.
//
// Purpose: InboxEvent (the plugin-local DTO the host adapter builds from a
// stamped internal/notify.Notification), Envelope (pkg/inboxenv's wire shape
// {id, class, origin_scope, data_class, trust, summary}) and BuildEnvelope,
// which carries the event's stamped class and trust, escapes and bounds it.
//
// Inputs: an InboxEvent (its DataClass and Trust stamped at notify
// ingestion) and a requested data class.
// Outputs: an Envelope, or a typed A-T7 refusal: provider.ErrDataClassDowngrade
// for a request below the event's class, ErrEnvelopeTooLarge past the 16 KB
// bound, ErrEnvelopeInvalid for an event with no id.
//
// Constraints: the data class type is pkg/provider.SensitivityTier (no
// parallel vocabulary); this package never derives or lowers a class or
// raises a trust (stamping is internal/notify's job); escaping, the summary
// and the bound come from pkg/inboxenv; the body is NEVER placed on the
// envelope, only a marked excerpt of at most half of it; plugins/** imports
// pkg/** only (Art.10.2).
//
// SPORT: plugins/claude/inbox-push-envelope + inbox-data-class (ADD).
package claude

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/inboxenv"
	"github.com/acamarata/cascade/pkg/provider"
)

// InboxEvent carries only what a W/S-49.T1 router delivery exposes. The
// host adapter (internal/plugins/claude_inbox_push.go) is the only place
// that builds one from the real notify.Notification.
type InboxEvent struct {
	ID            string
	Class         string
	OriginScope   string
	TargetScope   string
	TargetSession string
	TargetTask    string
	// Visibility is the router's RESOLVED R-16.60 value.
	Visibility    string
	CorrelationID string
	// ExpiresAt is the expiry; the zero time means none.
	ExpiresAt time.Time
	// Body is the record's content. Only a marked excerpt of it ever
	// leaves this package.
	Body []byte
	// DataClass is the class notify stamped at ingestion. The zero value is
	// SensitivityRestricted (fail closed); an invalid value refuses the build.
	DataClass provider.SensitivityTier
	// Trust is the provenance notify stamped at ingestion. Anything but a
	// declared member is carried as untrusted-source.
	Trust provider.Provenance
}

// Envelope is the exact R-21.178 wire shape, shared with every other
// injection surface through pkg/inboxenv.
type Envelope = inboxenv.Envelope

// maxEnvelopeBytes is R-21.154's encoded-envelope bound (16 KB).
const maxEnvelopeBytes = inboxenv.MaxEncodedBytes

var (
	// ErrEnvelopeTooLarge refuses an encoded envelope or record over 16 KB.
	ErrEnvelopeTooLarge = inboxenv.ErrEnvelopeTooLarge
	// ErrEnvelopeInvalid refuses an event with no id.
	ErrEnvelopeInvalid = cascade.New(cascade.KindInvalidInput, "claude: inbox event has no id")
)

// ErrDataClassInvalid refuses an event or request carrying a tier outside
// the four declared members.
var ErrDataClassInvalid = cascade.New(cascade.KindInvalidInput, "claude: inbox data class is not a declared tier")

// carryDataClass returns the class an envelope carries: ev's own, which a
// request may only RAISE. A request less restrictive than the event's class
// is a downgrade and is refused with the typed A-T7 error.
func carryDataClass(ev InboxEvent, requested provider.SensitivityTier) (provider.SensitivityTier, error) {
	if !ev.DataClass.Valid() || !requested.Valid() {
		return 0, ErrDataClassInvalid
	}
	if ev.DataClass.MoreRestrictiveThan(requested) {
		return 0, cascade.Wrapf(cascade.KindInvalidInput, provider.ErrDataClassDowngrade,
			"claude: requested data class %q is below the event class %q", requested, ev.DataClass)
	}
	return requested, nil
}

// carryTrust returns the trust an envelope carries: the stamped member, or
// untrusted-source for anything else, so a missing stamp never reads as
// trusted.
func carryTrust(p provider.Provenance) provider.Provenance {
	if p == provider.ProvenanceTrusted {
		return p
	}
	return provider.ProvenanceUntrustedSource
}

// BuildEnvelope carries ev's class (raised by requested, never lowered) and
// stamped trust, builds the escaped summary-only Envelope and checks the
// 16 KB bound. A caller that wants no raise passes ev.DataClass.
func BuildEnvelope(ev InboxEvent, requested provider.SensitivityTier) (Envelope, error) {
	if ev.ID == "" {
		return Envelope{}, ErrEnvelopeInvalid
	}
	class, err := carryDataClass(ev, requested)
	if err != nil {
		return Envelope{}, err
	}
	env := Envelope{
		ID:          inboxenv.EscapeControl(ev.ID),
		Class:       inboxenv.EscapeControl(ev.Class),
		OriginScope: inboxenv.EscapeControl(ev.OriginScope),
		DataClass:   class.String(),
		Trust:       carryTrust(ev.Trust).String(),
		Summary:     inboxenv.Summarize(ev.Body),
	}
	if _, err := encodeBounded(env); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

// guardControl is inboxenv.EscapeControl without the '&' rule. The renderer
// applies it to fields that EscapeControl already processed, so it is a no-op for
// them (no double escaping), while still neutralising any raw value.
func guardControl(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		default:
			writeGuardedRune(&b, r)
		}
	}
	return b.String()
}

// writeGuardedRune writes r, or its visible \u escape when r is a line
// break, a control character or an invisible format character (Cf covers
// bidi overrides and zero-width joiners).
func writeGuardedRune(b *strings.Builder, r rune) {
	if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || unicode.Is(unicode.Cf, r) {
		fmt.Fprintf(b, `\u%04x`, r)
		return
	}
	b.WriteRune(r)
}

// encodeBounded JSON-encodes v and refuses a result over maxEnvelopeBytes.
func encodeBounded(v any) ([]byte, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "claude: encode inbox envelope")
	}
	if len(encoded) > maxEnvelopeBytes {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, ErrEnvelopeTooLarge,
			"claude: encoded inbox envelope is %d bytes, over the %d byte bound", len(encoded), maxEnvelopeBytes)
	}
	return encoded, nil
}
