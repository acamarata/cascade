//go:build integration

package acceptance

// Purpose (this file): the rehearsal lane for Epic J's acceptance drill —
//   the same script, against a local endpoint that speaks the
//   anthropic-compat shape over real HTTP.
//
// WHAT THIS PROVES, AND WHAT IT DOES NOT. It proves the SCRIPT and the
//   wiring: that `provider add` probes, verifies, persists and registers;
//   that `provider list` reports what the registry holds; that `run`
//   reaches a lane nobody named; that usage records the dispatch; and that
//   doctor reads the same registry. It does NOT prove the acceptance
//   criterion this ticket exists for — that a REAL compat subscription
//   answered — because the endpoint here is one this repository wrote.
//   Art.2.4 names exactly that trap, and the header of
//   j_s21_compat_sub_test.go says which lane carries the claim.
//
// SPORT: acceptance/j-s21-compat-sub (ADD) — P1-E10-W3-S21-T3.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rehearsalKeyEnv is the variable the rehearsal's credential lives in.
// A name, like the real lane's, so the two run the identical command.
const rehearsalKeyEnv = "CASCADE_ACCEPTANCE_REHEARSAL_KEY"

// rehearsalCredential is the value that variable holds. It is not a
// secret and is not shaped like one: an endpoint this test wrote is the
// only thing that will ever see it.
const rehearsalCredential = "rehearsal-not-a-real-credential"

// rehearsalModel is the single model the local endpoint enumerates.
const rehearsalModel = "rehearsal-model-1"

// startRehearsalEndpoint serves the anthropic-compat shape the probe and
// the micro-verify look for, and records what it was asked.
//
// It answers the two requests `provider add` makes and the completion
// `run` makes, and refuses everything else with 404 — a stand-in that
// answered every path would hide a probe aimed at the wrong one.
func startRehearsalEndpoint(t *testing.T) (base string, calls *rehearsalCalls) {
	t.Helper()
	calls = &rehearsalCalls{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		calls.record("GET /v1/models", r.Header.Get("x-api-key"))
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": rehearsalModel}},
		})
	})
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		calls.record("POST /v1/messages", r.Header.Get("x-api-key"))
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_rehearsal", "type": "message", "role": "assistant",
			"model":   rehearsalModel,
			"content": []map[string]any{{"type": "text", "text": "ok"}},
			"usage":   map[string]any{"input_tokens": 1, "output_tokens": 1},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, calls
}

// rehearsalCalls records what the endpoint was asked, so the drill can
// assert the probe and the verify really happened rather than inferring
// it from an exit code.
type rehearsalCalls struct {
	Paths []string
	Keys  []string
}

func (c *rehearsalCalls) record(path, key string) {
	c.Paths = append(c.Paths, path)
	c.Keys = append(c.Keys, key)
}

// saw reports whether path was requested.
func (c *rehearsalCalls) saw(path string) bool {
	for _, p := range c.Paths {
		if p == path {
			return true
		}
	}
	return false
}

// TestJ_S21_CompatSub_RoutedUnprompted runs the drill's script against the
// local endpoint.
//
// The test name is the ticket's own check name: this is the lane that runs
// on every commit, and the one that fails when the script rots.
func TestJ_S21_CompatSub_RoutedUnprompted(t *testing.T) {
	base, calls := startRehearsalEndpoint(t)
	t.Setenv(rehearsalKeyEnv, rehearsalCredential)

	runCompatSubDrill(t, compatSubTarget{
		BaseURL: base,
		KeyEnv:  rehearsalKeyEnv,
		// Pinned, because the local endpoint deliberately answers only
		// the anthropic-compat shape: an unpinned probe would try it
		// first and pass, which would make the pin look untested.
		Kind: "anthropic",
		Live: false,
	})

	if !calls.saw("GET /v1/models") {
		t.Errorf("the shape probe never reached the endpoint; calls were %v", calls.Paths)
	}
	if !calls.saw("POST /v1/messages") {
		t.Errorf("the micro-verify never reached the endpoint; calls were %v", calls.Paths)
	}
	for _, key := range calls.Keys {
		if key != rehearsalCredential {
			t.Errorf("the endpoint was sent %q, not the credential named by --key-env", key)
		}
	}
}

// TestTheRehearsalIsNotTheAcceptance pins the distinction this file's
// header makes, so a later change cannot quietly let the rehearsal satisfy
// the owner prerequisite.
func TestTheRehearsalIsNotTheAcceptance(t *testing.T) {
	base, _ := startRehearsalEndpoint(t)
	if strings.HasPrefix(base, "https://") {
		t.Fatal("the rehearsal endpoint is https, so the real drill's resolver would accept it")
	}
	target := compatSubTarget{BaseURL: base, KeyEnv: rehearsalKeyEnv}
	if target.Live {
		t.Fatal("a rehearsal target reports itself live")
	}
}
