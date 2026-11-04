package secrets

// Purpose: PostJSON request shape, redirect refusal, size cap and error
// kinds, run over the inferred-type fake round tripper from
// oauth_transport_unit_test.go (no net/http import, no socket).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/acamarata/cascade/pkg/cascade"
)

type otherExchanger struct{}

func (otherExchanger) Exchange(context.Context, string, url.Values) ([]byte, int, error) {
	return nil, 0, nil
}

func TestPostJSONSendsAJSONBodyAndReturnsTheReply(t *testing.T) {
	ex, calls := exchangerWith(func(rtCall) rtReply { return rtReply{status: 200, body: strings.NewReader(`{"access_token":"t"}`)} })
	body, status, err := NewJSONGrantPoster(ex).PostJSON(context.Background(), "https://idp.example/token",
		map[string]any{"grant_type": "device_code", "n": 3})
	if err != nil || status != 200 || string(body) != `{"access_token":"t"}` {
		t.Fatalf("PostJSON = %q/%d/%v", body, status, err)
	}
	c := (*calls)[0]
	if len(*calls) != 1 || c.method != "POST" || c.target.String() != "https://idp.example/token" {
		t.Fatalf("wire request wrong: %d calls, %+v", len(*calls), c)
	}
	if c.header["Content-Type"][0] != "application/json" || c.header["Accept"][0] != "application/json" {
		t.Fatalf("headers wrong: %v", c.header)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(c.body), &sent); err != nil || sent["grant_type"] != "device_code" || sent["n"] != float64(3) {
		t.Fatalf("sent body %q (%v) does not carry the payload", c.body, err)
	}
}

func TestPostJSONRefusesBadInputBeforeAnyRequest(t *testing.T) {
	ex, calls := exchangerWith(func(rtCall) rtReply { return rtReply{status: 200, body: strings.NewReader("{}")} })
	ctx, p := context.Background(), NewJSONGrantPoster(ex)
	if _, _, err := p.PostJSON(ctx, "https://idp.example/token", map[string]any{"bad": make(chan int)}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("unmarshalable payload = %v, want invalid-input", err)
	}
	if _, _, err := p.PostJSON(ctx, "https://idp.example/\x7f", nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("control char in endpoint = %v, want invalid-input", err)
	}
	if _, _, err := NewJSONGrantPoster(otherExchanger{}).PostJSON(ctx, "https://idp.example/token", nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("non-http exchanger = %v, want invalid-input", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("%d requests sent for refused inputs, want 0", len(*calls))
	}
}

func TestPostJSONMapsTransportFailures(t *testing.T) {
	down, _ := exchangerWith(func(rtCall) rtReply { return rtReply{err: errors.New("dial refused")} })
	_, _, err := NewJSONGrantPoster(down).PostJSON(context.Background(), "https://idp.example/token", nil)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("transport error = %v, want unavailable", err)
	}
	reset, _ := exchangerWith(func(rtCall) rtReply { return rtReply{status: 200, body: iotest.ErrReader(errMidBody)} })
	_, status, err := NewJSONGrantPoster(reset).PostJSON(context.Background(), "https://idp.example/token", nil)
	if !cascade.HasKind(err, cascade.KindUnavailable) || !errors.Is(err, errMidBody) || status != 200 {
		t.Fatalf("mid-body failure = %d/%v, want unavailable wrapping the read error", status, err)
	}
}

func TestPostJSONRefusesRedirectsAndOversizeBodies(t *testing.T) {
	const target = "https://evil.example/steal"
	redir, calls := exchangerWith(func(rtCall) rtReply {
		return rtReply{status: 307, header: map[string][]string{"Location": {target}}, body: strings.NewReader("moved")}
	})
	body, status, err := NewJSONGrantPoster(redir).PostJSON(context.Background(), "https://idp.example/token", map[string]any{"refresh_token": "r"})
	if !errors.Is(err, errOAuthRedirectRefused) || !cascade.HasKind(err, cascade.KindUnavailable) || body != nil || status != 307 {
		t.Fatalf("redirect = %q/%d/%v, want the unavailable redirect refusal", body, status, err)
	}
	if strings.Contains(err.Error(), target) || len(*calls) != 1 {
		t.Fatalf("redirect error leaked the Location or re-sent (%d calls): %v", len(*calls), err)
	}
	big, _ := exchangerWith(func(rtCall) rtReply {
		return rtReply{status: 200, body: io.MultiReader(strings.NewReader(strings.Repeat("a", maxTokenResponseBytes+1)))}
	})
	body, status, err = NewJSONGrantPoster(big).PostJSON(context.Background(), "https://idp.example/token", nil)
	if !errors.Is(err, errOAuthResponseTooLarge) || !cascade.HasKind(err, cascade.KindIntegrity) || body != nil || status != 200 {
		t.Fatalf("oversize = %d bytes/%d/%v, want the integrity refusal", len(body), status, err)
	}
	exact, _ := exchangerWith(func(rtCall) rtReply {
		return rtReply{status: 200, body: strings.NewReader(strings.Repeat("a", maxTokenResponseBytes))}
	})
	if body, _, err = NewJSONGrantPoster(exact).PostJSON(context.Background(), "https://idp.example/token", nil); err != nil || len(body) != maxTokenResponseBytes {
		t.Fatalf("body of exactly the cap = %d/%v, want accepted", len(body), err)
	}
}
