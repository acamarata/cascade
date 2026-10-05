// Purpose: the credential-rotation claim for key-mode dispatch. A key
//
//	rotated in the vault under a provider's AuthRef is what the very next
//	request through the SAME resolved driver carries: the key-mode driver
//	resolves the credential per request and holds no copy of it.
//
// Inputs: the real Resolver, the real GrantedCredentials over a vault
//
//	broker (in-memory custody, file grant store in t.TempDir()), the real
//	anthropic key-mode driver, and a recording transport.
//
// Constraints: no socket, no host vault, no keychain. Credential-shaped
//
//	values are assembled at run time (C22).
//
// SPORT: internal/providers/dispatch rotation tests (ADD).
package dispatch

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/provider"
)

// recordingTransport is a transport.Transport double that records the
// x-api-key header of every request and answers a minimal Messages reply.
type recordingTransport struct{ keys []string }

func (r *recordingTransport) Send(_ context.Context, _, _ string, headers map[string]string, _ []byte) (int, map[string][]string, io.ReadCloser, error) {
	r.keys = append(r.keys, headers["x-api-key"])
	body := `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	return 200, nil, io.NopCloser(strings.NewReader(body)), nil
}

func TestKeyRotationVisibleOnNextDispatch(t *testing.T) {
	ctx := context.Background()
	creds, grants, _, custody := newGrantedCredentials(t)
	rec := keyProvider("anthropic-1", registry.DriverAnthropic)
	ref := rec.AuthRef.String()
	oldKey, newKey := "sk-ant-"+"api03-old-value", "sk-ant-"+"api03-rotated-value"
	vault, err := secrets.NewBroker(custody, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Set(ctx, ref, []byte(oldKey), secrets.SetUpdate); err != nil {
		t.Fatal(err)
	}
	if _, err := grants.Issue(ctx, ref, time.Hour); err != nil {
		t.Fatal(err)
	}
	wire := &recordingTransport{}
	res, err := NewResolver(testLookup(rec), creds, runtime.SystemClock{}, wire)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := res.Resolve(ctx, provider.Selection{LaneID: "lane-1", Model: "claude-test"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
	if _, err := driver.Chat(ctx, req); err != nil {
		t.Fatalf("dispatch 1: %v", err)
	}
	set, err := vault.Set(ctx, ref, []byte(newKey), secrets.SetUpdate)
	if err != nil || !set.Replaced || set.Name != ref {
		t.Fatalf("rotate %s: %+v (err %v), want an in-place replacement", ref, set, err)
	}
	if _, err := driver.Chat(ctx, req); err != nil {
		t.Fatalf("dispatch 2: %v", err)
	}
	if len(wire.keys) != 2 || wire.keys[0] != oldKey || wire.keys[1] != newKey {
		t.Fatalf("x-api-key per call = %d calls (first matches old: %v, second matches new: %v), want old then rotated",
			len(wire.keys), len(wire.keys) > 0 && wire.keys[0] == oldKey, len(wire.keys) > 1 && wire.keys[1] == newKey)
	}
}
