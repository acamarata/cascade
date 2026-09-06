// Purpose: the substitution pass as a one-argument seam. The audit log,
//
//	and any other sink that writes caller-supplied text into a durable
//	record, needs "give me these bytes with the credentials tagged" and
//	nothing else. It is the same two-stage engine the egress firewall
//	runs, exposed in the shape a writer can inject.
//
// Inputs: content bytes. Outputs: substituted bytes, or an error and no
//
//	content. Never partially substituted bytes.
//
// Constraints: fail closed. A rewrite failure, content that is not valid
//
//	UTF-8 and a rewrite that still reports the text tainted all refuse.
//	No clock, no randomness, no I/O.
//
// SPORT: AUDIT_REDACTION: ADD (internal/secrets.Redactor).

package secrets

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Redactor substitutes credential material for typed vault-reference
// tags. It satisfies internal/audit.Redactor.
//
// STATED SCOPE: this redactor runs the DETECTOR half of the substitution
// pass. The exact-value half, which matches stored vault values whatever
// their shape, needs a bound vault and lives with the egress engine; a
// redactor built here does not silently claim it. What this catches is
// credential material with recognisable shape, at the confidence the
// operator configured, which is the half that works with no composition
// root behind it.
type Redactor struct {
	detector *Detector
	rewriter *Rewriter
}

// NewRedactor binds a redactor to detector. A nil detector is refused:
// a redactor that detects nothing would report every field clean, which
// is the most dangerous answer this type can give.
func NewRedactor(detector *Detector) (*Redactor, error) {
	if detector == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: a redactor needs a detector")
	}
	return &Redactor{detector: detector, rewriter: NewRewriter()}, nil
}

// Redact returns content with every detected credential span replaced by
// its typed tag.
//
// Empty content returns empty content and no error. Every failure returns
// (nil, error): a caller that gets an error must write nothing, not the
// bytes it started with.
func (r *Redactor) Redact(_ context.Context, content []byte) ([]byte, error) {
	if r == nil || r.detector == nil {
		return nil, cascade.New(cascade.KindUnavailable, "secrets: no redactor configured")
	}
	if len(content) == 0 {
		return content, nil
	}
	result, err := r.rewriter.Rewrite(content, r.detector.ScanCertain(content))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err,
			"secrets: the redaction pass could not be applied; nothing is written")
	}
	if result.Tainted {
		return nil, cascade.New(cascade.KindIntegrity,
			"secrets: the rewriter reported the content still tainted; nothing is written")
	}
	return result.Text, nil
}
