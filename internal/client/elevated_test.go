package client

// Purpose: DoElevated's challenge sequencing against a fake server, in the
//   default no-network unit lane (Art.7.2). The fake server sits behind the
//   real codec: each call is encoded by encodeRequest, answered from the
//   request's wire params, and decoded by decodeResponse, exactly as
//   Client.Do does around its HTTP exchange. The real socket and the real
//   elevation middleware are covered by cmd/cascade's integration round trip.
// Inputs: canned JSON-RPC responses and an injected attester.
// Outputs: request, sign and retry-envelope assertions.
// SPORT: elevated client calls.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeServerDo returns a Client.Do-shaped call that runs the real codec
// around reply, which sees the request count and the wire params.
func fakeServerDo(t *testing.T, reply func(int, json.RawMessage) string) (func(context.Context, string, any, any) error, *int) {
	t.Helper()
	requests := 0
	return func(_ context.Context, method string, params, out any) error {
		requests++
		id := requestSeq(method)
		body, err := encodeRequest(method, params, id)
		if err != nil {
			return err
		}
		var request struct{ Params json.RawMessage }
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		return decodeResponse(method, id, []byte(reply(requests, request.Params)), out)
	}, &requests
}

const elevatedChallenge = `{"error":{"code":-32008,"message":"required","data":{"nonce":"challenge"}}}`

func TestDoElevatedRetriesOnce(t *testing.T) {
	params := map[string]string{"value": "<>&"}
	wire, _ := json.Marshal(params)
	digest := sha256.Sum256(wire)
	do, requests := fakeServerDo(t, func(n int, p json.RawMessage) string {
		if n == 1 {
			return elevatedChallenge
		}
		var env struct {
			Args json.RawMessage `json:"_args"`
			Att  json.RawMessage `json:"_attestation"`
		}
		if err := json.Unmarshal(p, &env); err != nil {
			t.Fatal(err)
		}
		if string(env.Args) != string(wire) || string(env.Att) != `{"signed":true}` {
			t.Errorf("retry params: %s", p)
		}
		return `{"result":{"ok":true}}`
	})
	signs := 0
	var out struct{ OK bool }
	err := doElevated(t.Context(), "plugin.add", params, &out, func(_ context.Context, method, hash, nonce string) (json.RawMessage, error) {
		signs++
		if method != "plugin.add" || hash != hex.EncodeToString(digest[:]) || nonce != "challenge" {
			t.Errorf("attest inputs: %s %s %s", method, hash, nonce)
		}
		return json.RawMessage(`{"signed":true}`), nil
	}, do)
	if err != nil || *requests != 2 || signs != 1 || !out.OK {
		t.Fatalf("err=%v requests=%d signs=%d result=%+v", err, *requests, signs, out)
	}
}

func TestDoElevatedSecondRequiredIsTyped(t *testing.T) {
	do, requests := fakeServerDo(t, func(int, json.RawMessage) string { return elevatedChallenge })
	signs := 0
	err := doElevated(t.Context(), "plugin.add", nil, nil, func(context.Context, string, string, string) (json.RawMessage, error) {
		signs++
		return json.RawMessage(`{}`), nil
	}, do)
	if err != ErrElevationNotSatisfied || err.Error() != ErrElevationNotSatisfied.Error() || !cascade.HasKind(err, cascade.KindElevationRequired) || *requests != 2 || signs != 1 {
		t.Fatalf("err=%v requests=%d signs=%d", err, *requests, signs)
	}
}

