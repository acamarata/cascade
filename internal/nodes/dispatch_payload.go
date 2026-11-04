package nodes

// Purpose (this file): the bounded DispatchPayload codec. The payload is
//   what a controller ships to the one node bound to a dispatch attempt:
//   the action to run, the base commit, where the result goes, the resume
//   point and the controller-signed scope envelope (pkg/envelope, ADR-0007).
// Inputs: untrusted bytes from the controller channel.
// Outputs: a DispatchPayload whose every field passed the checks below, or
//   a KindInvalidInput error before any worktree or execution exists.
// Constraints: never panics; refuses input over MaxPayloadBytes, invalid
//   UTF-8, unknown fields, trailing data, a wrong version, an unknown kind,
//   a non-hex head, empty ids, a result ref that does not name the
//   dispatch attempt and an envelope fence that does not equal it. The
//   envelope is decoded by envelope.Decode (bounded, fuzzed there). The
//   accepted bytes must equal EncodePayload of the decoded value, so the
//   receiver's sha256 of the wire bytes always equals Digest(): a payload
//   re-ordered, re-spaced or re-cased in transit is a digest mismatch.
// SPORT: internal/nodes (ADD) — P1-NODE-02b (contract:remote-dispatch-route).

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
)

// PayloadVersion is the only DispatchPayload schema version accepted.
const PayloadVersion = 1

// MaxPayloadBytes bounds one encoded DispatchPayload.
const MaxPayloadBytes = 256 << 10

// resultRefPrefix is where a dispatch attempt publishes its result.
const resultRefPrefix = "refs/cascade/results/"

// ActionKind names what a dispatch asks the node to run.
type ActionKind string

const (
	// ActionModel is a model call.
	ActionModel ActionKind = "model"
	// ActionAgent is an agent run.
	ActionAgent ActionKind = "agent"
	// ActionCI is a CI gate run.
	ActionCI ActionKind = "ci"
)

// DispatchPayload is the unit a controller ships to a bound node.
type DispatchPayload struct {
	// Version is always PayloadVersion.
	Version int `json:"v"`
	// DispatchID names the dispatch.
	DispatchID string `json:"dispatch_id"`
	// Attempt is the fenced attempt number (1 or more).
	Attempt uint64 `json:"attempt"`
	// ActionID names the action, for the node's action log.
	ActionID string `json:"action_id"`
	// Kind is what to run.
	Kind ActionKind `json:"kind"`
	// EntityID is the journal entity the action belongs to.
	EntityID string `json:"entity_id"`
	// Head is the 40-hex base commit.
	Head string `json:"head"`
	// RepoRemote is the repository the node fetches Head from.
	RepoRemote string `json:"repo_remote"`
	// ResultRef is refs/cascade/results/<dispatch>/<attempt>.
	ResultRef string `json:"result_ref"`
	// Spec is the kind-specific action specification.
	Spec json.RawMessage `json:"spec"`
	// Resume is where a replacement attempt picks up; zero is a cold start.
	Resume ResumePoint `json:"resume,omitzero"`
	// Envelope is the controller-signed scope envelope fenced to
	// (DispatchID, Attempt).
	Envelope envelope.Signed `json:"envelope"`
}

// wirePayload decodes the envelope as raw bytes so envelope.Decode, the
// bounded decoder, is the only parser that ever sees it. The outer
// Envelope field shadows the embedded one.
type wirePayload struct {
	DispatchPayload
	Envelope json.RawMessage `json:"envelope"`
}

// EncodePayload returns the one canonical encoding of p. A payload that
// DecodePayload would refuse is refused here too, so every encoded
// payload decodes on its peer.
func EncodePayload(p DispatchPayload) ([]byte, error) {
	if err := checkPayload(p); err != nil {
		return nil, err
	}
	if len(p.Envelope.Sig) != ed25519.SignatureSize {
		return nil, payloadInvalidf("envelope signature has length %d, want %d", len(p.Envelope.Sig), ed25519.SignatureSize)
	}
	if _, err := envelope.Canonical(p.Envelope.Env); err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "nodes: dispatch payload: envelope")
	}
	out, err := json.Marshal(p)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "nodes: dispatch payload: encode")
	}
	if _, err := DecodePayload(out); err != nil {
		return nil, err
	}
	return out, nil
}

