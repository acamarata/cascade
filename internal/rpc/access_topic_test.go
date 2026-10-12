package rpc

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/acamarata/cascade/internal/principal"
)

var _ = DeclareTopicAccess("rpc.test.view", AccessOwnerView)
var _ = DeclareTopicAccess("rpc.test.view.plain", AccessOwnerView)

// visibleTopic is a recordingTopic that promises per-event filtering, so an
// OwnerView topic admits through it (sse_mux_test.go builds the base type
// without importing net/http, which this lane's hygiene gate forbids).
type visibleTopic[W, R any] struct{ recordingTopic[W, R] }

func (visibleTopic[W, R]) ConsumesEventVisible() {}

func newVisibleTopic[W, R any](rt recordingTopic[W, R]) visibleTopic[W, R] {
	return visibleTopic[W, R]{rt}
}

func eventsRequest(h *Handler, rawQuery, token, marker string) int {
	r := httptest.NewRequest("GET", "http://unix"+EventsPath, nil)
	r.URL.RawQuery = rawQuery
	r = r.WithContext(ctxWithPeerCred(ownerUID(), true))
	if token != "" {
		r.Header.Set(principal.HeaderToken, token)
	}
	if marker != "" {
		r.Header.Set(principal.HeaderOwnerCLI, marker)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

// TestEventsTopicAdmission drives GET /events admission per topic class and caller.
func TestEventsTopicAdmission(t *testing.T) {
	m, clock, token := principalFixture(t)
	var def, view, plain, unmarked context.Context
	mux := NewSSEMux(newRecordingTopic((*SSEMux).ServeHTTP, &def)).
		WithTopic("rpc.test.view", newVisibleTopic(newRecordingTopic((*SSEMux).ServeHTTP, &view))).
		WithTopic("rpc.test.view.plain", newRecordingTopic((*SSEMux).ServeHTTP, &plain)).
		WithTopic("rpc.test.unmarked", newRecordingTopic((*SSEMux).ServeHTTP, &unmarked))
	r := NewRegistry()
	r.SetAccessPolicy(func(tok string) (principal.Principal, error) { return m.Resolve(tok, clock.Now()) })
	h := NewHandlerWithSSE(r, mux)
	steps := []struct {
		name, query, token, marker string
		code                       int
		hit                        *context.Context
	}{
		{"default owner", "", "", "owner-cli", 200, &def},
		{"default anonymous", "", "", "", 403, nil},
		{"default principal", "", token, "", 403, nil},
		{"view anonymous", "topic=rpc.test.view", "", "", 403, nil},
		{"view owner", "topic=rpc.test.view", "", "owner-cli", 200, &view},
		{"view principal", "topic=rpc.test.view", token, "", 200, &view},
		{"view without consumer", "topic=rpc.test.view.plain", "", "owner-cli", 403, nil},
		{"unmarked topic", "topic=rpc.test.unmarked", "", "owner-cli", 403, nil},
		{"two topics", "topic=rpc.test.view&Topic=rpc.test.view", "", "owner-cli", 400, nil},
		{"bad escape", "topic=%zz", "", "owner-cli", 400, nil},
	}
	for _, s := range steps {
		def, view, plain, unmarked = nil, nil, nil, nil
		code := eventsRequest(h, s.query, s.token, s.marker)
		served := def != nil || view != nil || plain != nil || unmarked != nil
		if code != s.code || (s.hit != nil) != served || (s.hit != nil && *s.hit == nil) {
			t.Fatalf("%s: code %d, served %v; want %d", s.name, code, served, s.code)
		}
		pred, filtered := EventVisibleFrom(context.Background())
		if s.hit != nil {
			pred, filtered = EventVisibleFrom(*s.hit)
		}
		if s.name == "view owner" && filtered {
			t.Fatal("owner stream got a visibility predicate")
		}
		if s.name == "view principal" && (!filtered || pred("any", "")) {
			t.Fatal("principal stream without a deny-all predicate when no factory is set")
		}
	}
	assertFactoryPredicate(t, r, h, token, &view)
}

// assertFactoryPredicate checks that a configured factory's predicate reaches a principal stream.
func assertFactoryPredicate(t *testing.T, r *Registry, h *Handler, token string, view *context.Context) {
	t.Helper()
	r.SetEventVisibleFactory(func(principal.Principal) func(string, string) bool {
		return func(id, _ string) bool { return id == "own" }
	})
	*view = nil
	code := eventsRequest(h, "topic=rpc.test.view", token, "")
	if code != 200 || *view == nil {
		t.Fatalf("factory stream not served: code %d", code)
	}
	if pred, ok := EventVisibleFrom(*view); !ok || !pred("own", "") || pred("other", "") {
		t.Fatal("factory predicate not installed")
	}
}

// TestEventsMalformedTopicWithoutStrictMux refuses rather than serving an unfiltered stream.
func TestEventsMalformedTopicWithoutStrictMux(t *testing.T) {
	var seen context.Context
	r := NewRegistry()
	r.SetAccessPolicy(nil)
	h := NewHandlerWithSSE(r, newRecordingTopic((*SSEMux).ServeHTTP, &seen))
	for _, q := range []string{"topic=a&topic=b", "topic=%zz"} {
		if code := eventsRequest(h, q, "", "owner-cli"); code == 200 || seen != nil {
			t.Fatalf("%s: code %d served %v", q, code, seen != nil)
		}
	}
	if code := eventsRequest(h, "", "", "owner-cli"); code != 200 || seen == nil {
		t.Fatalf("owner default stream: code %d served %v", code, seen != nil)
	}
}
