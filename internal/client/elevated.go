package client

// Purpose: answer one elevation challenge without changing the signed
//   arguments: the params are marshaled once, the challenge nonce is read
//   from the preserved server error data, and the call is retried once.
// Inputs: params and an injected attester. Outputs: result or typed refusal.
// Constraints: never retry twice; a challenge without a nonce is terminal.
// SPORT: elevated client calls.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// AttestFunc signs the hash and nonce supplied by a challenged call.
type AttestFunc func(ctx context.Context, method, actionHash, nonce string) (json.RawMessage, error)

// ErrElevationNotSatisfied is a terminal, non-retriable elevation refusal.
var ErrElevationNotSatisfied = cascade.New(cascade.KindElevationRequired, "client: elevation not satisfied")

// rpcErrorDetail keeps the JSON-RPC code and error data behind the
// taxonomy error decodeResponse returns.
type rpcErrorDetail struct {
	Code  int
	Data  json.RawMessage
	cause error
}

func (e *rpcErrorDetail) Error() string { return e.cause.Error() }
func (e *rpcErrorDetail) Unwrap() error { return e.cause }

// ErrorData returns the server's original error data through wrapped errors.
func ErrorData(err error) (json.RawMessage, bool) {
	var detail *rpcErrorDetail
	if !errors.As(err, &detail) || len(detail.Data) == 0 {
		return nil, false
	}
	return append(json.RawMessage(nil), detail.Data...), true
}

// responseError builds the taxonomy error for a JSON-RPC error member and
// keeps its data; a -32012 refusal names the custody tier it reports.
func responseError(obj *rpcErrorObject, body []byte, method string) error {
	var raw struct {
		Error struct{ Data json.RawMessage }
	}
	_ = json.Unmarshal(body, &raw)
	message := method + ": " + obj.Message
	if obj.Code == cascade.RPCCodeUnsupported {
		var data struct{ Tier string }
		if json.Unmarshal(raw.Error.Data, &data) == nil && data.Tier != "" {
			message += " (custody tier " + data.Tier + ")"
		}
	}
	return &rpcErrorDetail{Code: obj.Code, Data: raw.Error.Data, cause: cascade.New(kindForRPCError(obj), message)}
}

// DoElevated marshals params once, then answers at most one nonce challenge.
// A refusal without a nonce is returned as the server sent it; a second
// challenge after the signed retry returns ErrElevationNotSatisfied.
func (c *Client) DoElevated(ctx context.Context, method string, params any, out any, attest AttestFunc) error {
	return doElevated(ctx, method, params, out, attest, c.Do)
}

// doElevated isolates challenge sequencing from the transport for unit coverage.
func doElevated(ctx context.Context, method string, params any, out any, attest AttestFunc,
	do func(context.Context, string, any, any) error) error {
	if attest == nil {
		return ErrElevationNotSatisfied
	}
	p, err := json.Marshal(params)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "client: marshal elevated params")
	}
	err = do(ctx, method, json.RawMessage(p), out)
	var detail *rpcErrorDetail
	if !errors.As(err, &detail) || detail.Code != cascade.RPCCodeElevationRequired {
		return err
	}
	var challenge struct{ Nonce string }
	data, _ := ErrorData(err)
	if json.Unmarshal(data, &challenge) != nil || challenge.Nonce == "" {
		// Not a challenge this client can answer: surface the server's own
		// refusal unchanged (still KindElevationRequired) and never attest.
		return err
	}
	hash := sha256.Sum256(p)
	att, err := attest(ctx, method, hex.EncodeToString(hash[:]), challenge.Nonce)
	if err != nil {
		return err
	}
	envelope := struct {
		Attestation json.RawMessage `json:"_attestation"`
		Args        json.RawMessage `json:"_args"`
	}{att, p}
	err = do(ctx, method, envelope, out)
	if errors.As(err, &detail) && detail.Code == cascade.RPCCodeElevationRequired {
		return ErrElevationNotSatisfied
	}
	return err
}
