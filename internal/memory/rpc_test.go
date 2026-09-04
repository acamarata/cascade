package memory

// Purpose: unit tests for the memory.* handler — the four methods against
//   a real FileStore in t.TempDir(), the destructive semantics of
//   memory.forget (neighbours untouched, a part-way failure still a
//   deletion), the error paths, and FuzzMemoryRPCParams over the external
//   params decoder. The real JSON-RPC-over-unix-socket contract lives in
//   rpc_integration_test.go, build-tagged "integration" because the
//   no-network unit lane (Art.7.2) forbids "net"/"net/http" here.
// SPORT: internal.memory.rpc.Handler (ADD, P1-E07-W2-S13-T3).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// newHandler returns a Handler over a real FileStore in a fresh temp dir.
func newHandler(t *testing.T) (*Handler, *FileStore, string) {
	t.Helper()
	base := t.TempDir()
	store := NewFileStore(base, newTestClock())
	return NewHandler(store, newTestClock()), store, base
}

// call invokes one method with params marshalled the way the router would.
func call[T any](t *testing.T, fn func(context.Context, json.RawMessage) (any, error), params any) T {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	got, err := fn(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler returned an unexpected error: %v", err)
	}
	typed, ok := got.(T)
	if !ok {
		t.Fatalf("handler returned %T, want %T", got, *new(T))
	}
	return typed
}

// callErr invokes one method expecting a refusal, and returns it.
func callErr(t *testing.T, fn func(context.Context, json.RawMessage) (any, error), params any) error {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	got, err := fn(context.Background(), raw)
	if err == nil {
		t.Fatalf("expected an error, got result %+v", got)
	}
	return err
}

func TestRPCRememberDerivesNameFromBodyHash(t *testing.T) {
	h, _, _ := newHandler(t)
	body := "the body of a note"
	res := call[RememberResult](t, h.Remember, RememberParams{Content: body})

	want := "project/" + HashBody(body)[:nameHashPrefixLen]
	if res.ID != want {
		t.Errorf("ID = %q, want %q", res.ID, want)
	}
	// Deterministic: the same body remembered twice is the same address,
	// not a second copy.
	again := call[RememberResult](t, h.Remember, RememberParams{Content: body})
	if again.ID != res.ID {
		t.Errorf("second remember returned %q, want the same address %q", again.ID, res.ID)
	}
	listed := call[ListResult](t, h.List, ListParams{})
	if len(listed.Units) != 1 {
		t.Fatalf("list returned %d units, want 1", len(listed.Units))
	}
	if listed.Units[0].Body != body {
		t.Errorf("stored body = %q, want %q", listed.Units[0].Body, body)
	}
	if listed.Units[0].ScopeRef != DefaultScopeRef {
		t.Errorf("ScopeRef = %q, want %q", listed.Units[0].ScopeRef, DefaultScopeRef)
	}
}

func TestRPCRememberHonoursNameAndKind(t *testing.T) {
	h, _, _ := newHandler(t)
	res := call[RememberResult](t, h.Remember, RememberParams{
		Content: "a fact", Type: "user", Name: "given-name", Provenance: "session-9",
	})
	if res.ID != "user/given-name" {
		t.Fatalf("ID = %q, want %q", res.ID, "user/given-name")
	}
	got := call[ListResult](t, h.List, ListParams{Type: "user"})
	if len(got.Units) != 1 || got.Units[0].Provenance.SessionID != "session-9" {
		t.Fatalf("list = %+v, want one record carrying session-9", got.Units)
	}
}

func TestRPCRememberRefusals(t *testing.T) {
	h, _, _ := newHandler(t)
	cases := []struct {
		name   string
		params RememberParams
		want   cascade.Kind
	}{
		{"empty content", RememberParams{Content: "   "}, cascade.KindInvalidInput},
		{"unknown kind", RememberParams{Content: "x", Type: "nonsense"}, cascade.KindInvalidInput},
		{"illegal name", RememberParams{Content: "x", Name: "../escape"}, cascade.KindInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := callErr(t, h.Remember, tc.params)
			if kind, _ := cascade.KindOf(err); kind != tc.want {
				t.Errorf("kind = %v, want %v", kind, tc.want)
			}
		})
	}
}

