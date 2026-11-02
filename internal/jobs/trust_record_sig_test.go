package jobs

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestTrustRecordSignatures(t *testing.T) {
	f := newRegistryFixture(t)
	id := registerFixture(t, f)
	ctx := context.Background()
	tips := registryTips(t, f.root)
	if err := f.reg.RecordTrustFetch(ctx, f.root, "origin", "url", tips); err != nil {
		t.Fatal(err)
	}
	reg, err := f.reg.Registration(ctx, f.root)
	if err != nil || len(reg.Baseline) != 2 || !reflect.DeepEqual(reg.Baseline, tips) {
		t.Fatalf("baseline %+v %v", reg, err)
	}
	fetches, err := f.reg.TrustFetches(ctx, f.root)
	if err != nil || len(fetches) != 1 || len(fetches[0].Tips) != 2 || !reflect.DeepEqual(fetches[0].Tips, tips) {
		t.Fatalf("tips %+v %v", fetches, err)
	}
	for _, msg := range f.signer.messages {
		if !strings.HasPrefix(string(msg), "cascade.trust.record/v1\x1f") {
			t.Fatal("wrong signing domain")
		}
	}
	assertIndependentTrustSignatures(t, f, id)
	t.Run("tamper", testTrustRecordTamper)
	t.Run("domains", testTrustRecordDomains)
	t.Run("unbound", testUnboundTrustRegistry)
	t.Run("signing failure", testTrustSigningFailure)
	t.Run("empty baseline registration", testEmptyBaselineSignature)
	t.Run("row replay", testTrustRecordReplay)
}

func assertIndependentTrustSignatures(t *testing.T, f registryFixture, id string) {
	t.Helper()
	var root, stamp string
	var sig []byte
	if err := f.db.QueryRow(`SELECT root,registered_at,sig FROM jobs_repo_registration WHERE repo_id=?`, id).Scan(&root, &stamp, &sig); err != nil {
		t.Fatal(err)
	}
	assertIndependentSignature(t, f, struct{ T, RepoID, Root, RegisteredAt string }{"registration", id, root, stamp}, sig)
	rows, err := f.db.Query(`SELECT ref,commit_id,sig FROM jobs_repo_baseline WHERE repo_id=? ORDER BY ref`, id)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var ref, commit string
		if err := rows.Scan(&ref, &commit, &sig); err != nil {
			t.Fatal(err)
		}
		assertIndependentSignature(t, f, struct{ T, RepoID, RegisteredAt, Ref, Commit string }{"baseline", id, stamp, ref, commit}, sig)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	rows, err = f.db.Query(`SELECT p.remote_id,p.url,p.fetched_at,t.ref,t.commit_id,t.sig FROM jobs_repo_trust_fetch p JOIN jobs_repo_trust_fetch_tip t USING(repo_id,remote_id) WHERE repo_id=?`, id)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var remote, url, at, ref, commit string
		if err := rows.Scan(&remote, &url, &at, &ref, &commit, &sig); err != nil {
			t.Fatal(err)
		}
		assertIndependentSignature(t, f, struct{ T, RepoID, RemoteID, URL, FetchedAt, Ref, Commit string }{"trust-fetch-tip", id, remote, url, at, ref, commit}, sig)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertIndependentSignature(t *testing.T, f registryFixture, value any, sig []byte) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(f.signer.PublicKey(), append([]byte("cascade.trust.record/v1\x1f"), data...), sig) {
		t.Fatal("stored signature does not verify independently")
	}
	if ed25519.Verify(f.signer.PublicKey(), data, sig) {
		t.Fatal("signature valid without domain")
	}
}

