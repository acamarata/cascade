package memory

// Purpose: unit tests for the memory.soul.* handler — the three methods
//   over a real FileSoulStore in t.TempDir(), the registration that makes
//   them reachable from a running daemon, the malformed-params refusals,
//   and the export canary applied to the bytes that actually cross the
//   socket.
// SPORT: G/memory-soul-store (ADD, placeholder per T-2 sport_updates).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newSoulHandler returns a handler over a real store in a fresh temp dir.
func newSoulHandler(t *testing.T) (*SoulHandler, soulFixture) {
	t.Helper()
	f := newSoulFixture(t)
	return NewSoulHandler(f.store), f
}

// TestSoulRPCRegistersEveryMethod proves the namespace is reachable from a
// router rather than merely built: a handler the composition root never
// mounts is a subsystem that ships tested and unreachable.
func TestSoulRPCRegistersEveryMethod(t *testing.T) {
	h, _ := newSoulHandler(t)
	registry := rpc.NewRegistry()
	h.Register(registry)
	for _, method := range []string{MethodSoulShow, MethodSoulEdit, MethodSoulExport} {
		if !registry.Registered(method) {
			t.Fatalf("%s is not registered", method)
		}
	}
}

// TestSoulRPCEditThenShow walks the surface a caller actually uses.
func TestSoulRPCEditThenShow(t *testing.T) {
	h, _ := newSoulHandler(t)
	edited := call[SoulEditResult](t, h.Edit, SoulEditParams{Body: "I am Ada."})
	if edited.Version != 1 {
		t.Fatalf("version = %d, want 1", edited.Version)
	}
	shown := call[SoulShowResult](t, h.Show, SoulShowParams{})
	if shown.Body != "I am Ada." {
		t.Fatalf("body = %q", shown.Body)
	}
	if shown.Schema != DefaultSoulSchema {
		t.Fatalf("schema = %q, want %q", shown.Schema, DefaultSoulSchema)
	}
	if shown.Version != 1 || shown.Diverged {
		t.Fatalf("show = %+v", shown)
	}
}

// TestSoulRPCShowReportsDivergence proves the surface tells a caller when
// the document it is handing over may not be the one on disk. Hiding that
// would let a client act on a stale model of the user.
func TestSoulRPCShowReportsDivergence(t *testing.T) {
	h, f := newSoulHandler(t)
	if _, err := f.store.Edit(context.Background(), SoulDocument{Body: "stored"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	writeFileAs(t, f.documentPath(), "edited outside")
	shown := call[SoulShowResult](t, h.Show, SoulShowParams{})
	if !shown.Diverged {
		t.Fatalf("show did not report the divergence: %+v", shown)
	}
}

// TestSoulRPCShowBeforeFirstWrite proves the not-found refusal survives
// the RPC boundary with its taxonomy kind intact.
func TestSoulRPCShowBeforeFirstWrite(t *testing.T) {
	h, _ := newSoulHandler(t)
	err := callErr(t, h.Show, SoulShowParams{})
	if !errors.Is(err, ErrNoSoulDocument) {
		t.Fatalf("show = %v, want ErrNoSoulDocument", err)
	}
	if kind, _ := cascade.KindOf(err); kind != cascade.KindNotFound {
		t.Fatalf("kind = %v, want not-found", kind)
	}
}

// TestSoulRPCEditRefusesAnEmptyDocument proves the store's own refusal is
// what governs, not the wire type: a peer cannot blank the SOUL by sending
// an empty body.
func TestSoulRPCEditRefusesAnEmptyDocument(t *testing.T) {
	h, _ := newSoulHandler(t)
	err := callErr(t, h.Edit, SoulEditParams{Body: "   "})
	if !errors.Is(err, ErrInvalidSoulDocument) {
		t.Fatalf("edit = %v, want ErrInvalidSoulDocument", err)
	}
}

// TestSoulRPCMalformedParams proves every method refuses input it cannot
// decode, with the invalid-input kind rather than a crash at the far end.
func TestSoulRPCMalformedParams(t *testing.T) {
	h, _ := newSoulHandler(t)
	methods := map[string]func(context.Context, json.RawMessage) (any, error){
		MethodSoulShow:   h.Show,
		MethodSoulEdit:   h.Edit,
		MethodSoulExport: h.Export,
	}
	for name, fn := range methods {
		if _, err := fn(context.Background(), json.RawMessage(`{"body":`)); err == nil {
			t.Fatalf("%s accepted malformed params", name)
		} else if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
			t.Fatalf("%s kind = %v, want invalid-input", name, kind)
		}
	}
	// Absent params are legal for the two methods that take none.
	if _, err := h.Show(context.Background(), nil); !errors.Is(err, ErrNoSoulDocument) {
		t.Fatalf("show with no params = %v", err)
	}
}

// TestSoulRPCExportCrossesTheWireWithNothingExtra applies the export
// canary to the bytes that actually leave the process. The handler returns
// the envelope itself, so what a caller writes to a file is what the
// export contract describes and nothing wrapped around it.
func TestSoulRPCExportCrossesTheWireWithNothingExtra(t *testing.T) {
	h, f := newSoulHandler(t)
	ctx := context.Background()

	records := NewFileStore(f.base, f.clock)
	neighbour := validEntry()
	neighbour.Body = soulCanaries[0].value
	neighbour.Description = soulCanaries[1].value
	if err := records.Write(ctx, neighbour); err != nil {
		t.Fatalf("write neighbour: %v", err)
	}
	call[SoulEditResult](t, h.Edit, SoulEditParams{Body: "I am Ada.", Schema: "mine/v1"})

	raw, err := h.Export(ctx, nil)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	export, ok := raw.(SoulExport)
	if !ok {
		t.Fatalf("export returned %T, want SoulExport", raw)
	}
	wire, err := json.Marshal(export)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, c := range soulCanaries {
		if strings.Contains(string(wire), c.value) {
			t.Fatalf("the wire payload leaked %s: %s", c.name, wire)
		}
	}
	if export.Soul.Schema != "mine/v1" || len(export.AuditEntries) != 1 {
		t.Fatalf("export lost what it was asked for: %+v", export)
	}
	// The same request twice produces the same bytes: the clock is
	// injected and nothing in the payload is machine-derived.
	second, err := h.Export(ctx, nil)
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	again, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second: %v", err)
	}
	if string(wire) != string(again) {
		t.Fatalf("export is not deterministic:\n%s\n%s", wire, again)
	}
}