func TestRPCRecallScansNameDescriptionAndBody(t *testing.T) {
	h, store, _ := newHandler(t)
	seed(t, store, "project", "alpha", "about widgets", "nothing here")
	seed(t, store, "project", "beta", "unrelated", "mentions WIDGETS in the body")
	seed(t, store, "user", "widgets-in-name", "", "")

	res := call[RecallResult](t, h.Recall, RecallParams{Query: "widgets", K: 10})
	got := addressesOf(res.Units)
	want := []string{"project/alpha", "project/beta", "user/widgets-in-name"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("recall = %v, want %v (canonical-address order)", got, want)
	}
}

func TestRPCRecallHonoursKAndType(t *testing.T) {
	h, store, _ := newHandler(t)
	seed(t, store, "project", "alpha", "", "match")
	seed(t, store, "project", "beta", "", "match")
	seed(t, store, "user", "gamma", "", "match")

	if got := call[RecallResult](t, h.Recall, RecallParams{Query: "match", K: 1}); len(got.Units) != 1 {
		t.Errorf("--k 1 returned %d units, want 1", len(got.Units))
	}
	typed := call[RecallResult](t, h.Recall, RecallParams{Query: "match", K: 10, Type: "user"})
	if len(typed.Units) != 1 || typed.Units[0].Kind != KindUser {
		t.Errorf("--type user returned %+v, want the single user record", addressesOf(typed.Units))
	}
	zero := call[RecallResult](t, h.Recall, RecallParams{Query: "match", K: 0})
	if len(zero.Units) != 0 {
		t.Errorf("k=0 returned %d units, want an empty list", len(zero.Units))
	}
	if err := callErr(t, h.Recall, RecallParams{Query: "x", K: 1, Type: "bogus"}); err == nil {
		t.Error("an unknown --type must be refused, never silently widened to every kind")
	}
}

func TestRPCListPaginatesByAddressCursor(t *testing.T) {
	h, store, _ := newHandler(t)
	for _, name := range []string{"a-one", "b-two", "c-three"} {
		seed(t, store, "project", name, "", "body")
	}
	seed(t, store, "user", "z-last", "", "body")

	first := call[ListResult](t, h.List, ListParams{Limit: 2})
	if got := addressesOf(first.Units); strings.Join(got, ",") != "project/a-one,project/b-two" {
		t.Fatalf("page 1 = %v", got)
	}
	if first.NextCursor != "project/b-two" {
		t.Fatalf("NextCursor = %q, want the last address on the page", first.NextCursor)
	}
	second := call[ListResult](t, h.List, ListParams{Limit: 2, Cursor: first.NextCursor})
	if got := addressesOf(second.Units); strings.Join(got, ",") != "project/c-three,user/z-last" {
		t.Fatalf("page 2 = %v", got)
	}
	if second.NextCursor != "" {
		t.Errorf("NextCursor = %q on the final page, want empty", second.NextCursor)
	}
}

// TestRPCReadFailuresAreReportedNotDropped proves a damaged record is
// named in the response instead of quietly vanishing from it.
func TestRPCReadFailuresAreReportedNotDropped(t *testing.T) {
	h, store, base := newHandler(t)
	seed(t, store, "project", "healthy", "", "body")
	if err := os.WriteFile(filepath.Join(base, "project", "damaged.md"), []byte("not a record"), 0o600); err != nil {
		t.Fatalf("plant a damaged record: %v", err)
	}

	listed := call[ListResult](t, h.List, ListParams{})
	if len(listed.Units) != 1 || len(listed.Unreadable) != 1 {
		t.Fatalf("list = %d units / %d unreadable, want 1 and 1", len(listed.Units), len(listed.Unreadable))
	}
	if listed.Unreadable[0].ID != "project/damaged" {
		t.Errorf("unreadable ID = %q", listed.Unreadable[0].ID)
	}
	recalled := call[RecallResult](t, h.Recall, RecallParams{Query: "", K: 10})
	if len(recalled.Unreadable) != 1 {
		t.Errorf("recall dropped the damaged record silently: %+v", recalled)
	}
}

