package memory_test

// Purpose: a caller principal reads, writes and forgets only memory records
// attributed to its own project scope; the personal kinds and unattributed
// records never reach it; the owner view is unchanged; anonymous callers of
// the scoped memory and fleet reads are refused.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

type memFixture struct {
	reg    *rpc.Registry
	store  *memory.FileStore
	base   string
	clock  *testkit.FrozenClock
	minter *principal.Minter
}

func newMemFixture(t *testing.T) *memFixture {
	t.Helper()
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	base := t.TempDir()
	store := memory.NewFileStore(base, clock)
	f := &memFixture{reg: rpc.NewRegistry(), store: store, base: base, clock: clock, minter: principal.NewMinter(clock)}
	f.reg.SetAccessPolicy(func(token string) (principal.Principal, error) { return f.minter.Resolve(token, clock.Now()) })
	memory.NewHandler(store, clock).Register(f.reg)
	return f
}

// as mints a session principal scoped to scope and returns its caller context.
func (f *memFixture) as(t *testing.T, scope string) context.Context {
	t.Helper()
	token, err := f.minter.Mint(principal.Principal{ID: "p-" + scope, Kind: principal.KindSession, SessionID: "s-" + scope, Scope: scope}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.minter.Resolve(token, f.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	return principal.WithCaller(context.Background(), principal.Caller{Principal: &p})
}

func ownerCtx() context.Context {
	return principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
}

func dispatch(ctx context.Context, reg *rpc.Registry, method string, params any) (json.RawMessage, *rpc.ErrorObject) {
	raw, _ := json.Marshal(params)
	res, eo := reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method, Params: raw})
	if eo != nil {
		return nil, eo
	}
	out, _ := json.Marshal(res)
	return out, nil
}

func (f *memFixture) ok(ctx context.Context, t *testing.T, method string, params any) json.RawMessage {
	t.Helper()
	out, eo := dispatch(ctx, f.reg, method, params)
	if eo != nil {
		t.Fatalf("%s %v: %+v", method, params, eo)
	}
	return out
}

func (f *memFixture) denied(ctx context.Context, t *testing.T, method string, params any) {
	t.Helper()
	if _, eo := dispatch(ctx, f.reg, method, params); eo == nil || eo.Code != cascade.RPCCodePermissionDenied {
		t.Fatalf("%s %v: %+v, want permission denied", method, params, eo)
	}
}

// ids returns the "<kind>/<name>" of every unit in a recall or list result.
func ids(t *testing.T, out json.RawMessage) []string {
	t.Helper()
	var res struct {
		Units      []struct{ Name, Kind string }
		Unreadable []json.RawMessage
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Unreadable) != 0 {
		t.Fatalf("principal result names unreadable records: %s", out)
	}
	got := []string{}
	for _, u := range res.Units {
		got = append(got, u.Kind+"/"+u.Name)
	}
	return got
}

func (f *memFixture) write(t *testing.T, kind memory.MemoryKind, name, scope, body string) {
	t.Helper()
	if err := f.store.Write(context.Background(), memory.MemoryEntry{Name: name, Kind: kind, Body: body, ScopeRef: scope,
		Confidence: 1, Provenance: memory.Provenance{Origin: memory.OriginSession}}); err != nil {
		t.Fatal(err)
	}
}

