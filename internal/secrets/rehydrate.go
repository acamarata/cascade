// Purpose: the rehydrate direction. A turn that has been through the
//
//	rewriter carries typed tags where credentials used to be; the executor
//	is the one place allowed to put the values back, immediately before
//	the action runs, into a buffer nothing else can observe.
//
// Inputs: tagged content bytes, plus the vault broker the process holds.
// Outputs: a *RehydratedContent whose Data carries the raw values, or an
//
//	error and nothing at all. There is no partial result on any path.
//
// Constraints: fail closed and total. An unknown NAME, a tag-like run the
//
//	grammar refuses and content that is not valid UTF-8 all refuse. The
//	returned type deliberately implements none of the standard rendering
//	interfaces, so a value cannot reach a log by being printed. No clock,
//	no randomness, no I/O beyond the vault read.
//
// SPORT: REHYDRATOR: ADD (internal/secrets.Rehydrator, RehydratedContent, Zero).
//
//	REHYDRATE_CHANNEL: ADD (internal/secrets - non-inherited buffer carrier).

package secrets

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrVaultKeyNotFound reports a tag naming a vault entry that does not
// exist. It is a refusal, never a reason to leave the tag in place: a tag
// that survives rehydration would be sent to the model as literal text
// and read as an instruction.
var ErrVaultKeyNotFound = errors.New("secrets: no vault entry for that name")

// ErrMalformedTag reports a run of bytes that opens like a typed tag but
// does not satisfy the grammar. Rehydration refuses it rather than
// passing it through: on this direction a tag-like run is either a tag
// this code must resolve or an attempt to smuggle one past the parser,
// and neither is safe to forward.
var ErrMalformedTag = errors.New("secrets: malformed typed tag")

// RehydratedContent holds content with raw vault values injected.
//
// EXECUTOR-ONLY. The value lives from Rehydrate to the executor's return
// and no longer. The caller calls Zero in a defer immediately after
// Rehydrate, on the success path and the error path alike.
//
// The type implements no rendering interface on purpose: not
// fmt.Stringer, not fmt.GoStringer, not encoding.TextMarshaler, not
// json.Marshaler and not slog.LogValuer. A logging call that is handed
// one prints a struct address, never a credential. rehydrate_test.go
// asserts each of those five negatives at compile time.
type RehydratedContent struct {
	// Data is the rehydrated content. It aliases a buffer this package
	// allocated; Zero overwrites that buffer in place.
	Data []byte
}

// Zero overwrites every byte of the backing array with 0x00 and drops the
// slice. It is idempotent: calling it on an already-zeroed or zero-valued
// RehydratedContent is a no-op rather than a panic, because a deferred
// Zero on an error path must be safe to run against whatever the caller
// holds.
func (rc *RehydratedContent) Zero() {
	if rc == nil {
		return
	}
	buf := rc.Data[:cap(rc.Data)]
	for i := range buf {
		buf[i] = 0
	}
	rc.Data = nil
}

// Rehydrator resolves typed tags to vault values at the action boundary.
// The zero value is not usable; build one with NewRehydrator.
type Rehydrator struct {
	broker *Broker
}

// NewRehydrator binds a rehydrator to broker. A nil broker is refused:
// a rehydrator with no vault could only ever fail, and a component that
// fails on every call reads to an operator exactly like one that is
// working and finding nothing.
func NewRehydrator(broker *Broker) (*Rehydrator, error) {
	if broker == nil {
		return nil, cascade.New(cascade.KindInvalidInput,
			"secrets: a rehydrator needs a vault broker")
	}
	return &Rehydrator{broker: broker}, nil
}

