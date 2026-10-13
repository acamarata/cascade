// Package claude (this file): the R-21.143/R-21.154/R-21.178 inbox push
// envelope.
//
// Purpose: InboxEvent (the plugin-local DTO the host adapter builds from a
// real internal/notify.Notification), Envelope (the exact R-21.178 wire
// shape {id, class, origin_scope, data_class, summary}) and BuildEnvelope,
// which carries the event's class, escapes and bounds it.
//
// Inputs: an InboxEvent (its DataClass already decided by the producer) and
// a requested data class.
// Outputs: an Envelope, or a typed A-T7 refusal: provider.ErrDataClassDowngrade
// for a request below the event's class, ErrEnvelopeTooLarge past the 16 KB
// bound, ErrEnvelopeInvalid for an event with no id.
//
// Constraints: the data class type is pkg/provider.SensitivityTier (no
// parallel vocabulary); this package never derives or lowers a class
// (deriving it is the producer's job); the body is NEVER placed on the envelope, only a marked
// excerpt of at most half of it; every string field has harness control
// syntax escaped (escapeControl); plugins/** imports pkg/** only (Art.10.2).
//
// SPORT: plugins/claude/inbox-push-envelope + inbox-data-class (ADD) —
// P1-E37-W8-S73-T1.
package claude

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/acamarata/cascade/pkg/cascade"
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
	// DataClass is the event's class as the producer decided it. The zero
	// value is SensitivityRestricted (fail closed); an invalid value
	// refuses the build.
	DataClass provider.SensitivityTier
	// Trust is the producer's provenance tag. The hydrated record is data
	// either way: it is always tagged ProvenanceUntrusted.
	Trust provider.Provenance
}

// Envelope is the exact R-21.178 wire shape.
type Envelope struct {
	ID          string `json:"id"`
	Class       string `json:"class"`
	OriginScope string `json:"origin_scope"`
	DataClass   string `json:"data_class"`
	Summary     string `json:"summary"`
}

const (
	// maxSummaryRunes is R-21.178's summary bound.
	maxSummaryRunes = 200
	// maxExcerptRunes caps the body excerpt inside the summary.
	maxExcerptRunes = 160
	// maxEnvelopeBytes is R-21.154's encoded-envelope bound (16 KB).
	maxEnvelopeBytes = 16 * 1024
)

var (
	// ErrEnvelopeTooLarge refuses an encoded envelope or record over 16 KB.
	ErrEnvelopeTooLarge = cascade.New(cascade.KindInvalidInput, "claude: encoded inbox envelope exceeds the 16 KB bound")
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

// BuildEnvelope carries ev's class (raised by requested, never lowered),
// builds the escaped summary-only Envelope and checks the 16 KB bound. A
// caller that wants no raise passes ev.DataClass.
func BuildEnvelope(ev InboxEvent, requested provider.SensitivityTier) (Envelope, error) {
	if ev.ID == "" {
		return Envelope{}, ErrEnvelopeInvalid
	}
	class, err := carryDataClass(ev, requested)
	if err != nil {
		return Envelope{}, err
	}
	env := Envelope{
		ID:          escapeControl(ev.ID),
		Class:       escapeControl(ev.Class),
		OriginScope: escapeControl(ev.OriginScope),
		DataClass:   class.String(),
		Summary:     summarize(ev.Body),
	}
	if _, err := encodeBounded(env); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

// summarize returns a MARKED excerpt of body: at most half its runes and
// at most maxExcerptRunes, escaped, prefixed with the shown/total count and
// suffixed with an ellipsis, clamped to maxSummaryRunes. It never equals
// the body: the body only leaves through cascade_inbox_read (S-73.T2).
func summarize(body []byte) string {
	text := []rune(strings.ToValidUTF8(string(body), "\uFFFD"))
	if len(text) == 0 {
		return "(no body)"
	}
	shown := min(len(text)/2, maxExcerptRunes)
	prefix := fmt.Sprintf("excerpt %d/%d chars: ", shown, len(text))
	excerpt := []rune(escapeControl(string(text[:shown])))
	room := maxSummaryRunes - len([]rune(prefix)) - 1
	if len(excerpt) > room {
		excerpt = excerpt[:room]
	}
	return prefix + string(excerpt) + "…"
}

// escapeControl neutralises harness control syntax in untrusted text: the
// markup characters & < > become entities, and every line break, control
// or invisible format character becomes a visible \u escape (a newline
// renders as \u000a), so a value
// can never open a tag, close the data block or start a new line.
func escapeControl(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
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

// guardControl is escapeControl without the '&' rule. The renderer applies
// it to fields that escapeControl already processed, so it is a no-op for
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
