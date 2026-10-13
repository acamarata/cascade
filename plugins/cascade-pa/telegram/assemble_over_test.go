package telegram

// Purpose (this file): NewModuleOver's own proof (fix18 round 3) — the
//   injected Poster really is the module's transport, sitting BELOW the real
//   httpDoer (URL built from the pinned host, JSON body encoded by the
//   production path), and a nil Poster resolves to the real HTTPS poster.
//
// Constraints: no network — the recording poster answers from the
//   sendmessage_ok.json fixture (testdata/README.md carries its provenance),
//   and the nil-transport case only inspects what was assembled.
//
// SPORT: plugins/cascade-pa/telegram NewModuleOver/TEST (fix18).

import (
	"context"
	"strings"
	"sync"
	"testing"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// wirePoster answers every Post with reply and records each URL and body.
type wirePoster struct {
	mu    sync.Mutex
	urls  []string
	body  []string
	reply []byte
}

func (p *wirePoster) Post(_ context.Context, url string, body []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.urls = append(p.urls, url)
	p.body = append(p.body, string(body))
	return p.reply, nil
}

// TestNewModuleOver_TransportCarriesTheRealWireCall proves an injected
// Poster receives the pinned-host sendMessage URL and the production JSON
// body, so a host test counting Poster calls counts real Bot API calls.
func TestNewModuleOver_TransportCarriesTheRealWireCall(t *testing.T) {
	stores := cascadepa.NewStores(fixedTestClock{t0()}, &okRegistrar{}, newMemState(), testPairKey(t))
	poster := &wirePoster{reply: mustReadTestdata(t, "sendmessage_ok.json")}
	module := NewModuleOver(syntheticToken, poster, &tierGate{}, nodeVerbPolicy(), stores, nil, nil, nil)

	if err := module.client.SendMessage(context.Background(), 555000111, cascadepa.TierPublic, "over the poster"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if len(poster.urls) != 1 {
		t.Fatalf("poster calls = %v, want exactly one", poster.urls)
	}
	want := telegramAPIBase + "/bot" + syntheticToken + "/" + MethodSendMessage
	if poster.urls[0] != want {
		t.Fatalf("poster url = %q, want %q", poster.urls[0], want)
	}
	if !strings.Contains(poster.body[0], `"text":"over the poster"`) || !strings.Contains(poster.body[0], `"chat_id":555000111`) {
		t.Fatalf("poster body = %s, want the production sendMessage encoding", poster.body[0])
	}
}

// TestNewModuleOver_NilTransportIsTheHTTPSPoster proves the production
// default: a nil Poster assembles the real *httpPoster, never a nil
// transport that would panic on the first call.
func TestNewModuleOver_NilTransportIsTheHTTPSPoster(t *testing.T) {
	stores := cascadepa.NewStores(fixedTestClock{t0()}, &okRegistrar{}, newMemState(), testPairKey(t))
	module := NewModuleOver(syntheticToken, nil, nil, nodeVerbPolicy(), stores, nil, nil, nil)
	doer, ok := module.client.doer.(*httpDoer)
	if !ok {
		t.Fatalf("module doer = %T, want *httpDoer", module.client.doer)
	}
	if _, ok := doer.transport.(*httpPoster); !ok {
		t.Fatalf("nil Poster assembled transport %T, want *httpPoster", doer.transport)
	}
}
