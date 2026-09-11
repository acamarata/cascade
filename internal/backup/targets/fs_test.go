// Purpose: FSTarget's conformance and error-path tests, run entirely
// against a real filesystem under t.TempDir() (Art.7.1) — never an
// in-memory fake, since fs.go's own atomic-rename and path-escape logic
// is exactly what a fake would paper over.
//
// SPORT: internal.backup.targets.fs/ADDED (P1-E19-W4-S41-T3).
package targets_test

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/backup/targets"
	"github.com/acamarata/cascade/pkg/cascade"
)

func newFSTarget(t *testing.T) *targets.FSTarget {
	t.Helper()
	tgt, err := targets.NewFSTarget(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSTarget: %v", err)
	}
	return tgt
}

func TestFSTarget_PutGetRoundTrip(t *testing.T) {
	tgt := newFSTarget(t)
	ctx := context.Background()
	want := []byte("hello backup target")
	if err := tgt.Put(ctx, "objects/ab/abcd", strings.NewReader(string(want))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, err := tgt.Get(ctx, "objects/ab/abcd")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	// Bytes actually landed and read back equal — not merely err == nil.
	if string(got) != string(want) {
		t.Fatalf("round trip = %q, want %q", got, want)
	}
}

func TestFSTarget_PutOverwrites(t *testing.T) {
	tgt := newFSTarget(t)
	ctx := context.Background()
	if err := tgt.Put(ctx, "k", strings.NewReader("first")); err != nil {
		t.Fatalf("Put(first): %v", err)
	}
	if err := tgt.Put(ctx, "k", strings.NewReader("second")); err != nil {
		t.Fatalf("Put(second): %v", err)
	}
	rc, err := tgt.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, _ := io.ReadAll(rc)
	if string(got) != "second" {
		t.Fatalf("Get after overwrite = %q, want %q", got, "second")
	}
}

func TestFSTarget_GetMissingIsNotFound(t *testing.T) {
	tgt := newFSTarget(t)
	_, err := tgt.Get(context.Background(), "no/such/key")
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Get(missing) = %v, want KindNotFound", err)
	}
}

func TestFSTarget_DeleteAbsentIsNotError(t *testing.T) {
	tgt := newFSTarget(t)
	if err := tgt.Delete(context.Background(), "never/written"); err != nil {
		t.Fatalf("Delete(absent) = %v, want nil", err)
	}
}

func TestFSTarget_DeleteThenGetIsNotFound(t *testing.T) {
	tgt := newFSTarget(t)
	ctx := context.Background()
	if err := tgt.Put(ctx, "k", strings.NewReader("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := tgt.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := tgt.Get(ctx, "k"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Get after delete = %v, want KindNotFound", err)
	}
}

func TestFSTarget_ListByPrefixSortedAndFiltered(t *testing.T) {
	tgt := newFSTarget(t)
	ctx := context.Background()
	for _, k := range []string{"objects/ab/1", "objects/ab/2", "objects/cd/3", "manifests/m1"} {
		if err := tgt.Put(ctx, k, strings.NewReader("v")); err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
	}
	got, err := tgt.List(ctx, "objects/ab/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"objects/ab/1", "objects/ab/2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("List(objects/ab/) = %v, want %v", got, want)
	}
}

func TestFSTarget_ListEmptyRootIsEmptyNotError(t *testing.T) {
	tgt := newFSTarget(t)
	got, err := tgt.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List(empty root): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List(empty root) = %v, want empty", got)
	}
}

func TestFSTarget_KeyEscapingRootRefused(t *testing.T) {
	tgt := newFSTarget(t)
	ctx := context.Background()
	if err := tgt.Put(ctx, "../escape", strings.NewReader("v")); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Put(escaping key) = %v, want KindInvalidInput", err)
	}
	if _, err := tgt.Get(ctx, "../escape"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Get(escaping key) = %v, want KindInvalidInput", err)
	}
}

func TestFSTarget_EmptyKeyRefused(t *testing.T) {
	tgt := newFSTarget(t)
	if err := tgt.Put(context.Background(), "", strings.NewReader("v")); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Put(empty key) = %v, want KindInvalidInput", err)
	}
}

func TestFSTarget_EmptyRootRefused(t *testing.T) {
	_, err := targets.NewFSTarget("   ")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NewFSTarget(blank root) = %v, want KindInvalidInput", err)
	}
}

func TestFSTarget_CanceledContextRefusesBeforeIO(t *testing.T) {
	tgt := newFSTarget(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tgt.Put(ctx, "k", strings.NewReader("v")); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Put(canceled) = %v, want KindCanceled", err)
	}
	if _, err := tgt.Get(ctx, "k"); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Get(canceled) = %v, want KindCanceled", err)
	}
	if err := tgt.Delete(ctx, "k"); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Delete(canceled) = %v, want KindCanceled", err)
	}
	if _, err := tgt.List(ctx, ""); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("List(canceled) = %v, want KindCanceled", err)
	}
}

// errReader always returns err on Read.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestFSTarget_PutReaderErrorPropagates(t *testing.T) {
	tgt := newFSTarget(t)
	err := tgt.Put(context.Background(), "k", errReader{err: io.ErrClosedPipe})
	if err == nil {
		t.Fatal("Put(erroring reader) returned nil error")
	}
}

func TestFSTarget_PutFailsWhenTargetPathIsExistingDirectory(t *testing.T) {
	tgt := newFSTarget(t)
	ctx := context.Background()
	// "collision/child" makes "collision" exist as a real, non-empty
	// directory; renaming a temp file onto that path must fail.
	if err := tgt.Put(ctx, "collision/child", strings.NewReader("v")); err != nil {
		t.Fatalf("Put(collision/child): %v", err)
	}
	if err := tgt.Put(ctx, "collision", strings.NewReader("v")); err == nil {
		t.Fatal("Put(key colliding with an existing directory) returned nil error")
	}
}

func TestFSTarget_ListOnDeletedRootIsEmpty(t *testing.T) {
	dir := t.TempDir()
	tgt, err := targets.NewFSTarget(dir)
	if err != nil {
		t.Fatalf("NewFSTarget: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	got, err := tgt.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List(deleted root): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List(deleted root) = %v, want empty", got)
	}
}

func TestFSTarget_DeleteEscapingKeyRefused(t *testing.T) {
	tgt := newFSTarget(t)
	if err := tgt.Delete(context.Background(), "../escape"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Delete(escaping key) = %v, want KindInvalidInput", err)
	}
}

func TestFSTarget_DeadlineExceededRefusesBeforeIO(t *testing.T) {
	tgt := newFSTarget(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)
	if err := tgt.Put(ctx, "k", strings.NewReader("v")); !cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatalf("Put(deadline exceeded) = %v, want KindTimeout", err)
	}
}

func TestFSTarget_NewFSTargetFailsWhenRootPathIsAFile(t *testing.T) {
	base := t.TempDir()
	filePath := base + "/not-a-dir"
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := targets.NewFSTarget(filePath + "/nested")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("NewFSTarget(root under a file) = %v, want KindUnavailable", err)
	}
}

func TestFSTarget_DotKeyResolvesToRootItself(t *testing.T) {
	tgt := newFSTarget(t)
	// "." resolves to the root itself (full == t.root), the one resolve
	// branch that skips the escape-prefix check entirely.
	if err := tgt.Delete(context.Background(), "."); err != nil {
		t.Fatalf("Delete(.) = %v, want nil (deleting the now-empty root)", err)
	}
}
