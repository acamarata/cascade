package daemon

// Purpose: covers context_assemble.go, the E/S-09.T2 daemon-side
//   registration for context.show and context.slice. That ticket verified
//   the path through cmd/cascade's integration test, which is the right
//   end-to-end proof but contributes nothing to THIS package's coverage
//   profile: every function here measured 0% and the package fell to
//   81.2%, under its 85 floor. Same shape as the recall_index.go gap
//   earlier the same day (R-14.204's sibling case).
// Constraints: Art.2 -- a real git repository and a real modernc SQLite
//   file under t.TempDir(), driven through the REAL registry.Dispatch.
//   Art.7.1 -- nothing outside t.TempDir(), no network listener.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDecodeContextAssembleParams(t *testing.T) {
	t.Run("absent params resolve to the zero value", func(t *testing.T) {
		got, err := decodeContextAssembleParams(nil)
		if err != nil {
			t.Fatalf("decodeContextAssembleParams(nil) = %v, want nil", err)
		}
		if got.Cwd != "" {
			t.Fatalf("Cwd = %q, want empty", got.Cwd)
		}
	})

	t.Run("malformed params refuse rather than defaulting", func(t *testing.T) {
		_, err := decodeContextAssembleParams(json.RawMessage(`{"cwd":`))
		if err == nil {
			t.Fatal("malformed params decoded without error; a truncated body must refuse")
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
			t.Fatalf("kind = %v (typed=%v), want KindInvalidInput", kind, ok)
		}
	})

	t.Run("an empty object is valid", func(t *testing.T) {
		if _, err := decodeContextAssembleParams(json.RawMessage(`{}`)); err != nil {
			t.Fatalf("an empty object refused: %v", err)
		}
	})
}

// TestRegisterContextAssembleHandler_RegistersAndDispatches drives both
// methods through the REAL registry rather than calling the compute
// functions directly, so a registration that is present but broken cannot
// pass.
func TestRegisterContextAssembleHandler_RegistersAndDispatches(t *testing.T) {
	dir := t.TempDir()
	seedRepo(t, dir)
	chdir(t, dir)

	paths := fakePaths{root: dir}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	reg := rpc.NewRegistry()
	db, err := RegisterContextAssembleHandler(reg, paths, runtime.NewSystemClock())
	if err != nil {
		t.Fatalf("RegisterContextAssembleHandler: %v", err)
	}
	if db != nil {
		t.Cleanup(func() { _ = db.Close() })
	}

	for _, method := range []string{ContextShowMethod, ContextSliceMethod} {
		t.Run(method, func(t *testing.T) {
			if !reg.Registered(method) {
				t.Fatalf("method %q is not registered", method)
			}
			res, errObj := reg.Dispatch(context.Background(), &rpc.Request{
				JSONRPC: "2.0", Method: method, ID: []byte("1"),
				Params: json.RawMessage(`{"cwd":` + jsonQuote(dir) + `}`),
			})
			if errObj != nil {
				t.Fatalf("dispatch %s returned a transport error: %+v", method, errObj)
			}
			if res == nil {
				t.Fatalf("dispatch %s returned a nil result", method)
			}
		})
	}
}

// TestRegisterContextAssembleHandler_UnwritableDataDirRefuses covers the
// failure path: the handler must refuse rather than register methods that
// would then fail on every call.
func TestRegisterContextAssembleHandler_UnwritableDataDirRefuses(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := rpc.NewRegistry()
	if _, err := RegisterContextAssembleHandler(reg, fakePaths{root: blocker}, runtime.NewSystemClock()); err == nil {
		t.Fatal("RegisterContextAssembleHandler succeeded with an unusable data dir; want a refusal")
	}
	if reg.Registered(ContextShowMethod) {
		t.Error("context.show was registered despite the failure")
	}
}

// jsonQuote renders s as a JSON string literal.
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
