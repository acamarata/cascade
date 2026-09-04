// Package bgem3 speaks the BGE-M3 sidecar wire protocol: the first-party
// request/response contract (SPEC.md) between cascade and the local
// embedding sidecar that computes BGE-M3 vectors out of process.
//
// Purpose: this file is the protocol half — frame encoding, the payload
//
//	types, version negotiation, the sidecar error-code table, and
//	decodeResponseFrame, the untrusted-input decoder FuzzBgeM3SidecarDecode
//	drives.
//
// Inputs: bytes from the far end of a sidecar connection, and the batch a
//
//	caller asked to embed.
//
// Outputs: framed request bytes, decoded response payloads, and
//
//	pkg/cascade taxonomy errors — never a raw encoding/json error crossing
//	the package boundary.
//
// Constraints: every byte the sidecar sends is untrusted. The decoder must
//
//	never panic, never read without a bound, and never allocate ahead of
//	the bytes that actually arrived (see readFramePayload). Pure Go, no
//	CGO; providers may import pkg/** only (Art.10.2).
//
// SPORT: providers/embeddings/bgem3 (ADD).
package bgem3

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ProtocolVersion is the wire-protocol version this client speaks, in
// MAJOR.MINOR form. SPEC.md §Version negotiation is binding: a sidecar
// answering with a different MAJOR is refused, a differing MINOR is
// accepted because minor revisions are additive only.
const ProtocolVersion = "1.0"

// opEmbed is the only operation version 1 defines: one batch embed,
// mirroring pkg/provider.Embedder's single Embed method.
const opEmbed = "embed"

// frameHeaderBytes is the length prefix's width: a 4-byte big-endian
// unsigned count of the JSON payload bytes that follow it.
const frameHeaderBytes = 4

// maxFrameBytes caps a single frame's payload. A sidecar (or anything else
// that has taken over its socket) must not be able to exhaust client
// memory by announcing a payload larger than any legitimate batch: 16 MiB
// holds roughly four thousand 1024-dimension float32 vectors in JSON, an
// order of magnitude above any batch the pipeline groups.
const maxFrameBytes = 16 << 20

// wireRequest is the request payload's JSON shape (SPEC.md §Request).
type wireRequest struct {
	ProtocolVersion string   `json:"protocol_version"`
	Op              string   `json:"op"`
	Model           string   `json:"model"`
	Dimensions      int      `json:"dimensions"`
	Inputs          []string `json:"inputs"`
}

// wireResponse is the response payload's JSON shape (SPEC.md §Response).
// Vectors and Error are mutually exclusive: a payload carrying both is a
// contract violation the client refuses rather than picking a winner.
type wireResponse struct {
	ProtocolVersion string      `json:"protocol_version"`
	Model           string      `json:"model"`
	Dimensions      int         `json:"dimensions"`
	Vectors         [][]float32 `json:"vectors,omitempty"`
	Error           *wireError  `json:"error,omitempty"`
}

// wireError is the sidecar's failure member (SPEC.md §Error model).
type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Framing sentinels. Each names one distinguishable way a frame can be
// unusable, so the client's error messages say which rather than
// collapsing every case into "bad response".
var (
	errFrameHeaderShort  = errors.New("bgem3: response frame header truncated")
	errFramePayloadShort = errors.New("bgem3: response frame payload truncated")
	errFrameEmpty        = errors.New("bgem3: response frame declares a zero-length payload")
	errFrameTooLarge     = errors.New("bgem3: response frame exceeds the protocol size cap")
	errPayloadNotJSON    = errors.New("bgem3: response payload is not a decodable JSON object")
)

// encodeFrame wraps payload in the protocol's length prefix. A payload
// over the cap is this side's own programming error (an over-large batch
// assembled locally), reported as KindInvalidInput rather than sent.
func encodeFrame(payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, cascade.New(cascade.KindInternal, "bgem3: refusing to send an empty frame")
	}
	if len(payload) > maxFrameBytes {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"bgem3: request frame of %d bytes exceeds the %d-byte protocol cap",
			len(payload), maxFrameBytes)
	}
	frame := make([]byte, frameHeaderBytes+len(payload))
	binary.BigEndian.PutUint32(frame[:frameHeaderBytes], uint32(len(payload)))
	copy(frame[frameHeaderBytes:], payload)
	return frame, nil
}

// encodeRequestFrame builds the single framed request one Embed call
// sends: the version stamp, the operation, the model identity the caller
// configured, and the batch.
func encodeRequestFrame(model string, dimensions int, inputs []string) ([]byte, error) {
	payload, err := json.Marshal(wireRequest{
		ProtocolVersion: ProtocolVersion,
		Op:              opEmbed,
		Model:           model,
		Dimensions:      dimensions,
		Inputs:          inputs,
	})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "bgem3: marshal embed request")
	}
	return encodeFrame(payload)
}

