//go:build integration

package egress

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRedTeamWire is the socket-level leg: the substituted bytes go
// through a real HTTP client to a real listener, every byte the server
// receives is captured, and the synthetic secret must be absent from that
// capture. It sits behind the integration tag because the default unit
// lane forbids importing net.
func TestRedTeamWire(t *testing.T) {
	for _, fixture := range []string{"mcp-response-fixture.json", "hook-response-fixture.json"} {
		t.Run(fixture, func(t *testing.T) {
			var captured bytes.Buffer
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(&captured, r.Body)
			}))
			defer server.Close()

			engine := newEngineOn(t, DefaultRegistry(), &mapVault{values: redTeamVault()})
			class := EgressClassMCP
			if fixture == "hook-response-fixture.json" {
				class = EgressClassHook
			}
			out, err := engine.InterceptClass(context.Background(), class, TierInternal, loadFixture(t, fixture))
			if err != nil {
				t.Fatalf("InterceptClass: %v", err)
			}
			resp, err := http.Post(server.URL, "application/json", bytes.NewReader(out))
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			_ = resp.Body.Close()
			assertNoCanary(t, "wire capture", captured.Bytes())
			if captured.Len() == 0 {
				t.Fatal("the wire capture is empty; the assertion would prove nothing")
			}
		})
	}
}

// TestRedTeamWireDisabledClassSendsNothing proves the disabled leg never
// opens a request at all.
func TestRedTeamWireDisabledClassSendsNothing(t *testing.T) {
	var captured bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(&captured, r.Body)
	}))
	defer server.Close()

	engine := newEngineOn(t, DefaultRegistry(), &mapVault{values: redTeamVault()})
	out, err := engine.InterceptClass(context.Background(), EgressClassTelemetry, TierInternal,
		loadFixture(t, "mcp-response-fixture.json"))
	if err == nil {
		t.Fatal("a disabled class was admitted")
	}
	if out != nil {
		resp, perr := http.Post(server.URL, "application/json", bytes.NewReader(out))
		if perr == nil {
			_ = resp.Body.Close()
		}
	}
	if captured.Len() != 0 {
		t.Fatalf("%d bytes reached the wire from a disabled class", captured.Len())
	}
}
