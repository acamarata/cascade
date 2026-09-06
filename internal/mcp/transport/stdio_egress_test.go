package transport_test

// Purpose: the proof that an MCP response is an outbound crossing the
//   egress engine actually sees. It drives Serve, the real entry point,
//   and asserts on the bytes that reach the writer - not on a helper
//   called in isolation.
// Constraints: no vault is bound in this lane, so the assertion is on the
//   detector half of the substitution pass, which is the half that runs
//   without a composition root.
// SPORT: MCP_RESPONSE_FIREWALL: ADD (tests).

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/transport"
)

// leakedKey has credential shape, so the detector half of the
// substitution pass recognises it with no vault bound.
// The literal is SPLIT so the source carries no contiguous match:
// GitHub push protection blocks a push containing an AWS key ID
// shape, and this fixture is synthetic but correctly shaped, which
// is the whole point of it (R-14.202). The runtime value is
// unchanged, so the detector still sees a credential.
const leakedKey = "AKIA" + "7YQ2XPLM4RZV6WTB"

// leakingDispatcher returns a tool result carrying a credential, which is
// exactly what a tool that read a config file would produce.
func leakingDispatcher() fakeDispatcher {
	return fakeDispatcher{fn: func(_ context.Context, f *mcp.Frame) *mcp.Response {
		return &mcp.Response{JSONRPC: "2.0", ID: f.ID, Result: map[string]any{
			"output": "the key is " + leakedKey,
		}}
	}}
}

// TestStdioResponseIsFilteredOnTheRealPath drives Serve end to end and
// asserts the credential never reaches the writer. Removing the firewall
// call in StdioTransport.writeResponse turns this test red.
func TestStdioResponseIsFilteredOnTheRealPath(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","mcp_method":"tools/call",` +
		`"mcp_name":"c","id":1,"params":{"name":"x"}}` + "\n")
	out := &bytes.Buffer{}
	tr := transport.NewStdioTransport(leakingDispatcher(), in, out)

	if err := tr.Serve(context.Background()); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	written := out.String()
	if written == "" {
		t.Fatal("Serve wrote nothing")
	}
	if strings.Contains(written, leakedKey) {
		t.Fatalf("the raw credential reached the wire: %s", written)
	}
	if !strings.Contains(written, "</apikey>") {
		t.Fatalf("the response was not substituted; the marshal path is not routed through egress: %s", written)
	}
}

// TestStdioRefusesToWriteWithoutAFirewall covers the fail-closed half: a
// transport whose marshaler could not be built writes nothing at all
// rather than falling back to a plain encode.
func TestStdioRefusesToWriteWithoutAFirewall(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"tools/list","mcp_method":"tools/list","mcp_name":"c","id":1}` + "\n")
	out := &bytes.Buffer{}
	tr := transport.NewStdioTransport(echoOK(), in, out).WithResponseMarshaler(nil)

	if err := tr.Serve(context.Background()); err == nil {
		t.Fatal("a transport with no response firewall must refuse to serve")
	}
	if out.Len() != 0 {
		t.Fatalf("a refusing transport wrote %d bytes: %q", out.Len(), out.String())
	}
}
