package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// The lifecycle side of the projection: retiring, withdrawing, rebuilding,
// the layout stamp, and the failure paths. The fixture, the real SQLite
// database and the test-only embedder all live in db_projection_test.go;
// this file is split from it only to keep every file inside the 300-line
// cap.

// dumpProjection returns every projection key and value, in key order. Two
// dumps compare byte for byte, which is how the determinism and rebuild
// assertions below are made without trusting any single field.
func dumpProjection(t *testing.T, f *projectionFixture) string {
	t.Helper()
	ctx := context.Background()
	keys, err := scanKeys(ctx, f.kv, projectionPrefix)
	if err != nil {
		t.Fatalf("scanning the projection: %v", err)
	}
	var b strings.Builder
	for _, k := range keys {
		val, gerr := f.kv.Get(ctx, projectionNamespace, k)
		if gerr != nil {
			t.Fatalf("reading %s: %v", k, gerr)
		}
		b.WriteString(k + "=" + string(val) + "\n")
	}
	return b.String()
}

func TestRun_TombstoneRetiresTheRow(t *testing.T) {
	f := newProjection(t)
	writeEntry(t, f, "alpha", "body pelicans\n", "first")
	mustRun(t, f)
	before := mustVectorCount(t, f)

	if err := f.files.Delete(context.Background(), KindProject, "alpha"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	res := mustRun(t, f)
	if res.Retired != 1 {
		t.Fatalf("run after a delete = %+v, want 1 retired", res)
	}
	row := mustRow(t, f, "project/alpha")
	if !row.Deleted {
		t.Fatal("the row of a tombstoned record is not marked deleted")
	}
	assertHits(t, f, "pelicans")
	if after := mustVectorCount(t, f); after != before-1 {
		t.Fatalf("vector count %d after retiring, want %d", after, before-1)
	}
}

func TestRun_FileRemovedWithoutTombstoneRetiresTheRow(t *testing.T) {
	f := newProjection(t)
	writeEntry(t, f, "alpha", "body pelicans\n", "first")
	mustRun(t, f)

	if err := os.Remove(filepath.Join(f.base, "project", "alpha.md")); err != nil {
		t.Fatalf("removing the record file: %v", err)
	}
	res := mustRun(t, f)
	if res.Retired != 1 {
		t.Fatalf("run after an out-of-store removal = %+v, want 1 retired", res)
	}
	assertHits(t, f, "pelicans")
}

func TestRun_UnsupportedVersionIsNeverIndexed(t *testing.T) {
	f := newProjection(t)
	writeEntry(t, f, "alpha", "body pelicans\n", "first")
	mustRun(t, f)
	assertHits(t, f, "pelicans", "project/alpha")

	// Rewrite the same record as a format version this build does not know.
	path := filepath.Join(f.base, "project", "alpha.md")
	data, err := os.ReadFile(path) //nolint:gosec // a path this test itself built under t.TempDir
	if err != nil {
		t.Fatalf("reading the record: %v", err)
	}
	bumped := strings.Replace(string(data), "format: 1", "format: 999", 1)
	if werr := os.WriteFile(path, []byte(bumped), 0o600); werr != nil {
		t.Fatalf("rewriting the record: %v", werr)
	}
	if _, rerr := f.files.Read(context.Background(), KindProject, "alpha"); !cascade.HasKind(rerr, cascade.KindUnsupported) {
		t.Fatalf("the file store did not refuse the bumped record: %v", rerr)
	}

	res := mustRun(t, f)
	if res.Failed != 1 || len(res.Failures) != 1 || res.Failures[0].ID != "project/alpha" {
		t.Fatalf("run over an unreadable record = %+v, want one reported failure", res)
	}
	assertHits(t, f, "pelicans")
	if _, found, rerr := readRow(context.Background(), f.kv, "project/alpha"); rerr != nil || found {
		t.Fatalf("the withdrawn record still has a row (found=%v, err=%v)", found, rerr)
	}
	if n := mustVectorCount(t, f); n != 0 {
		t.Fatalf("the withdrawn record still has %d vectors", n)
	}
}

func TestRun_OneDamagedFileDoesNotFailTheRun(t *testing.T) {
	f := newProjection(t)
	writeEntry(t, f, "alpha", "good body pelicans\n", "first")
	damaged := filepath.Join(f.base, "project", "damaged.md")
	if err := os.WriteFile(damaged, []byte("this is not a memory record"), 0o600); err != nil {
		t.Fatalf("writing the damaged file: %v", err)
	}

	res := mustRun(t, f)
	if res.Scanned != 2 || res.Upserted != 1 || res.Failed != 1 {
		t.Fatalf("run = %+v, want 2 scanned, 1 upserted, 1 failed", res)
	}
	if res.Failures[0].ID != "project/damaged" {
		t.Fatalf("failure names %q, want project/damaged", res.Failures[0].ID)
	}
	assertHits(t, f, "pelicans", "project/alpha")
}

func TestRebuild_IsIdempotentAndDeterministic(t *testing.T) {
	f := newProjection(t)
	writeEntry(t, f, "alpha", "body pelicans\n", "first")
	writeEntry(t, f, "beta", "body cormorants\n", "second")

	if _, err := f.job.Rebuild(context.Background()); err != nil {
		t.Fatalf("first Rebuild: %v", err)
	}
	first := dumpProjection(t, f)
	if _, err := f.job.Rebuild(context.Background()); err != nil {
		t.Fatalf("second Rebuild: %v", err)
	}
	if second := dumpProjection(t, f); second != first {
		t.Fatalf("two rebuilds over the same files differ:\n%s\n%s", first, second)
	}
}

func TestRebuild_RecoversFromADeletedProjection(t *testing.T) {
	f := newProjection(t)
	writeEntry(t, f, "alpha", "body pelicans\n", "first")
	writeEntry(t, f, "beta", "body cormorants\n", "second")
	mustRun(t, f)
	want := dumpProjection(t, f)

	ctx := context.Background()
	keys, err := scanKeys(ctx, f.kv, projectionPrefix)
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	for _, k := range keys {
		if derr := f.kv.Delete(ctx, projectionNamespace, k); derr != nil {
			t.Fatalf("deleting %s: %v", k, derr)
		}
	}
	if dumpProjection(t, f) != "" {
		t.Fatal("the projection was not emptied")
	}
	if _, rerr := f.job.Rebuild(ctx); rerr != nil {
		t.Fatalf("Rebuild: %v", rerr)
	}
	if got := dumpProjection(t, f); got != want {
		t.Fatalf("rebuild from the files alone differs:\nwant %s\ngot  %s", want, got)
	}
}

func TestRun_CorruptRowIsRewrittenFromTheFile(t *testing.T) {
	f := newProjection(t)
	writeEntry(t, f, "alpha", "body pelicans\n", "first")
	mustRun(t, f)
	want := dumpProjection(t, f)

	ctx := context.Background()
	if err := f.kv.Put(ctx, projectionNamespace, recordKey("project/alpha"), []byte("{corrupt")); err != nil {
		t.Fatalf("corrupting the row: %v", err)
	}
	res := mustRun(t, f)
	if res.Upserted != 1 {
		t.Fatalf("run over a corrupt row = %+v, want 1 upserted", res)
	}
	if got := dumpProjection(t, f); got != want {
		t.Fatalf("the corrupt row was not restored from the file:\nwant %s\ngot  %s", want, got)
	}
}

func TestRun_VersionMismatchForcesARebuild(t *testing.T) {
	f := newProjection(t)
	writeEntry(t, f, "alpha", "body pelicans\n", "first")
	mustRun(t, f)

	ctx := context.Background()
	if err := f.kv.Put(ctx, projectionNamespace, metaVersionKey, []byte("999")); err != nil {
		t.Fatalf("stamping another version: %v", err)
	}
	res := mustRun(t, f)
	if !res.Rebuilt || res.Upserted != 1 {
		t.Fatalf("run after a version mismatch = %+v, want a rebuild of 1 record", res)
	}
	if v := mustVersion(t, f); v != ProjectionVersion {
		t.Fatalf("stored version = %d, want %d", v, ProjectionVersion)
	}
}

func TestRun_UnreadableVersionStampRebuilds(t *testing.T) {
	f := newProjection(t)
	writeEntry(t, f, "alpha", "body pelicans\n", "first")
	ctx := context.Background()
	if err := f.kv.Put(ctx, projectionNamespace, metaVersionKey, []byte("not a number")); err != nil {
		t.Fatalf("stamping: %v", err)
	}
	if res := mustRun(t, f); !res.Rebuilt {
		t.Fatalf("run over an unreadable stamp = %+v, want a rebuild", res)
	}
}

func TestRun_RetiredRecordComesBack(t *testing.T) {
	f := newProjection(t)
	writeEntry(t, f, "alpha", "body pelicans\n", "first")
	mustRun(t, f)
	if err := f.files.Delete(context.Background(), KindProject, "alpha"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	mustRun(t, f)

	writeEntry(t, f, "alpha", "body pelicans\n", "first")
	res := mustRun(t, f)
	if res.Upserted != 1 || res.Embedded != 1 {
		t.Fatalf("run after a record returned = %+v, want 1 upserted and 1 embedded", res)
	}
	if row := mustRow(t, f, "project/alpha"); row.Deleted {
		t.Fatal("the returned record is still marked deleted")
	}
	assertHits(t, f, "pelicans", "project/alpha")
}

func TestRun_StoreFailurePropagates(t *testing.T) {
	files, _, _ := newStore(t)
	e := validEntry()
	if err := files.Write(context.Background(), e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	job := NewProjectionJob(files, failingStore{openTestDB(t)}, nil, nil, files.clock)
	_, err := job.Run(context.Background())
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Run over a failing store returned %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), "memory store I/O failure") {
		t.Fatalf("Run error %v does not carry the store I/O sentinel", err)
	}
}

type failingStore struct{ provider.Store }

func TestRun_ScanFailurePropagates(t *testing.T) {
	files, _, _ := newStore(t)
	if err := files.Write(context.Background(), validEntry()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	job := NewProjectionJob(files, unscannableStore{openTestDB(t)}, nil, nil, files.clock)
	ctx := context.Background()
	if _, err := job.Run(ctx); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Run over an unscannable store returned %v, want KindUnavailable", err)
	}
	if _, err := job.Search(ctx, "anything", 0); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Search over an unscannable store returned %v, want KindUnavailable", err)
	}
}

type unscannableStore struct{ provider.Store }