func (f *memFixture) fileBytes(t *testing.T, kind, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.base, kind, name+".md"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMemoryAndSessionReadsRefuseAnonymous(t *testing.T) {
	f := newMemFixture(t)
	kv := storetest.NewMemStore()
	sessions.RegisterHandlers(f.reg, sessions.New(kv, f.clock, nil))
	supervision.RegisterHandlers(f.reg, supervision.NewStore(kv, f.clock, nil, func() string { return "id-1" }, 10), nil)
	calls := []struct {
		method string
		params any
	}{
		{memory.MethodRecall, map[string]any{"query": "", "k": 5}},
		{memory.MethodList, map[string]any{}},
		{sessions.MethodList, map[string]any{}},
		{supervision.MethodList, map[string]any{"own_scope": map[string]string{"kind": "project", "id": "x"}}},
	}
	for _, c := range calls {
		if _, eo := dispatch(context.Background(), f.reg, c.method, c.params); eo == nil || eo.Code != cascade.RPCCodePermissionDenied {
			t.Errorf("%s with no caller: %+v, want permission denied", c.method, eo)
		}
		if _, eo := dispatch(ownerCtx(), f.reg, c.method, c.params); eo != nil && eo.Code == cascade.RPCCodePermissionDenied {
			t.Errorf("%s as owner refused: %+v", c.method, eo)
		}
	}
}

func TestPrincipalSeesOnlyOwnMemoryScope(t *testing.T) {
	f := newMemFixture(t)
	a, b := f.as(t, "project:alpha"), f.as(t, "project:beta")
	f.ok(a, t, memory.MethodRemember, map[string]string{"name": "a-note", "content": "shared word alpha"})
	f.ok(a, t, memory.MethodRemember, map[string]string{"name": "a-ref", "type": "reference", "content": "shared word ref"})
	f.ok(b, t, memory.MethodRemember, map[string]string{"name": "b-note", "content": "shared word beta"})
	f.ok(ownerCtx(), t, memory.MethodRemember, map[string]string{"name": "legacy", "content": "shared word legacy"})
	f.ok(ownerCtx(), t, memory.MethodRemember, map[string]string{"name": "pref", "type": "user", "content": "shared word user"})
	// Personal records attributed to alpha cannot be made through the verbs; seed them directly.
	f.write(t, memory.KindUser, "alpha-user", "project:alpha", "shared word personal")
	f.write(t, memory.KindFeedback, "alpha-fb", "project:alpha", "shared word feedback")
	if err := os.WriteFile(filepath.Join(f.base, "project", "broken.md"), []byte("not frontmatter"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(f.fileBytes(t, "project", "a-note"), []byte(`scope_ref: "project:alpha"`)) {
		t.Fatalf("remember did not attribute the record:\n%s", f.fileBytes(t, "project", "a-note"))
	}
	want := "project/a-note,reference/a-ref"
	for _, params := range []map[string]any{{"query": "shared", "k": 50}, {"query": "", "k": 50}, {"query": "", "k": 50, "scope": "project:alpha"}} {
		if got := strings.Join(ids(t, f.ok(a, t, memory.MethodRecall, params)), ","); got != want {
			t.Errorf("alpha recall %v = %s, want %s", params, got, want)
		}
	}
	if got := strings.Join(ids(t, f.ok(a, t, memory.MethodList, map[string]any{})), ","); got != want {
		t.Errorf("alpha list = %s, want %s", got, want)
	}
	if got := strings.Join(ids(t, f.ok(b, t, memory.MethodList, map[string]any{})), ","); got != "project/b-note" {
		t.Errorf("beta list = %s", got)
	}
	page := f.ok(a, t, memory.MethodList, map[string]any{"limit": 1})
	if got := strings.Join(ids(t, page), ","); got != "project/a-note" || !bytes.Contains(page, []byte(`"next_cursor":"project/a-note"`)) {
		t.Errorf("alpha page 1 = %s", page)
	}
	if got := strings.Join(ids(t, f.ok(a, t, memory.MethodList, map[string]any{"limit": 1, "cursor": "project/a-note"})), ","); got != "reference/a-ref" {
		t.Errorf("alpha page 2 = %s", got)
	}
	for _, m := range []string{memory.MethodRecall, memory.MethodList} {
		f.denied(a, t, m, map[string]any{"query": "", "k": 50, "scope": "project:beta"})
		f.denied(a, t, m, map[string]any{"query": "", "k": 50, "type": "user"})
		f.denied(a, t, m, map[string]any{"query": "", "k": 50, "type": "feedback"})
	}
}

func TestPrincipalRememberCannotRewriteLegacyRecord(t *testing.T) {
	f := newMemFixture(t)
	a, b := f.as(t, "project:alpha"), f.as(t, "project:beta")
	f.ok(ownerCtx(), t, memory.MethodRemember, map[string]string{"name": "legacy", "content": "owner body"})
	f.ok(b, t, memory.MethodRemember, map[string]string{"name": "b-note", "content": "beta body"})
	f.ok(a, t, memory.MethodRemember, map[string]string{"name": "a-note", "content": "alpha body"})
	for _, target := range []string{"legacy", "b-note"} {
		before := f.fileBytes(t, "project", target)
		f.denied(a, t, memory.MethodRemember, map[string]string{"name": target, "content": "overwrite"})
		if after := f.fileBytes(t, "project", target); !bytes.Equal(before, after) {
			t.Errorf("refused remember changed %s:\n%s\n->\n%s", target, before, after)
		}
	}
	f.denied(a, t, memory.MethodRemember, map[string]string{"name": "mine", "type": "user", "content": "personal"})
	f.ok(a, t, memory.MethodRemember, map[string]string{"name": "a-note", "content": "alpha body two"})
	f.ok(ownerCtx(), t, memory.MethodRemember, map[string]string{"name": "a-note", "content": "owner edit"})
	got := f.fileBytes(t, "project", "a-note")
	if !bytes.Contains(got, []byte(`scope_ref: "project:alpha"`)) || !bytes.Contains(got, []byte("owner edit")) {
		t.Errorf("owner rewrite changed the write-once scope:\n%s", got)
	}
}

func TestPrincipalForgetForeignRecordRefused(t *testing.T) {
	f := newMemFixture(t)
	a, b := f.as(t, "project:alpha"), f.as(t, "project:beta")
	f.ok(ownerCtx(), t, memory.MethodRemember, map[string]string{"name": "legacy", "content": "owner body"})
	f.ok(b, t, memory.MethodRemember, map[string]string{"name": "b-note", "content": "beta body"})
	f.ok(a, t, memory.MethodRemember, map[string]string{"name": "a-note", "content": "alpha body"})
	messages := map[string]bool{}
	for _, id := range []string{"project/b-note", "project/legacy", "project/absent"} {
		for _, dry := range []bool{true, false} {
			_, eo := dispatch(a, f.reg, memory.MethodForget, map[string]any{"id": id, "dry_run": dry})
			if eo == nil || eo.Code != cascade.RPCCodePermissionDenied {
				t.Fatalf("alpha forget %s (dry %v): %+v", id, dry, eo)
			}
			messages[eo.Message] = true
		}
	}
	if len(messages) != 1 {
		t.Errorf("refusals differ (existence oracle): %v", messages)
	}
	for _, name := range []string{"b-note", "legacy"} {
		f.fileBytes(t, "project", name)
		if _, err := os.Stat(filepath.Join(f.base, "project", name+".md.tombstone")); !os.IsNotExist(err) {
			t.Errorf("%s tombstoned by a refused forget (%v)", name, err)
		}
	}
	f.ok(a, t, memory.MethodForget, map[string]any{"id": "project/a-note"})
	if _, err := os.Stat(filepath.Join(f.base, "project", "a-note.md")); !os.IsNotExist(err) {
		t.Errorf("own forget left the record (%v)", err)
	}
}

// ownerFixture seeds the store the owner-view golden was captured on.
func ownerFixture(t *testing.T, f *memFixture) {
	t.Helper()
	f.write(t, memory.KindProject, "alpha", "project:alpha", "alpha note")
	f.write(t, memory.KindProject, "beta", "project:beta", "beta note")
	f.write(t, memory.KindProject, "legacy", "local", "legacy note")
	f.write(t, memory.KindReference, "ref1", "local", "reference note")
	f.write(t, memory.KindUser, "pref", "project:alpha", "user note")
	f.write(t, memory.KindFeedback, "fb", "local", "feedback note")
	if err := os.WriteFile(filepath.Join(f.base, "project", "broken.md"), []byte("not frontmatter"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryOwnerCLIUnchanged(t *testing.T) {
	f := newMemFixture(t)
	ownerFixture(t, f)
	var got []string
	for _, c := range []struct {
		method string
		params map[string]any
	}{{memory.MethodRecall, map[string]any{"query": "note", "k": 100}}, {memory.MethodRecall, map[string]any{"query": "", "k": 3}},
		{memory.MethodRecall, map[string]any{"query": "a", "k": 10, "type": "project"}}, {memory.MethodList, map[string]any{"limit": 2}},
		{memory.MethodList, map[string]any{"limit": 2, "cursor": "feedback/fb"}}, {memory.MethodList, map[string]any{"type": "user"}}} {
		got = append(got, string(f.ok(ownerCtx(), t, c.method, c.params)))
	}
	if joined := strings.Join(got, "\n"); fmt.Sprintf("%x", sha256.Sum256([]byte(joined))) != ownerGoldenSHA256 {
		t.Fatalf("owner view drifted from the baseline:\n%s", joined)
	}
}

// ownerGoldenSHA256 digests the owner view captured on ownerFixture at the
// pre-scoping baseline (the same calls run against the tree before this
// change; the full capture is kept with the ticket's evidence).
const ownerGoldenSHA256 = "aaffd6d7b8d7863e4de81bfa0649726889000a57fbd5350ddf9be2f722a853af"