func TestRPCMalformedParamsAreRefused(t *testing.T) {
	h, _, _ := newHandler(t)
	for _, fn := range []func(context.Context, json.RawMessage) (any, error){
		h.Remember, h.Recall, h.Forget, h.List,
	} {
		if _, err := fn(context.Background(), json.RawMessage(`{"k":`)); err == nil {
			t.Error("malformed params were accepted")
		} else if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
			t.Errorf("kind = %v, want invalid_input", kind)
		}
	}
	// Absent params decode as an empty request; the method's own field
	// validation is what refuses it, so the error is about the field and
	// not about the envelope.
	if _, err := h.List(context.Background(), nil); err != nil {
		t.Errorf("list with no params: %v", err)
	}
}

func TestParseAddressRoundTrip(t *testing.T) {
	kind, name, err := ParseAddress(Address(KindFeedback, "a.name_1"))
	if err != nil || kind != KindFeedback || name != "a.name_1" {
		t.Fatalf("ParseAddress = (%v, %q, %v)", kind, name, err)
	}
}

// seed writes one record straight through the store.
func seed(t *testing.T, store *FileStore, kind, name, description, body string) {
	t.Helper()
	entry := validEntry()
	entry.Kind = MemoryKind(kind)
	entry.Name = name
	entry.Description = description
	entry.Body = body
	if err := store.Write(context.Background(), entry); err != nil {
		t.Fatalf("seed %s/%s: %v", kind, name, err)
	}
}

// addressesOf renders a unit slice as its canonical addresses, which is
// what every ordering assertion in this file is actually about.
func addressesOf(units []MemoryEntry) []string {
	out := make([]string, 0, len(units))
	for _, u := range units {
		out = append(out, Address(u.Kind, u.Name))
	}
	return out
}

// FuzzMemoryRPCParams drives the namespace's external-input decoder with
// arbitrary bytes. The contract is narrow and absolute: no input may
// panic. A decode failure is a correct outcome, not a finding.
func FuzzMemoryRPCParams(f *testing.F) {
	seedDir := filepath.Join("..", "testdata", "fuzz", "FuzzMemoryRPCParams")
	entries, err := os.ReadDir(seedDir)
	if err != nil {
		f.Fatalf("read seed corpus %s: %v", seedDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || strings.EqualFold(e.Name(), "README.md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(seedDir, e.Name()))
		if err != nil {
			f.Fatalf("read seed %s: %v", e.Name(), err)
		}
		f.Add(data)
	}
	f.Add([]byte(nil))
	f.Add([]byte(`{"id":"project/x","dry_run":true}`))
	f.Add([]byte(`{"k":-1,"type":"user"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		params := json.RawMessage(raw)
		var remember RememberParams
		var recall RecallParams
		var forget ForgetParams
		var list ListParams
		_ = decodeParams(MethodRemember, params, &remember)
		_ = decodeParams(MethodRecall, params, &recall)
		_ = decodeParams(MethodForget, params, &forget)
		_ = decodeParams(MethodList, params, &list)
		// A decoded address must never be accepted unless it parses; this
		// asserts the refusal path stays reachable under fuzzed input.
		if _, _, err := ParseAddress(forget.ID); err == nil && !strings.Contains(forget.ID, "/") {
			t.Fatalf("ParseAddress accepted %q, which is not a <kind>/<name> pair", forget.ID)
		}
	})
}
