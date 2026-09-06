package audit

// Purpose: the redaction half of the append path. An audit record names
//   what happened, and the two fields a caller writes free text into,
//   `action` and `explain`, are the two that can carry a credential into
//   a log that is by design never rewritten. The substitution pass runs
//   over both BEFORE the record is sealed, so what lands in the store is
//   already tagged.
// Inputs: the caller's Event and the injected Redactor.
// Outputs: the Event with both fields substituted, or an error and no
//   append at all.
// Constraints: fail closed. A redactor that errors refuses the append
//   rather than writing the unredacted field; there is no path that
//   inserts a record the redactor did not clear.
// SPORT: AUDIT_REDACTION: ADD (internal/audit append-path redaction seam).

import (
	"context"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Redactor is the secrets-substitution seam. It is an interface declared
// here rather than an imported type because the dependency direction is
// secrets to audit and never back: internal/secrets already imports this
// package, so this package importing it would be a cycle.
//
// Implementations return the substituted content, or an error and no
// content. internal/secrets.Redactor satisfies it.
type Redactor interface {
	// Redact returns content with credential material replaced by typed
	// vault-reference tags.
	Redact(ctx context.Context, content []byte) ([]byte, error)
}

// NewWithRedactor returns a Log that runs redactor over `action` and
// `explain` on every append.
//
// It is a second constructor rather than a method on *Log because the
// method set of *Log is itself an asserted invariant: the type exposes
// Append, Explain, Query and Verify and nothing else, so that "there is
// no update path" is provable by listing the methods. A WithRedactor
// method would have broken that proof to save one argument.
//
// STATED GAP: a Log built without one appends unredacted fields. That is
// the state of every caller today, because the process that owns the
// vault and the detector has no composition root for the substitution
// engine yet. The seam is what closes it; binding it is the mount, and
// the mount belongs to whoever builds that engine.
func NewWithRedactor(store provider.Store, clock runtime.Clock, bus Publisher, redactor Redactor) *Log {
	l := New(store, clock, bus)
	l.redactor = redactor
	return l
}

// redactEvent substitutes the two free-text fields in place.
//
// It runs BEFORE validation, so the bounds and control-character checks
// apply to what is actually stored rather than to what the caller
// offered. A substitution that pushes a field over its limit is therefore
// refused, which is the correct outcome: the record is not written and
// the caller is told, instead of a record landing with a field the
// validator never saw.
func (l *Log) redactEvent(ctx context.Context, event *Event) error {
	if l.redactor == nil {
		return nil
	}
	action, err := l.redact(ctx, "action", []byte(event.Action))
	if err != nil {
		return err
	}
	event.Action = string(action)
	if len(event.Explain) == 0 {
		return nil
	}
	explain, err := l.redact(ctx, "explain", event.Explain)
	if err != nil {
		return err
	}
	event.Explain = explain
	return nil
}

// redact runs one field through the seam and classifies its failure.
func (l *Log) redact(ctx context.Context, field string, content []byte) ([]byte, error) {
	if len(content) == 0 {
		return content, nil
	}
	out, err := l.redactor.Redact(ctx, content)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindIntegrity, err,
			"audit: %s could not be redacted; the record is not written", field)
	}
	if out == nil {
		return nil, cascade.Newf(cascade.KindIntegrity,
			"audit: the redactor returned no content for %s; the record is not written", field)
	}
	return out, nil
}
