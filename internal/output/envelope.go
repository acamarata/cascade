package output

// This file (envelope.go) holds the --json versioned envelope wire type
// and the NDJSON stream writer (D/S-06.T5 task 2). Split out of output.go
// to stay under Art.10.3's 300-line/file cap (R-14.117) — a
// behavior-preserving split, same package, no signature changes to
// output.go's public surface.
//
// Inputs: command result data (any) or an error, plus pkg/cascade's frozen
//   error taxonomy (R-14.2/R-14.3) for the error envelope's kind/code/data.
// Outputs: Envelope — the single JSON object every --json command emits —
//   and NDJSONWriter, which emits one such value (or any other JSON value)
//   per line for streaming commands.
// Constraints: Envelope's JSON shape is a PUBLIC, VERSIONED contract
//   (EnvelopeVersion) — struct field order and json tags here are the wire
//   format; changing them without bumping EnvelopeVersion breaks every
//   downstream script parsing --json output. See docs/cli-output-contract.md.
// SPORT: internal/output [ADD] (D/S-06.T5 sport_updates).

import (
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// EnvelopeVersion is the current --json wire-format version. Bump it only
// on an incompatible shape change (a field removed, renamed, or changing
// type); additive, backward-compatible fields do not require a bump. The
// version travels in every envelope so a script can detect drift before it
// parses the rest.
const EnvelopeVersion = 1

// Envelope is the --json output contract: a single JSON object every
// command emits on --json, whether it succeeds or fails. Field order here
// is the wire order (encoding/json marshals struct fields in declaration
// order, which is what keeps the golden fixtures byte-stable — no map at
// this top level).
type Envelope struct {
	// Version is EnvelopeVersion at marshal time.
	Version int `json:"version"`
	// OK is true on success, false on failure. Exactly one of Data/Error is
	// populated, matching OK.
	OK bool `json:"ok"`
	// Data is the command's result payload on success. Omitted (not merely
	// null) when there is none.
	Data any `json:"data,omitempty"`
	// Error describes the failure when OK is false. Omitted on success.
	Error *EnvelopeError `json:"error,omitempty"`
}

// EnvelopeError is the envelope's error member: the A-T7 taxonomy's
// kind/code/message/data wire mapping (full_desc), reusing pkg/cascade's
// own RPCError code table (wire.go) rather than defining a second one.
type EnvelopeError struct {
	// Kind is the taxonomy Kind's stable string name (kinds.go's
	// kindNames — e.g. "not-found"), or "internal" for a non-taxonomy
	// error.
	Kind string `json:"kind"`
	// Code is the JSON-RPC application error code for Kind (codes.go),
	// via cascade.NewRPCError — the same table plugin RPC uses, so a
	// script correlating CLI --json output with RPC responses sees one
	// numbering.
	Code int `json:"code"`
	// Message is the human-readable error text (err.Error()).
	Message string `json:"message"`
	// Data optionally carries structured detail. Always empty for taxonomy
	// errors constructed by this ticket's own code paths; reserved for a
	// future command that has a reason to attach one (cascade.RPCError.Data
	// passes through unchanged).
	Data any `json:"data,omitempty"`
}

// NewOKEnvelope builds a successful envelope carrying data.
func NewOKEnvelope(data any) Envelope {
	return Envelope{Version: EnvelopeVersion, OK: true, Data: data}
}

// NewErrEnvelope builds a failing envelope for err. err's Kind is
// recovered via cascade.KindOf when its chain carries a taxonomy error;
// otherwise the envelope's Kind reports "internal" and Code falls back to
// RPCCodeInternal, mirroring cascade.NewRPCError's own fallback so the two
// stay in lockstep by construction rather than by convention.
// NewErrEnvelope(nil) returns a zero-value OK envelope — callers that
// always route through Fail (output.go) never hit this case, but the
// function stays total rather than panicking on a nil argument.
func NewErrEnvelope(err error) Envelope {
	if err == nil {
		return NewOKEnvelope(nil)
	}
	kind, ok := cascade.KindOf(err)
	if !ok {
		kind = cascade.KindInternal
	}
	rpc := cascade.NewRPCError(err)
	return Envelope{
		Version: EnvelopeVersion,
		OK:      false,
		Error: &EnvelopeError{
			Kind:    kind.String(),
			Code:    rpc.Code,
			Message: rpc.Message,
			Data:    rpc.Data,
		},
	}
}

// MarshalLine renders e as indented, human-readable JSON (a single --json
// invocation emits exactly one envelope, so readability outranks
// compactness — unlike NDJSONWriter, which must stay one line per record).
// The trailing newline makes the output well-behaved when piped into
// line-oriented tools. Marshal failures are wrapped as a taxonomy error
// (KindInternal): encoding/json only fails this way for a type this
// package does not control (a caller's Data value with an unsupported
// kind, e.g. a channel or func), which is exactly the class of failure the
// taxonomy's internal fallback exists for.
func (e Envelope) MarshalLine() ([]byte, error) {
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "marshal json envelope")
	}
	return append(b, '\n'), nil
}

// NDJSONWriter streams one compact JSON value per line to an underlying
// writer — the wire format for streaming commands (07-CLI-COMMAND-TREE's
// --stream flags, fleet sessions --watch, etc.). Unlike Envelope, NDJSON
// lines are always compact: a pretty-printed value could itself contain a
// newline, which would break the one-record-per-line contract.
type NDJSONWriter struct {
	w interface{ Write([]byte) (int, error) }
}

// NDJSON returns an NDJSONWriter bound to w's stdout stream.
func (w *Writer) NDJSON() *NDJSONWriter {
	return &NDJSONWriter{w: w.stdout}
}

// Emit marshals v as compact JSON and writes it as one line. A marshal
// failure is wrapped KindInternal (v's type is not this package's
// responsibility); a write failure is wrapped via wrapWriteError, matching
// output.go's Result/Fail error-wrapping convention for stdout writes.
func (n *NDJSONWriter) Emit(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "marshal ndjson line")
	}
	b = append(b, '\n')
	if _, err := n.w.Write(b); err != nil {
		return wrapWriteError(err)
	}
	return nil
}

// wrapWriteError wraps a failed write to a process output stream as a
// taxonomy error. KindUnavailable fits best among the 14 frozen kinds: a
// stream write failing (closed pipe, disk full on a redirect target) is a
// dependency-unreachable condition a caller could plausibly retry, not a
// permanent KindInternal defect in cascade itself.
func wrapWriteError(err error) error {
	return cascade.Wrap(cascade.KindUnavailable, err, "write to output stream")
}
