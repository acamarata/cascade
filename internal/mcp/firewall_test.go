package mcp

// Purpose: the response firewall's own unit tests. The end-to-end proof
//   that a transport routes through it lives in transport/, driving Serve;
//   these cover the construction and refusal branches that end-to-end test
//   cannot reach.
// SPORT: MCP_RESPONSE_FIREWALL: ADD (tests).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
)

// recordingInterceptor captures what the marshaler handed the firewall.
type recordingInterceptor struct {
	seen  []byte
	tier  egress.SensitivityTier
	class egress.EgressClass
	err   error
}

func (r *recordingInterceptor) Intercept(_ context.Context, token egress.Capability,
	tier egress.SensitivityTier, content []byte) ([]byte, error) {
	r.seen, r.tier, r.class = content, tier, token.Class()
	if r.err != nil {
		return nil, r.err
	}
	return append([]byte("filtered:"), content...), nil
}

func TestResponseMarshalerRoutesThroughTheFirewall(t *testing.T) {
	rec := &recordingInterceptor{}
	m, err := NewResponseMarshaler(rec)
	if err != nil {
		t.Fatalf("NewResponseMarshaler: %v", err)
	}
	out, err := m.Marshal(context.Background(), &Response{JSONRPC: "2.0", Result: "ok"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.HasPrefix(string(out), "filtered:") {
		t.Fatalf("the response did not come back from the firewall: %s", out)
	}
	if !json.Valid(rec.seen) {
		t.Fatalf("the firewall was handed something that is not JSON: %s", rec.seen)
	}
	if rec.class != egress.EgressClassMCP {
		t.Fatalf("capability class = %q, want %q", rec.class, egress.EgressClassMCP)
	}
	if rec.tier != egress.TierInternal {
		t.Fatalf("declared tier = %q, want %q", rec.tier, egress.TierInternal)
	}
}

func TestResponseMarshalerFailsClosed(t *testing.T) {
	if _, err := NewResponseMarshaler(nil); err == nil {
		t.Fatal("a marshaler with no interceptor must be refused")
	}
	var zero *ResponseMarshaler
	if _, err := zero.Marshal(context.Background(), &Response{}); err == nil {
		t.Fatal("an unconfigured marshaler must refuse rather than encode")
	}
	m, err := NewResponseMarshaler(&recordingInterceptor{err: errors.New("substitution unavailable")})
	if err != nil {
		t.Fatalf("NewResponseMarshaler: %v", err)
	}
	out, merr := m.Marshal(context.Background(), &Response{JSONRPC: "2.0"})
	if merr == nil {
		t.Fatal("a substitution failure must refuse")
	}
	if out != nil {
		t.Fatalf("a refusal returned %d bytes to write", len(out))
	}
}

func TestDefaultResponseMarshalerIsUsable(t *testing.T) {
	m, err := NewDefaultResponseMarshaler()
	if err != nil {
		t.Fatalf("NewDefaultResponseMarshaler: %v", err)
	}
	out, merr := m.Marshal(context.Background(), &Response{
		JSONRPC: "2.0", Result: map[string]string{"output": "key AKIA7YQ2XPLM4RZV6WTB here"},
	})
	if merr != nil {
		t.Fatalf("Marshal: %v", merr)
	}
	if strings.Contains(string(out), "AKIA7YQ2XPLM4RZV6WTB") {
		t.Fatalf("the default marshaler let a shaped credential through: %s", out)
	}
}

// TestUnboundVaultInventsNothing pins the stated gap: an unbound vault
// reports no entries and refuses a lookup, rather than answering with a
// value it does not have.
func TestUnboundVaultInventsNothing(t *testing.T) {
	names, err := unboundVault{}.List(context.Background())
	if err != nil || len(names) != 0 {
		t.Fatalf("List = (%v, %v), want (empty, nil)", names, err)
	}
	if _, err := (unboundVault{}).Get(context.Background(), "ANY"); err == nil {
		t.Fatal("an unbound vault must refuse a lookup")
	}
}