// decodeResponseFrame reads exactly one response frame from r and decodes
// its JSON payload. It is FuzzBgeM3SidecarDecode's target and must never
// panic, never block on a reader that has stopped producing (it reads a
// bounded, declared number of bytes and no more), and never allocate
// ahead of the bytes that actually arrived.
//
// Every failure is a plain error here rather than a taxonomy error: this
// function is pure over bytes, and the caller (client.go) is the layer
// that knows whether a truncated read was a canceled context or a wedged
// sidecar, which is what decides the Kind.
func decodeResponseFrame(r io.Reader) (*wireResponse, error) {
	var header [frameHeaderBytes]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, fmt.Errorf("%w: %w", errFrameHeaderShort, err)
	}
	declared := binary.BigEndian.Uint32(header[:])
	switch {
	case declared == 0:
		return nil, errFrameEmpty
	case declared > maxFrameBytes:
		return nil, fmt.Errorf("%w: declared %d bytes, cap is %d",
			errFrameTooLarge, declared, maxFrameBytes)
	}
	payload, err := readFramePayload(r, int64(declared))
	if err != nil {
		return nil, err
	}
	var resp wireResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return nil, fmt.Errorf("%w: %w", errPayloadNotJSON, err)
	}
	return &resp, nil
}

// readFramePayload reads exactly declared bytes from r.
//
// It copies incrementally into a growing buffer rather than allocating
// make([]byte, declared) up front, deliberately: a hostile peer that
// announces the full 16 MiB cap and then sends three bytes would
// otherwise cost 16 MiB per frame with nothing to show for it. The
// buffer's capacity tracks the bytes that actually arrived, so a
// truncated frame costs what it delivered, and the declared length still
// bounds the honest case.
func readFramePayload(r io.Reader, declared int64) ([]byte, error) {
	var buf bytes.Buffer
	n, err := io.CopyN(&buf, r, declared)
	if err != nil {
		return nil, fmt.Errorf("%w: read %d of %d bytes: %w",
			errFramePayloadShort, n, declared, err)
	}
	return buf.Bytes(), nil
}

// sidecarErrorKinds is SPEC.md §Error model's code table: the closed set
// of codes a version-1 sidecar may return, mapped to this repo's frozen
// pkg/cascade taxonomy. A code outside the table is not guessed at — see
// kindForSidecarCode.
var sidecarErrorKinds = map[string]cascade.Kind{
	"invalid_input":     cascade.KindInvalidInput,
	"unsupported":       cascade.KindUnsupported,
	"unavailable":       cascade.KindUnavailable,
	"timeout":           cascade.KindTimeout,
	"canceled":          cascade.KindCanceled,
	"quota_exhausted":   cascade.KindQuotaExhausted,
	"model_mismatch":    cascade.KindIntegrity,
	"internal":          cascade.KindInternal,
	"permission_denied": cascade.KindPermissionDenied,
}

// kindForSidecarCode maps a sidecar error code to a taxonomy Kind. An
// unrecognized code maps to KindInternal — the safe fallback for "the
// far end failed in a way this protocol version does not describe" —
// never to a kind that would read as a caller mistake or as a retryable
// condition the client cannot actually verify.
func kindForSidecarCode(code string) cascade.Kind {
	if kind, ok := sidecarErrorKinds[code]; ok {
		return kind
	}
	return cascade.KindInternal
}

// checkProtocolVersion enforces SPEC.md §Version negotiation against the
// version stamp a response carries: the MAJOR component must equal this
// client's, and an absent or unparsable stamp is a refusal rather than an
// assumption that the peer meant the current version. A mismatch is
// KindUnsupported: the sidecar is reachable and answering, the two sides
// simply do not share a contract.
func checkProtocolVersion(got string) error {
	wantMajor, _, err := splitVersion(ProtocolVersion)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "bgem3: own protocol version is malformed")
	}
	gotMajor, _, err := splitVersion(got)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnsupported, err,
			"bgem3: sidecar reported protocol version %q; this client speaks %s", got, ProtocolVersion)
	}
	if gotMajor != wantMajor {
		return cascade.Newf(cascade.KindUnsupported,
			"bgem3: sidecar speaks protocol major version %d, this client speaks %s",
			gotMajor, ProtocolVersion)
	}
	return nil
}

// splitVersion parses a MAJOR.MINOR version stamp. Exactly two components,
// both non-negative decimal integers; anything else is malformed.
func splitVersion(v string) (major, minor int, err error) {
	parts := strings.Split(v, ".")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("bgem3: version %q is not MAJOR.MINOR", v)
	}
	major, err = strconv.Atoi(parts[0])
	if err != nil || major < 0 {
		return 0, 0, fmt.Errorf("bgem3: version %q has a non-numeric major component", v)
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil || minor < 0 {
		return 0, 0, fmt.Errorf("bgem3: version %q has a non-numeric minor component", v)
	}
	return major, minor, nil
}
