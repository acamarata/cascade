package memory

// Purpose: the SOUL store's error paths — every file-system failure comes
//   back classified rather than raw, a failed write leaves nothing behind
//   that claims a version, and the guards on the single write path itself
//   (unknown route, canceled context, empty document through any route).
// SPORT: G/memory-soul-store (ADD, placeholder per T-2 sport_updates).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestSoulStoreIOFailuresAreTyped proves no raw OS error escapes: every
// file-system failure comes back classified, so a caller can tell an
// unavailable disk from a refused document.
func TestSoulStoreIOFailuresAreTyped(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		set  func(*failingFS)
		call func(*FileSoulStore) error
	}{
		{"ledger read fails", func(f *failingFS) { f.failRead = true },
			func(s *FileSoulStore) error { _, err := s.Get(ctx); return err }},
		{"document write fails", func(f *failingFS) { f.failWrite = true },
			func(s *FileSoulStore) error { _, err := s.Edit(ctx, SoulDocument{Body: "x"}); return err }},
		{"chat write fails", func(f *failingFS) { f.failWrite = true },
			func(s *FileSoulStore) error { return s.EditViaChat(ctx, SoulDocument{Body: "x"}) }},
		{"detect read fails", func(f *failingFS) { f.failRead = true },
			func(s *FileSoulStore) error { _, err := s.DetectDivergence(ctx); return err }},
		{"export read fails", func(f *failingFS) { f.failRead = true },
			func(s *FileSoulStore) error { _, err := s.Export(ctx); return err }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sys := newFailingFS()
			c.set(sys)
			err := c.call(newSoulStoreWithFS(t.TempDir(), newTestClock(), nil, sys))
			if !errors.Is(err, ErrStoreIO) {
				t.Fatalf("error = %v, want ErrStoreIO", err)
			}
			if kind, _ := cascade.KindOf(err); kind != cascade.KindUnavailable {
				t.Fatalf("kind = %v, want unavailable", kind)
			}
		})
	}
}

// TestSoulLedgerWriteFailureLeavesNoVersionClaim proves the failure a
// half-written ledger would cause is refused rather than absorbed: the
// version the caller is told about is never one the ledger did not record.
func TestSoulLedgerWriteFailureLeavesNoVersionClaim(t *testing.T) {
	sys := newFailingFS()
	store := newSoulStoreWithFS(t.TempDir(), newTestClock(), nil, sys)
	sys.failWrite = true
	if _, err := store.Edit(context.Background(), SoulDocument{Body: "x"}); err == nil {
		t.Fatal("a failing write reported success")
	}
	if len(sys.present) != 0 {
		t.Fatalf("a failed edit left files behind: %v", sys.present)
	}
}

// TestSoulRefusesUnknownRouteAndCanceledContext covers the two guards on
// the single write path itself.
func TestSoulRefusesUnknownRouteAndCanceledContext(t *testing.T) {
	f := newSoulFixture(t)
	if _, err := f.store.applyEdit(context.Background(), SoulDocument{Body: "x"},
		SoulEditRoute("api")); !errors.Is(err, ErrInvalidSoulRoute) {
		t.Fatalf("applyEdit with an unknown route = %v, want ErrInvalidSoulRoute", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, err := range map[string]error{
		"applyEdit": func() error {
			_, e := f.store.applyEdit(ctx, SoulDocument{Body: "x"}, SoulRouteCLI)
			return e
		}(),
		"detect": func() error { _, e := f.store.DetectDivergence(ctx); return e }(),
	} {
		if kind, _ := cascade.KindOf(err); kind != cascade.KindCanceled {
			t.Fatalf("%s kind = %v, want canceled", name, kind)
		}
	}
}

// TestSoulRefusesAnEmptyDocumentThroughEveryRoute proves the empty-body
// refusal is on the shared write path rather than on one entry point, so
// no route can blank the system's model of the user.
func TestSoulRefusesAnEmptyDocumentThroughEveryRoute(t *testing.T) {
	ctx := context.Background()
	f := newSoulFixture(t)
	f.mustEdit(t, "real content")
	if _, err := f.store.Edit(ctx, SoulDocument{Body: "  "}); !errors.Is(err, ErrInvalidSoulDocument) {
		t.Fatalf("cli edit with an empty body = %v", err)
	}
	if err := f.store.EditViaChat(ctx, SoulDocument{}); !errors.Is(err, ErrInvalidSoulDocument) {
		t.Fatalf("chat edit with an empty body = %v", err)
	}
	view, err := f.store.Get(ctx)
	if err != nil || view.Body() != "real content" || view.Version != 1 {
		t.Fatalf("a refused edit changed the stored soul: %+v, %v", view, err)
	}
}

// Body is a test-local reader for the view's document text, so the
// assertions above read as one line rather than three.
func (v SoulView) Body() string { return v.Document.Body }

// TestSoulExportIsDeterministic proves identical input exports to
// identical bytes: same clock, same document, same log, same file.
func TestSoulExportIsDeterministic(t *testing.T) {
	ctx := context.Background()
	render := func() string {
		f := newSoulFixture(t)
		f.mustEdit(t, "I am Ada.\n")
		f.clock.Advance(time.Minute)
		f.mustEdit(t, "I am Ada. Still.\n")
		export, err := f.store.Export(ctx)
		if err != nil {
			t.Fatalf("export: %v", err)
		}
		data, err := json.MarshalIndent(export, "", "  ")
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(data)
	}
	first, second := render(), render()
	if first != second {
		t.Fatalf("export is not deterministic:\n%s\n---\n%s", first, second)
	}
	if strings.Contains(first, "\"exported_at\": \"0001-01-01") {
		t.Fatal("export took its instant from something other than the injected clock")
	}
}
