package scope

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestContextScopeShowMalformedParams(t *testing.T) {
	store := newTestStore(t)
	_, err := ContextScopeShow(context.Background(), ResolveDeps{Store: store}, json.RawMessage(`{not-json`))
	if err == nil {
		t.Fatal("ContextScopeShow with malformed params = nil, want error")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v, ok=%v, want KindInvalidInput", kind, ok)
	}
}

func TestContextScopeShowEmptyParamsResolvesGeneral(t *testing.T) {
	store := newTestStore(t)
	got, err := ContextScopeShow(context.Background(), ResolveDeps{Store: store, GitRoot: fakeGitRoot("/nowhere")}, nil)
	if err != nil {
		t.Fatalf("ContextScopeShow: %v", err)
	}
	if got.Kind != ScopeKindGeneral {
		t.Errorf("Kind = %q, want %q", got.Kind, ScopeKindGeneral)
	}
}

func TestContextScopeShowDecodesEveryField(t *testing.T) {
	store := newTestStore(t)
	raw, err := json.Marshal(ScopeShowParams{
		Cwd: "/nowhere", User: "u1", Machine: "m1", Branch: "b1", Task: "t1", Session: "s1", ExplicitOverrides: "eo1",
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	got, err := ContextScopeShow(context.Background(), ResolveDeps{Store: store, GitRoot: fakeGitRoot("/nowhere")}, raw)
	if err != nil {
		t.Fatalf("ContextScopeShow: %v", err)
	}
	if got.User != "u1" || got.Machine != "m1" || got.Session != "s1" || got.ExplicitOverrides != "eo1" {
		t.Errorf("ContextScopeShow did not thread every caller field through: %+v", got)
	}
}

// TestContextScopeShowPropagatesStorageFailure closes the underlying
// connection to force a genuine storage failure on the next query,
// proving ContextScopeShow surfaces it as a typed error rather than
// panicking or returning a silent empty success.
func TestContextScopeShowPropagatesStorageFailure(t *testing.T) {
	store := newTestStore(t)
	_ = store.db.Close()
	_, err := ContextScopeShow(context.Background(), ResolveDeps{Store: store, GitRoot: fakeGitRoot("/nowhere")}, nil)
	if err == nil {
		t.Fatal("ContextScopeShow over a closed db = nil, want error")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("kind = %v, ok=%v, want KindUnavailable", kind, ok)
	}
}

// FuzzContextScopeShowParams proves ContextScopeShow never panics on any
// byte sequence a caller (a malicious or corrupted RPC peer) might send as
// params -- it always returns either a resolved SessionScope or a typed
// *cascade.Error, never a panic and never a silent empty success on
// malformed input. Seed corpus: seed-valid.json (a well-formed
// ScopeShowParams object) and seed-malformed.json (truncated/invalid
// JSON), per R-21.266's package-local testdata/fuzz/<FuzzName>/
// convention -- Go auto-loads both, no f.Add call needed.
func FuzzContextScopeShowParams(f *testing.F) {
	store := newTestStore(f)
	deps := ResolveDeps{Store: store, GitRoot: fakeGitRoot("/nowhere")}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, err := ContextScopeShow(context.Background(), deps, data)
		if err != nil {
			if _, ok := cascade.KindOf(err); !ok {
				t.Fatalf("ContextScopeShow returned a non-taxonomy error for input %q: %v", data, err)
			}
		}
	})
}
