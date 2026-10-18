// Purpose: the elevation helper's wire protocol. One request line in, one
//
//	response line out; every failure is a typed {"ok":false,"kind":...} line
//	and never a partial success.
//
// Inputs: one line of JSON (at most maxLine bytes) and a backend.
// Outputs: one line of JSON:
//
//	request  {"v":1,"op":"status|generate|pubkey|sign|delete","payload_b64":"..."}
//	status   {"v":1,"ok":true,"available":b,"tier":"...","cgo":b}
//	generate {"v":1,"ok":true}   pubkey {"v":1,"ok":true,"pub_b64":"..."}
//	sign     {"v":1,"ok":true,"sig_b64":"..."}   delete {"v":1,"ok":true}
//	failure  {"v":1,"ok":false,"kind":"<cascade Kind>","message":"..."}
//
// Constraints: the request is decoded strictly (unknown fields, trailing
//
//	data, a payload on any op but sign, an oversize line or an oversize
//	payload are invalid-input). There is no caller-supplied prompt text: the
//	sign prompt is built here from the payload digest. Every op except
//	status runs the caller check BEFORE any backend call, so a refused caller
//	never reaches the keychain.
//
// SPORT: cmd/cascade-elevate-helper protocol/ADDED (P1-SHIP-15).
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	protocolVersion = 1
	maxLine         = 64 << 10
	maxPayload      = 4096

	deleteReason = "Cascade elevation key delete"
)

// backend is the keystore seam: the real darwin cgo keystore in production,
// an injected fake in tests.
type backend interface {
	Available() bool
	Tier() string
	CGO() bool
	Pubkey() (string, error)
	// EnrollmentAbsent is nil only when no key item exists, definitely;
	// generate must never run on any other answer.
	EnrollmentAbsent() error
	Generate() error
	Sign(payload []byte, reason string) ([]byte, error)
	// Presence evaluates device-owner authentication; delete never runs
	// before it returns nil.
	Presence(reason string) error
	DeleteKey() error
}

// server answers requests against a backend. caller verifies the peer that
// is talking to this process; it is consulted for every op but status.
type server struct {
	be     backend
	caller func() error
}

type request struct {
	V          int     `json:"v"`
	Op         string  `json:"op"`
	PayloadB64 *string `json:"payload_b64,omitempty"`
}

