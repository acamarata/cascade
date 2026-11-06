package jobs

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

var (
	_ func(context.Context, string, string, string, time.Time) error = (*CiAttestTrustStore)(nil).Vouch
	_ func(context.Context, string, string) (string, bool, error)    = (*CiAttestTrustStore)(nil).Lookup
)

func trustKey(b byte, n int) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, n))
}

func newTrustStore(t *testing.T) *CiAttestTrustStore {
	t.Helper()
	st, err := NewCiAttestTrustStore(newTestStore(t).db)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func wantKind(t *testing.T, err error, want cascade.Kind, what string) {
	t.Helper()
	if kind, ok := cascade.KindOf(err); !ok || kind != want {
		t.Fatalf("%s: kind %v (%v), want %v", what, kind, err, want)
	}
}

func wantLookup(t *testing.T, st *CiAttestTrustStore, node, key, pub string, found bool) {
	t.Helper()
	got, ok, err := st.Lookup(context.Background(), node, key)
	if err != nil || ok != found || got != pub {
		t.Fatalf("Lookup(%s,%s) = (%q,%v,%v), want (%q,%v,nil)", node, key, got, ok, err, pub, found)
	}
}

func TestCiAttestTrustVouchRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTrustStore(t)
	a, b := trustKey(1, 32), trustKey(2, 32)
	t1, t2 := time.Unix(1000, 0).UTC(), time.Unix(2000, 0).UTC()
	if err := st.Vouch(ctx, "n1", "k1", a, t1); err != nil {
		t.Fatal(err)
	}
	wantLookup(t, st, "n1", "k1", a, true)
	if err := st.Vouch(ctx, "n1", "k1", a, t2); err != nil {
		t.Fatal(err)
	}
	rows, err := st.List(ctx)
	if err != nil || len(rows) != 1 || !rows[0].VouchedAt.Equal(t1) {
		t.Fatalf("after repeat: %+v %v", rows, err)
	}
	wantKind(t, st.Vouch(ctx, "n1", "k1", b, t1), cascade.KindConflict, "same node other pub")
	wantKind(t, st.Vouch(ctx, "n2", "k1", b, t1), cascade.KindConflict, "other node other pub")
	wantLookup(t, st, "n1", "k1", a, true)
	if err := st.Vouch(ctx, "n2", "k1", a, t1); err != nil {
		t.Fatal(err)
	}
	rows, err = st.List(ctx)
	if err != nil || len(rows) != 2 || rows[0].NodeID != "n1" || rows[1].NodeID != "n2" ||
		rows[0].KeyID != "k1" || rows[1].KeyID != "k1" {
		t.Fatalf("list: %+v %v", rows, err)
	}
	wantLookup(t, st, "n2", "k1", a, true)
	wantLookup(t, st, "n3", "k1", "", false)
	wantLookup(t, st, "n1", "unknown", "", false)
}

func TestCiAttestTrustRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	st := newTrustStore(t)
	a := trustKey(1, 32)
	at := time.Unix(1000, 0)
	wantKind(t, st.Vouch(ctx, "", "k1", a, at), cascade.KindInvalidInput, "empty node")
	wantKind(t, st.Vouch(ctx, "n1", "", a, at), cascade.KindInvalidInput, "empty key")
	wantKind(t, st.Vouch(ctx, "n1", "k1", "not base64!", at), cascade.KindInvalidInput, "bad base64")
	wantKind(t, st.Vouch(ctx, "n1", "k1", trustKey(1, 31), at), cascade.KindInvalidInput, "31 bytes")
	wantKind(t, st.Vouch(ctx, "n1", "k1", a, time.Time{}), cascade.KindInvalidInput, "zero time")
	for _, ids := range [][2]string{{"", "k1"}, {"n1", ""}} {
		pub, ok, err := st.Lookup(ctx, ids[0], ids[1])
		wantKind(t, err, cascade.KindInvalidInput, "lookup "+ids[0]+"/"+ids[1])
		if ok || pub != "" {
			t.Fatalf("lookup %v: (%q,%v)", ids, pub, ok)
		}
	}
	if rows, err := st.List(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("list after refusals: %+v %v", rows, err)
	}
	_, err := NewCiAttestTrustStore(nil)
	wantKind(t, err, cascade.KindInvalidInput, "nil db")
}

func TestCiAttestTrustSchemaShape(t *testing.T) {
	db := newTestStore(t).db
	rows, err := db.Query(`PRAGMA table_info(` + tableCiAttestTrust + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	type col struct {
		name string
		pk   int
	}
	var got []col
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		got = append(got, col{name, pk})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []col{{"node_id", 1}, {"key_id", 2}, {"pub_key_b64", 0}, {"vouched_at", 0}}
	if len(got) != len(want) {
		t.Fatalf("columns %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("columns %+v, want %+v", got, want)
		}
	}
	steps := MigrationSet().Steps
	last := steps[len(steps)-1]
	if last.Table == nil || last.Table.Name != tableCiAttestTrust {
		t.Fatalf("last step %+v does not create %s", last, tableCiAttestTrust)
	}
}

func TestCiAttestTrustReadErrorIsNotAbsence(t *testing.T) {
	ctx := context.Background()
	db := newTestStore(t).db
	st, err := NewCiAttestTrustStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	pub, ok, err := st.Lookup(ctx, "n1", "k1")
	wantKind(t, err, cascade.KindUnavailable, "lookup on closed db")
	if ok || pub != "" {
		t.Fatalf("closed lookup: (%q,%v)", pub, ok)
	}
	_, err = st.List(ctx)
	wantKind(t, err, cascade.KindUnavailable, "list on closed db")
	if err := st.Vouch(ctx, "n1", "k1", trustKey(1, 32), time.Unix(1000, 0)); err == nil {
		t.Fatal("vouch on closed db succeeded")
	}
}