func TestDoElevatedNoNonceNotRetried(t *testing.T) {
	for _, response := range []string{`{"error":{"code":-32008,"message":"no attestation source"}}`, `{"error":{"code":-32008,"message":"no attestation source","data":{"nonce":""}}}`} {
		do, requests := fakeServerDo(t, func(int, json.RawMessage) string { return response })
		err := doElevated(t.Context(), "approval.grant", nil, nil, func(context.Context, string, string, string) (json.RawMessage, error) {
			t.Fatal("attested without nonce")
			return nil, nil
		}, do)
		want := "approval.grant: no attestation source"
		if err == ErrElevationNotSatisfied || !cascade.HasKind(err, cascade.KindElevationRequired) || !strings.Contains(err.Error(), want) || *requests != 1 {
			t.Fatalf("err=%v requests=%d, want the server refusal %q unchanged", err, *requests, want)
		}
	}
	do, requests := fakeServerDo(t, func(int, json.RawMessage) string {
		return `{"error":{"code":-32012,"message":"unsupported","data":{"tier":"file","nonce":"not-a-challenge"}}}`
	})
	err := doElevated(t.Context(), "approval.grant", nil, nil, func(context.Context, string, string, string) (json.RawMessage, error) {
		t.Fatal("attested unrelated error")
		return nil, nil
	}, do)
	if !cascade.HasKind(err, cascade.KindUnsupported) || !strings.Contains(err.Error(), "custody tier file") || *requests != 1 {
		t.Fatalf("err=%v requests=%d", err, *requests)
	}
}

func TestElevatedErrorData(t *testing.T) {
	err := decodeResponse("plugin.add", "", []byte(`{"error":{"code":-32008,"message":"required","data":{"nonce":"n","integer":9007199254740993}}}`), nil)
	data, ok := ErrorData(err)
	if !ok || string(data) != `{"nonce":"n","integer":9007199254740993}` {
		t.Fatalf("data=%s ok=%v", data, ok)
	}
	data[0] = '!'
	again, _ := ErrorData(err)
	if again[0] != '{' {
		t.Fatal("caller changed stored data")
	}
	if _, ok := ErrorData(errors.New("unrelated")); ok {
		t.Fatal("unrelated error has data")
	}
	if _, ok := ErrorData(decodeResponse("m", "", []byte(`{"error":{"code":-32008,"message":"required"}}`), nil)); ok {
		t.Fatal("absent data was reported")
	}
	for _, data := range []string{`{"tier":"file"}`, `{}`, `null`} {
		err := decodeResponse("m", "", []byte(`{"error":{"code":-32012,"message":"unsupported","data":`+data+`}}`), nil)
		if !cascade.HasKind(err, cascade.KindUnsupported) {
			t.Fatal(err)
		}
	}
}

func TestElevatedSequenceUnit(t *testing.T) {
	challenge := decodeResponse("m", "", []byte(elevatedChallenge), nil)
	other := cascade.New(cascade.KindUnsupported, "file custody")
	for _, first := range []error{nil, challenge, other} {
		for _, second := range []error{nil, challenge, other} {
			calls, signs := 0, 0
			err := doElevated(t.Context(), "m", struct{}{}, nil, func(context.Context, string, string, string) (json.RawMessage, error) {
				signs++
				return json.RawMessage(`{}`), nil
			}, func(context.Context, string, any, any) error {
				calls++
				if calls == 1 {
					return first
				}
				return second
			})
			want, wantCalls, wantSigns := first, 1, 0
			if first == challenge {
				want, wantCalls, wantSigns = second, 2, 1
				if second == challenge {
					want = ErrElevationNotSatisfied
				}
			}
			if err != want || calls != wantCalls || signs != wantSigns {
				t.Fatalf("err=%v want=%v calls=%d signs=%d", err, want, calls, signs)
			}
		}
	}
}

func TestElevatedEarlyRefusals(t *testing.T) {
	var c *Client
	if err := c.DoElevated(t.Context(), "m", nil, nil, nil); err != ErrElevationNotSatisfied {
		t.Fatal(err)
	}
	attest := func(context.Context, string, string, string) (json.RawMessage, error) {
		return nil, cascade.New(cascade.KindPermissionDenied, "denied")
	}
	if err := c.DoElevated(t.Context(), "m", make(chan int), nil, attest); !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatal(err)
	}
	for _, data := range []string{`{"nonce":"n"}`, `{"nonce":4}`, `{}`, `null`} {
		first := decodeResponse("m", "", []byte(`{"error":{"code":-32008,"message":"required","data":`+data+`}}`), nil)
		err := doElevated(t.Context(), "m", nil, nil, attest, func(context.Context, string, any, any) error { return first })
		if data == `{"nonce":"n"}` {
			if !cascade.HasKind(err, cascade.KindPermissionDenied) {
				t.Fatal(err)
			}
		} else if err != first {
			t.Fatalf("data %s: err=%v, want the server refusal unchanged", data, err)
		}
	}
}