func testTrustRecordTamper(t *testing.T) {
	cases := []struct {
		table, column, value string
		fetch                bool
	}{
		{"jobs_repo_registration", "root", "edited", false},
		{"jobs_repo_registration", "registered_at", "2026-10-02T00:00:00Z", false},
		{"jobs_repo_baseline", "commit_id", strings.Repeat("b", 40), false},
		{"jobs_repo_baseline", "ref", "refs/heads/release", false},
		{"jobs_repo_trust_fetch_tip", "commit_id", strings.Repeat("b", 40), true},
		{"jobs_repo_trust_fetch_tip", "ref", "refs/heads/release", true},
		{"jobs_repo_trust_fetch", "url", "edited", true},
		{"jobs_repo_trust_fetch", "fetched_at", "2026-10-02T00:00:00Z", true},
		{"jobs_repo_trust_fetch", "url", string([]byte{0xff}), true},
	}
	for _, tc := range cases {
		t.Run(tc.table+tc.column, func(t *testing.T) {
			f := newRegistryFixture(t)
			registerFixture(t, f)
			ctx := context.Background()
			if err := f.reg.RecordTrustFetch(ctx, f.root, "origin", "url", registryTips(t, f.root)); err != nil {
				t.Fatal(err)
			}
			query := `UPDATE ` + tc.table + ` SET ` + tc.column + `=? WHERE rowid=(SELECT min(rowid) FROM ` + tc.table + `)`
			if _, err := f.db.Exec(query, tc.value); err != nil {
				t.Fatal(err)
			}
			if tc.fetch {
				_, err := f.reg.TrustFetches(ctx, f.root)
				assertRegistryKind(t, err, cascade.KindIntegrity, "jobs_repo_trust_fetch_tip")
			} else {
				_, err := f.reg.Registration(ctx, f.root)
				assertRegistryKind(t, err, cascade.KindIntegrity, tc.table)
				if _, err := f.reg.OperatorRegistered(ctx, f.root); err == nil || err == ErrRepoNotRegistered {
					t.Fatalf("forged registration accepted: %v", err)
				}
			}
		})
	}
}

func testTrustRecordDomains(t *testing.T) {
	for _, table := range []string{"jobs_repo_registration", "jobs_repo_baseline", "jobs_repo_trust_fetch_tip"} {
		for _, bad := range [][]byte{nil, {}, make([]byte, 63), make([]byte, 64)} {
			t.Run(table, func(t *testing.T) {
				f := newRegistryFixture(t)
				registerFixture(t, f)
				ctx := context.Background()
				if err := f.reg.RecordTrustFetch(ctx, f.root, "origin", "url", registryTips(t, f.root)); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.Exec(`UPDATE `+table+` SET sig=?`, bad); err != nil {
					t.Fatal(err)
				}
				if table == "jobs_repo_trust_fetch_tip" {
					_, err := f.reg.TrustFetches(ctx, f.root)
					assertRegistryKind(t, err, cascade.KindIntegrity, table)
				} else {
					_, err := f.reg.Registration(ctx, f.root)
					assertRegistryKind(t, err, cascade.KindIntegrity, table)
				}
			})
		}
	}
	t.Run("wrong prefixes", testTrustWrongPrefixes)
}

func testTrustWrongPrefixes(t *testing.T) {
	f := newRegistryFixture(t)
	id := registerFixture(t, f)
	ctx := context.Background()
	var root, stamp string
	if err := f.db.QueryRow(`SELECT root,registered_at FROM jobs_repo_registration`).Scan(&root, &stamp); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(struct{ T, RepoID, Root, RegisteredAt string }{"registration", id, root, stamp})
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"cascade.scope-envelope.v1\n", "cascade.ci.attest.put.v1\x1f", ""} {
		sig := ed25519.Sign(f.signer.key, append([]byte(prefix), data...))
		if _, err := f.db.Exec(`UPDATE jobs_repo_registration SET sig=?`, sig); err != nil {
			t.Fatal(err)
		}
		_, err := f.reg.Registration(ctx, f.root)
		assertRegistryKind(t, err, cascade.KindIntegrity, "jobs_repo_registration")
	}
	sig := ed25519.Sign(f.signer.key, append([]byte("cascade.trust.record/v1\x1f"), data...))
	if _, err := f.db.Exec(`UPDATE jobs_repo_registration SET sig=?`, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reg.OperatorRegistered(ctx, f.root); err != nil {
		t.Fatal(err)
	}
}