// DecodePayload parses and checks one encoded payload. Every refusal is
// KindInvalidInput; it never panics.
func DecodePayload(b []byte) (DispatchPayload, error) {
	if len(b) > MaxPayloadBytes {
		return DispatchPayload{}, payloadInvalidf("payload is %d bytes, over %d", len(b), MaxPayloadBytes)
	}
	if !utf8.Valid(b) {
		return DispatchPayload{}, payloadInvalidf("payload is not valid UTF-8")
	}
	var w wirePayload
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return DispatchPayload{}, cascade.Wrap(cascade.KindInvalidInput, err, "nodes: dispatch payload: decode")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return DispatchPayload{}, payloadInvalidf("trailing data after the payload")
	}
	signed, err := envelope.Decode(w.Envelope)
	if err != nil {
		return DispatchPayload{}, cascade.Wrap(cascade.KindInvalidInput, err, "nodes: dispatch payload: envelope")
	}
	p := w.DispatchPayload
	p.Envelope = signed
	if err := checkPayload(p); err != nil {
		return DispatchPayload{}, err
	}
	again, err := json.Marshal(p)
	if err != nil || !bytes.Equal(again, b) {
		return DispatchPayload{}, payloadInvalidf("payload is not in canonical form; its digest does not match the encoded payload")
	}
	return p, nil
}

// Digest returns hex(sha256(EncodePayload(p))), or "" when p cannot be
// encoded. A decoded payload's Digest is the sha256 of its wire bytes.
func (p DispatchPayload) Digest() string {
	b, err := EncodePayload(p)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// checkPayload enforces the field rules shared by both directions.
func checkPayload(p DispatchPayload) error {
	if p.Version != PayloadVersion {
		return payloadInvalidf("version %d is not %d", p.Version, PayloadVersion)
	}
	switch p.Kind {
	case ActionModel, ActionAgent, ActionCI:
	default:
		return payloadInvalidf("unknown action kind %q", clipText(string(p.Kind)))
	}
	if err := validRefSegment(p.DispatchID); err != nil {
		return err
	}
	if p.Attempt == 0 || p.ActionID == "" || p.EntityID == "" || p.RepoRemote == "" {
		return payloadInvalidf("attempt, action_id, entity_id and repo_remote are required")
	}
	if !isCommitHex(p.Head) {
		return payloadInvalidf("head %q is not a 40-hex commit", clipText(p.Head))
	}
	want := resultRefPrefix + p.DispatchID + "/" + strconv.FormatUint(p.Attempt, 10)
	if p.ResultRef != want {
		return payloadInvalidf("result_ref %q does not name the dispatch attempt %q", clipText(p.ResultRef), want)
	}
	fence := p.Envelope.Env.Fence
	if fence.DispatchID != p.DispatchID || fence.Attempt != p.Attempt {
		return payloadInvalidf("envelope fence (%q, %d) does not equal the dispatch attempt (%q, %d)",
			clipText(fence.DispatchID), fence.Attempt, clipText(p.DispatchID), p.Attempt)
	}
	return nil
}

// validRefSegment requires a dispatch id usable as one git ref segment.
func validRefSegment(id string) error {
	if id == "" || id == "." || id == ".." || strings.HasSuffix(id, ".lock") ||
		strings.Contains(id, "..") || strings.ContainsAny(id, "/\\~^:?*[@{") {
		return payloadInvalidf("dispatch_id %q is empty or not a ref segment", clipText(id))
	}
	for _, r := range id {
		if r <= ' ' || r == 0x7f {
			return payloadInvalidf("dispatch_id %q holds a control or space character", clipText(id))
		}
	}
	return nil
}

// isCommitHex reports whether s is exactly 40 lowercase hex digits.
func isCommitHex(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// clipText bounds untrusted text quoted in an error.
func clipText(s string) string {
	const limit = 64
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}

// payloadInvalidf builds a KindInvalidInput error with the codec prefix.
func payloadInvalidf(format string, args ...any) error {
	return cascade.Newf(cascade.KindInvalidInput, "nodes: dispatch payload: "+format, args...)
}
