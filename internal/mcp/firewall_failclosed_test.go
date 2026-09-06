package mcp

// Purpose: covers ResponseMarshaler's refusal paths. H/S-16.T5 bound a real
//   vault to the marshaler and added the error branches that come with it,
//   which dropped internal/mcp from 89.9% to 80%, under its 85 floor. Those
//   branches are the fail-closed ones, so they are the branches that most
//   need a test: every one of them exists to make the transport write
//   NOTHING rather than write something unfiltered.
// Constraints: no real vault, no real keychain, no network. The one test
//   that needs openProcessVault to fail replaces the package seam and
//   restores it.
// SPORT: internal.mcp.ResponseMarshaler/COVERED (T0, R-14.207).

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

func TestNewResponseMarshalerRefusesANilInterceptor(t *testing.T) {
	m, err := NewResponseMarshaler(nil)
	if err == nil {
		t.Fatal("NewResponseMarshaler(nil) succeeded; a marshaler with no interceptor would write unfiltered bytes")
	}
	if m != nil {
		t.Fatalf("NewResponseMarshaler(nil) returned a marshaler (%v) alongside its error", m)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("kind = %v (typed=%v), want KindInvalidInput", kind, ok)
	}
}

// TestMarshalRefusesWhenUnconfigured covers the two shapes a caller can
// hold that cannot filter: a nil marshaler and one whose firewall is nil.
// Both must refuse rather than fall back to a plain encode.
func TestMarshalRefusesWhenUnconfigured(t *testing.T) {
	resp := &Response{JSONRPC: "2.0"}

	for _, tc := range []struct {
		name string
		m    *ResponseMarshaler
	}{
		{name: "nil marshaler", m: nil},
		{name: "nil firewall", m: &ResponseMarshaler{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.m.Marshal(context.Background(), resp)
			if err == nil {
				t.Fatal("Marshal succeeded with no firewall configured; unfiltered bytes would reach the transport")
			}
			if out != nil {
				t.Fatalf("Marshal returned %d bytes alongside its error; the transport contract is to write nothing", len(out))
			}
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
				t.Fatalf("kind = %v (typed=%v), want KindUnavailable", kind, ok)
			}
		})
	}
}

// TestNewDefaultResponseMarshalerFailsClosedWithoutAVault pins the whole
// point of binding a vault: if the vault cannot be opened, construction
// FAILS. A marshaler that quietly carried on with no value source would
// leave the exact-value half of substitution silently inert, which is the
// state R-14.203 was raised to end.
func TestNewDefaultResponseMarshalerFailsClosedWithoutAVault(t *testing.T) {
	previous := openProcessVault
	t.Cleanup(func() { openProcessVault = previous })

	wantErr := errors.New("no vault on this host")
	openProcessVault = func() (egress.Vault, error) { return nil, wantErr }

	m, err := NewDefaultResponseMarshaler()
	if err == nil {
		t.Fatal("NewDefaultResponseMarshaler succeeded with no vault; substitution would have nothing to resolve against")
	}
	if m != nil {
		t.Fatal("NewDefaultResponseMarshaler returned a marshaler alongside its error")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want it to wrap %v", err, wantErr)
	}
}

// TestErrorObjectError covers the error surface an MCP client actually
// reads. A nil receiver returning "" matters because errResponse builds
// these and a caller comparing err.Error() on a nil object would otherwise
// panic on the transport's own error path.
func TestErrorObjectError(t *testing.T) {
	var nilObj *ErrorObject
	if got := nilObj.Error(); got != "" {
		t.Fatalf("(*ErrorObject)(nil).Error() = %q, want the empty string", got)
	}
	obj := &ErrorObject{Code: -32600, Message: "invalid request"}
	if got := obj.Error(); got != "invalid request" {
		t.Fatalf("Error() = %q, want the message", got)
	}
}

// unmarshalableResult is a value encoding/json cannot encode, used to
// drive Marshal's encode-failure branch.
type unmarshalableResult struct{ Ch chan int }

// TestMarshalRefusesAnUnencodableResponse covers the encode-failure path.
// It matters for the same reason the others do: the transport must receive
// an error and NO bytes, rather than a truncated fragment of a response
// that was never filtered.
func TestMarshalRefusesAnUnencodableResponse(t *testing.T) {
	m, err := NewResponseMarshaler(passthroughInterceptor{})
	if err != nil {
		t.Fatalf("NewResponseMarshaler: %v", err)
	}
	out, err := m.Marshal(context.Background(), &Response{
		JSONRPC: "2.0",
		Result:  unmarshalableResult{Ch: make(chan int)},
	})
	if err == nil {
		t.Fatal("Marshal encoded a response containing a channel; want a refusal")
	}
	if out != nil {
		t.Fatalf("Marshal returned %d bytes alongside its error", len(out))
	}
}

// passthroughInterceptor is the minimal ResponseInterceptor: it returns
// what it is given. It exists so Marshal's ENCODE path can be tested
// without also exercising the firewall, which has its own tests.
type passthroughInterceptor struct{}

func (passthroughInterceptor) Intercept(_ context.Context, _ egress.Capability, _ egress.SensitivityTier, raw []byte) ([]byte, error) {
	return raw, nil
}

// TestDispatchToolsCallRejectsMalformedParams covers the decode-failure
// branch of tools/call. The params come from untrusted input, so a
// malformed body must become a typed error response rather than a nil
// name reaching the tool registry.
func TestDispatchToolsCallRejectsMalformedParams(t *testing.T) {
	// An empty manifest source: this test never reaches a tool, it stops at
	// the params decode. (NewToolRegistry calls its source, so nil panics.)
	srv := NewServer(NewToolRegistry(func() []plugin.BuiltinRegistration { return nil }))

	resp := srv.Dispatch(context.Background(), &Frame{
		JSONRPC: "2.0",
		ID:      []byte("1"),
		Method:  "tools/call",
		Params:  []byte(`{"name": 12345}`),
	})
	if resp == nil || resp.Error == nil {
		t.Fatalf("tools/call with a non-string name returned %+v; want an error response", resp)
	}
	// The exact code depends on which validation layer rejects first; what
	// this pins is that untrusted params never reach the tool registry.
	if resp.Error.Code == 0 {
		t.Fatal("error response carried code 0")
	}
	if resp.Result != nil {
		t.Fatalf("a rejected tools/call still carried a result: %+v", resp.Result)
	}
}

// TestOpenVaultFromBuildsOverTheFileVault covers the broker and vault
// construction the response firewall depends on, WITHOUT touching the
// host's real credential store: the failing Runner makes the platform
// backend report unavailable, so custody selection lands on the temp-dir
// file vault (R-14.206).
func TestOpenVaultFromBuildsOverTheFileVault(t *testing.T) {
	vault, err := openVaultFrom(secrets.Config{
		Service:    "cascade-mcp-openvault-test",
		Dir:        t.TempDir(),
		Passphrase: "openvault-test-pass",
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("no platform keychain in this test")
		},
	})
	if err != nil {
		t.Fatalf("openVaultFrom: %v", err)
	}
	if vault == nil {
		t.Fatal("openVaultFrom returned a nil vault and no error")
	}
}

// TestOpenVaultFromRefusesAnUnusableConfig pins the fail-closed half: a
// config that selects no backend must produce an error, never a vault that
// silently resolves nothing.
func TestOpenVaultFromRefusesAnUnusableConfig(t *testing.T) {
	if _, err := openVaultFrom(secrets.Config{Dir: t.TempDir()}); err == nil {
		t.Fatal("openVaultFrom succeeded with no service and no backend; want a refusal")
	}
}