// Rehydrate replaces every typed tag in content with the raw value the
// vault holds under the tag's NAME.
//
// Approved use, and the only approved use:
//
//	rc, err := rehydrator.Rehydrate(ctx, action.Content)
//	if err != nil {
//		return err
//	}
//	defer rc.Zero()
//	return executor.Run(ctx, rc.Data)
//
// Nil or empty content returns (nil, nil): no allocation and no vault
// call. Every other failure returns (nil, error), so no caller can ever
// observe a partially rehydrated buffer.
func (r *Rehydrator) Rehydrate(ctx context.Context, content []byte) (*RehydratedContent, error) {
	if r == nil || r.broker == nil {
		return nil, cascade.New(cascade.KindUnavailable, "secrets: no rehydrator configured")
	}
	if len(content) == 0 {
		return nil, nil
	}
	if !utf8.Valid(content) {
		return nil, cascade.Wrap(cascade.KindInvalidInput, ErrMalformedTag,
			"secrets: content must be valid UTF-8 before it can be rehydrated")
	}
	spans, err := scanRehydrationSpans(content)
	if err != nil {
		return nil, err
	}
	if len(spans) == 0 {
		out := make([]byte, len(content))
		copy(out, content)
		return &RehydratedContent{Data: out}, nil
	}
	return r.build(ctx, content, spans)
}

// build resolves every span's value first and only then assembles the
// output, so a lookup that fails on the last tag cannot leave the first
// tag's value in a buffer the caller sees.
func (r *Rehydrator) build(ctx context.Context, content []byte, spans []rehydrationSpan) (*RehydratedContent, error) {
	values := make([][]byte, len(spans))
	for i, span := range spans {
		value, err := internalGet(ctx, r.broker, span.tag.Name)
		if err != nil {
			zeroAll(values)
			return nil, err
		}
		values[i] = value
	}
	out := assemble(content, spans, values)
	zeroAll(values)
	return &RehydratedContent{Data: out}, nil
}

// assemble stitches the output from the untagged runs and the values.
func assemble(content []byte, spans []rehydrationSpan, values [][]byte) []byte {
	size := len(content)
	for i, span := range spans {
		size += len(values[i]) - (span.end - span.start)
	}
	if size < 0 {
		size = 0
	}
	out := make([]byte, 0, size)
	cursor := 0
	for i, span := range spans {
		out = append(out, content[cursor:span.start]...)
		out = append(out, values[i]...)
		cursor = span.end
	}
	return append(out, content[cursor:]...)
}

// zeroAll wipes the intermediate value buffers. They are copies the
// custody backend handed back, and they are exactly as sensitive as the
// output; leaving them for the collector would put the values in freed
// heap pages the process may hand to something else.
func zeroAll(values [][]byte) {
	for _, v := range values {
		for i := range v {
			v[i] = 0
		}
	}
}

// internalGet reads one vault value WITHOUT the elevated-verb gate.
//
// Rehydration happens once per dispatched action, inside the executor. If
// it went through Broker.Get it would raise an interactive attestation
// prompt per action, which is not a security control anybody can answer
// honestly at that rate, and in a release build (where the elevated verbs
// are refused outright) it would make every tagged action unrunnable.
//
// It is unexported and lives in this package precisely so the exemption
// cannot spread: no package outside internal/secrets can call it in any
// Go program. Name validation still runs, and the custody backend is
// still the only source of a value.
func internalGet(ctx context.Context, b *Broker, name string) ([]byte, error) {
	if b == nil || b.custody == nil {
		return nil, ErrNoCustodyAvailable()
	}
	if err := validateSecretName(name); err != nil {
		return nil, err
	}
	value, err := b.custody.Get(ctx, name)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return nil, cascade.Wrapf(cascade.KindNotFound, ErrVaultKeyNotFound,
				"secrets: rehydrating <%s>: %v", name, err)
		}
		return nil, err
	}
	if value == nil {
		return nil, cascade.Wrapf(cascade.KindNotFound, ErrVaultKeyNotFound,
			"secrets: rehydrating <%s>", name)
	}
	// A copy, because the caller zeroes what it is given and a custody
	// backend is free to hand back a slice it still owns.
	return append([]byte(nil), value...), nil
}