type failure struct {
	V       int    `json:"v"`
	OK      bool   `json:"ok"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type okReply struct {
	V  int  `json:"v"`
	OK bool `json:"ok"`
}

type statusReply struct {
	V         int    `json:"v"`
	OK        bool   `json:"ok"`
	Available bool   `json:"available"`
	Tier      string `json:"tier"`
	CGO       bool   `json:"cgo"`
}

type pubkeyReply struct {
	V      int    `json:"v"`
	OK     bool   `json:"ok"`
	PubB64 string `json:"pub_b64"`
}

type signReply struct {
	V      int    `json:"v"`
	OK     bool   `json:"ok"`
	SigB64 string `json:"sig_b64"`
}

func fail(kind cascade.Kind, msg string) failure {
	return failure{V: protocolVersion, Kind: kind.String(), Message: msg}
}

func failFromError(err error) failure {
	var ce *cascade.Error
	if errors.As(err, &ce) && ce.Msg != "" {
		return fail(ce.Kind, ce.Msg)
	}
	kind, ok := cascade.KindOf(err)
	if !ok {
		kind = cascade.KindUnavailable
	}
	return fail(kind, err.Error())
}

// readLine reads one request line of at most maxLine bytes. An oversize
// line is reported as such without reading the rest of it.
func readLine(r io.Reader) ([]byte, error) {
	br := bufio.NewReaderSize(r, 4096)
	buf := make([]byte, 0, 512)
	for {
		b, err := br.ReadByte()
		if err != nil {
			if len(buf) > 0 && errors.Is(err, io.EOF) {
				return buf, nil
			}
			return nil, err
		}
		if b == '\n' {
			return buf, nil
		}
		if len(buf) >= maxLine {
			return nil, errOversize
		}
		buf = append(buf, b)
	}
}

// errOversize is the only invalid-input error readLine returns (its other
// errors are I/O errors), so serve tells them apart by kind.
var errOversize = cascade.New(cascade.KindInvalidInput, "request line exceeds the size limit")

func decodeRequest(line []byte) (request, *failure) {
	var req request
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.V != protocolVersion {
		f := fail(cascade.KindInvalidInput, "malformed request")
		return request{}, &f
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		f := fail(cascade.KindInvalidInput, "trailing data after the request")
		return request{}, &f
	}
	return req, nil
}

// decodePayload returns the sign payload, enforcing that only sign carries
// one and that it is present, base64 and at most maxPayload bytes decoded.
func decodePayload(req request) ([]byte, *failure) {
	if req.Op != "sign" {
		if req.PayloadB64 != nil {
			f := fail(cascade.KindInvalidInput, "payload is only valid for sign")
			return nil, &f
		}
		return nil, nil
	}
	if req.PayloadB64 == nil || len(*req.PayloadB64) > base64.StdEncoding.EncodedLen(maxPayload) {
		f := fail(cascade.KindInvalidInput, "sign needs a payload of at most 4096 bytes")
		return nil, &f
	}
	payload, err := base64.StdEncoding.DecodeString(*req.PayloadB64)
	if err != nil || len(payload) == 0 || len(payload) > maxPayload {
		f := fail(cascade.KindInvalidInput, "sign needs a payload of at most 4096 bytes")
		return nil, &f
	}
	return payload, nil
}

// handle answers one request line with the value to marshal as the reply.
func (s server) handle(line []byte) any {
	req, bad := decodeRequest(line)
	if bad != nil {
		return *bad
	}
	payload, bad := decodePayload(req)
	if bad != nil {
		return *bad
	}
	switch req.Op {
	case "status":
		return statusReply{V: protocolVersion, OK: true, Available: s.be.Available(), Tier: s.be.Tier(), CGO: s.be.CGO()}
	case "generate", "pubkey", "sign", "delete":
		if err := s.caller(); err != nil {
			return failFromError(err)
		}
		return s.keyOp(req.Op, payload)
	}
	return fail(cascade.KindInvalidInput, "unknown op")
}

func (s server) keyOp(op string, payload []byte) any {
	switch op {
	case "generate":
		if err := s.be.EnrollmentAbsent(); err != nil {
			return failFromError(err)
		}
		if err := s.be.Generate(); err != nil {
			return failFromError(err)
		}
		return okReply{V: protocolVersion, OK: true}
	case "pubkey":
		pub, err := s.be.Pubkey()
		if err != nil {
			return failFromError(err)
		}
		return pubkeyReply{V: protocolVersion, OK: true, PubB64: pub}
	case "sign":
		sig, err := s.be.Sign(payload, elevation.HelperPromptReason(payload))
		if err != nil {
			return failFromError(err)
		}
		return signReply{V: protocolVersion, OK: true, SigB64: base64.StdEncoding.EncodeToString(sig)}
	}
	if err := s.be.Presence(deleteReason); err != nil {
		return failFromError(err)
	}
	if err := s.be.DeleteKey(); err != nil {
		return failFromError(err)
	}
	return okReply{V: protocolVersion, OK: true}
}

// serve reads one request from r and writes one reply line to w.
func (s server) serve(r io.Reader, w io.Writer) error {
	var reply any
	line, err := readLine(r)
	switch {
	case cascade.HasKind(err, cascade.KindInvalidInput):
		reply = fail(cascade.KindInvalidInput, errOversize.Msg)
	case err != nil:
		return err
	default:
		reply = s.handle(line)
	}
	out, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	_, err = w.Write(append(out, '\n'))
	return err
}
