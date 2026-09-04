package memory

// Purpose: the ledger format tests — the forward-compatibility refusal
//   (a NEWER on-disk version is refused with its own sentinel, never
//   parsed loosely) and every way a damaged ledger fails to read whole.
//   Both matter for the same reason: a ledger read on a best-effort basis
//   silently resets the version counter and drops audit entries.
// SPORT: G/memory-soul-store (ADD, placeholder per T-2 sport_updates).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestSoulLedgerUnknownVersionIsADistinctRefusal proves a newer on-disk
// format is refused with its OWN sentinel rather than parsed loosely. A
// build that read a newer ledger on a best-effort basis would silently
// reset the version counter and drop entries it did not understand.
func TestSoulLedgerUnknownVersionIsADistinctRefusal(t *testing.T) {
	ctx := context.Background()
	f := newSoulFixture(t)
	f.mustEdit(t, "written by this build")

	raw, err := os.ReadFile(f.ledgerPath())
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var led map[string]any
	if err := json.Unmarshal(raw, &led); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	led["format"] = soulFormatVersion + 1
	bumped, err := json.Marshal(led)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	writeFileAs(t, f.ledgerPath(), string(bumped))

	for name, call := range map[string]func() error{
		"get":    func() error { _, err := f.store.Get(ctx); return err },
		"edit":   func() error { _, err := f.store.Edit(ctx, SoulDocument{Body: "x"}); return err },
		"chat":   func() error { return f.store.EditViaChat(ctx, SoulDocument{Body: "x"}) },
		"export": func() error { _, err := f.store.Export(ctx); return err },
		"detect": func() error { _, err := f.store.DetectDivergence(ctx); return err },
	} {
		err := call()
		if !errors.Is(err, ErrUnsupportedSoulFormat) {
			t.Fatalf("%s = %v, want ErrUnsupportedSoulFormat", name, err)
		}
		if errors.Is(err, ErrMalformedSoulLedger) {
			t.Fatalf("%s conflated a newer format with a damaged file", name)
		}
		if kind, _ := cascade.KindOf(err); kind != cascade.KindUnsupported {
			t.Fatalf("%s kind = %v, want unsupported", name, kind)
		}
	}
	// The document itself is untouched: a refusal does not destroy data.
	body, err := os.ReadFile(f.documentPath())
	if err != nil || string(body) != "written by this build" {
		t.Fatalf("document damaged by a refused read: %q, %v", body, err)
	}
}

// TestSoulLedgerDamagedFileRefusals covers every way a ledger can fail to
// read whole. Each is refused as an integrity failure, distinct from the
// forward-compatibility refusal above, and none is treated as "absent",
// which would silently restart the version counter at zero.
func TestSoulLedgerDamagedFileRefusals(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		content string
	}{
		{"truncated mid-object", `{"format":1,"version":2,"entries":[{"vers`},
		{"empty file", ""},
		{"not json at all", "version: 2\n"},
		{"no format field", `{"version":2,"entries":[]}`},
		{"unknown route in log", `{"format":1,"version":1,"entries":[` +
			`{"version":1,"route":"api","edited_at":"2020-01-01T00:00:00Z","delta_hash":"a"}]}`},
		{"versions out of order", `{"format":1,"version":2,"entries":[` +
			`{"version":2,"route":"cli","edited_at":"2020-01-01T00:00:00Z","delta_hash":"a"}]}`},
		{"more entries than versions", `{"format":1,"version":1,"entries":[` +
			`{"version":1,"route":"cli","edited_at":"2020-01-01T00:00:00Z","delta_hash":"a"},` +
			`{"version":2,"route":"cli","edited_at":"2020-01-01T00:00:00Z","delta_hash":"b"}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newSoulFixture(t)
			writeFileAs(t, f.documentPath(), "a body")
			writeFileAs(t, f.ledgerPath(), c.content)
			_, err := f.store.Get(ctx)
			if !errors.Is(err, ErrMalformedSoulLedger) {
				t.Fatalf("Get() = %v, want ErrMalformedSoulLedger", err)
			}
			if kind, _ := cascade.KindOf(err); kind != cascade.KindIntegrity {
				t.Fatalf("kind = %v, want integrity", kind)
			}
		})
	}
}