func testUnboundTrustRegistry(t *testing.T) {
	f := newRegistryFixture(t)
	ctx := context.Background()
	r := *f.reg
	r.signer = nil
	_, err := r.Register(ctx, f.root, "", fixedRegistryTips(nil))
	assertRegistryKind(t, err, cascade.KindPermissionDenied, "not bound")
	err = r.RecordTrustFetch(ctx, f.root, "origin", "url", registryTips(t, f.root))
	assertRegistryKind(t, err, cascade.KindPermissionDenied, "not bound")
	_, err = r.Registration(ctx, f.root)
	assertRegistryKind(t, err, cascade.KindPermissionDenied, "not bound")
	_, err = r.OperatorRegistered(ctx, f.root)
	assertRegistryKind(t, err, cascade.KindPermissionDenied, "not bound")
	_, err = r.TrustFetches(ctx, f.root)
	assertRegistryKind(t, err, cascade.KindPermissionDenied, "not bound")
	if _, err := r.WithTrustRecordSigner(nil); err == nil {
		t.Fatal("nil signer accepted")
	}
	if _, err := r.WithTrustRecordSigner(registryShortSigner{f.signer}); err == nil {
		t.Fatal("short public key accepted")
	}
	for _, table := range []string{"jobs_repo_registration", "jobs_repo_baseline", "jobs_repo_trust_fetch", "jobs_repo_trust_fetch_tip"} {
		var n int
		if err := f.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s rows %d %v", table, n, err)
		}
	}
}

func testTrustSigningFailure(t *testing.T) {
	f := newRegistryFixture(t)
	ctx := context.Background()
	failure := cascade.New(cascade.KindUnavailable, "signer unavailable")
	f.signer.err = failure
	if _, err := f.reg.Register(ctx, f.root, "", fixedRegistryTips(registryTips(t, f.root))); err != failure {
		t.Fatalf("sign error = %v", err)
	}
	f.signer.err = nil
	f.signer.wrong = true
	_, err := f.reg.Register(ctx, f.root, "", fixedRegistryTips(nil))
	assertRegistryKind(t, err, cascade.KindIntegrity, "signature")
	f.signer.wrong = false
	registerFixture(t, f)
	tips := registryTips(t, f.root)
	if err := f.reg.RecordTrustFetch(ctx, f.root, "origin", "before", tips); err != nil {
		t.Fatal(err)
	}
	f.signer.err = failure
	if err := f.reg.RecordTrustFetch(ctx, f.root, "origin", "after", tips); err != failure {
		t.Fatalf("fetch sign error = %v", err)
	}
	f.signer.err = nil
	got, err := f.reg.TrustFetches(ctx, f.root)
	if err != nil || len(got) != 1 || got[0].URL != "before" {
		t.Fatalf("failed write changed record %+v %v", got, err)
	}
}

func testEmptyBaselineSignature(t *testing.T) {
	f := newRegistryFixture(t)
	ctx := context.Background()
	id, err := f.reg.Register(ctx, f.root, "", fixedRegistryTips(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.reg.OperatorRegistered(ctx, f.root); err != nil || got != id {
		t.Fatalf("empty registration %s %v", got, err)
	}
	if _, err := f.db.Exec(`UPDATE jobs_repo_registration SET sig=NULL`); err != nil {
		t.Fatal(err)
	}
	_, err = f.reg.Registration(ctx, f.root)
	assertRegistryKind(t, err, cascade.KindIntegrity, "jobs_repo_registration")
	if err := f.reg.RecordTrustFetch(ctx, f.root, "origin", "url", registryTips(t, f.root)); err == nil {
		t.Fatal("unsigned registration can fetch")
	}
}

func testTrustRecordReplay(t *testing.T) {
	f := newRegistryFixture(t)
	id := registerFixture(t, f)
	ctx := context.Background()
	root := filepath.Join(filepath.Dir(f.root), "second")
	registryGit(t, filepath.Dir(f.root), "init", root)
	second, err := f.reg.Register(ctx, root, "", fixedRegistryTips(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO jobs_repo_baseline(repo_id,ref,commit_id,sig) SELECT ?,ref,commit_id,sig FROM jobs_repo_baseline WHERE repo_id=?`, second, id); err != nil {
		t.Fatal(err)
	}
	_, err = f.reg.Registration(ctx, root)
	assertRegistryKind(t, err, cascade.KindIntegrity, "jobs_repo_baseline")
	if err := f.reg.RecordTrustFetch(ctx, f.root, "origin", "url", registryTips(t, f.root)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO jobs_repo_trust_fetch SELECT repo_id,'mirror',url,fetched_at FROM jobs_repo_trust_fetch`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO jobs_repo_trust_fetch_tip SELECT repo_id,'mirror',ref,commit_id,sig FROM jobs_repo_trust_fetch_tip`); err != nil {
		t.Fatal(err)
	}
	_, err = f.reg.TrustFetches(ctx, f.root)
	assertRegistryKind(t, err, cascade.KindIntegrity, "jobs_repo_trust_fetch_tip")
}
