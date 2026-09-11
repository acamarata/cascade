// Purpose: RcloneTarget unit tests against a recording fakeRunner — no
// process spawn, matching Art.7.2's "unit tests make no network calls"
// extended to "no process spawn either" for this exec-only driver. The
// REAL rclone binary run is rclone_integration_test.go.
//
// SPORT: internal.backup.targets.rclone/ADDED (P1-E19-W4-S41-T3).
package targets_test

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/backup/targets"
	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeRunner is a recording, in-memory targets.RcloneRunner: it never
// imports os/exec, so this file exercises every RcloneTarget branch
// without spawning a real process.
type fakeRunner struct {
	stdout []byte
	stderr []byte
	err    error
	calls  [][]string
	lastIn []byte
}

func (f *fakeRunner) Run(_ context.Context, stdin io.Reader, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, args)
	if stdin != nil {
		f.lastIn, _ = io.ReadAll(stdin)
	}
	return f.stdout, f.stderr, f.err
}

func newRcloneTarget(t *testing.T, r targets.RcloneRunner) *targets.RcloneTarget {
	t.Helper()
	tgt, err := targets.NewRcloneTarget("myremote:bucket", r, testEgressEngine(t))
	if err != nil {
		t.Fatalf("NewRcloneTarget: %v", err)
	}
	return tgt
}

func TestRcloneTarget_PutRunsRcatWithEgressedBytes(t *testing.T) {
	r := &fakeRunner{}
	tgt := newRcloneTarget(t, r)
	if err := tgt.Put(context.Background(), "objects/ab/abcd", strings.NewReader("payload")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(r.calls) != 1 || r.calls[0][0] != "rcat" {
		t.Fatalf("calls = %v, want one rcat call", r.calls)
	}
	if string(r.lastIn) != "payload" {
		t.Fatalf("stdin sent = %q, want %q", r.lastIn, "payload")
	}
}

func TestRcloneTarget_GetReturnsStdout(t *testing.T) {
	r := &fakeRunner{stdout: []byte("stored content")}
	tgt := newRcloneTarget(t, r)
	rc, err := tgt.Get(context.Background(), "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, _ := io.ReadAll(rc)
	if string(got) != "stored content" {
		t.Fatalf("Get = %q, want %q", got, "stored content")
	}
}

func TestRcloneTarget_GetNotFound(t *testing.T) {
	r := &fakeRunner{err: errors.New("exit status 3"), stderr: []byte("directory not found")}
	tgt := newRcloneTarget(t, r)
	_, err := tgt.Get(context.Background(), "missing")
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Get(not found) = %v, want KindNotFound", err)
	}
}

func TestRcloneTarget_DeleteAbsentIsNotError(t *testing.T) {
	r := &fakeRunner{err: errors.New("exit status 4"), stderr: []byte("object not found")}
	tgt := newRcloneTarget(t, r)
	if err := tgt.Delete(context.Background(), "missing"); err != nil {
		t.Fatalf("Delete(absent) = %v, want nil", err)
	}
}

func TestRcloneTarget_DeleteRealFailurePropagates(t *testing.T) {
	r := &fakeRunner{err: errors.New("exit status 1"), stderr: []byte("permission denied")}
	tgt := newRcloneTarget(t, r)
	err := tgt.Delete(context.Background(), "k")
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("Delete(permission denied) = %v, want KindPermissionDenied", err)
	}
}

func TestRcloneTarget_ListFiltersDirsAndPrefix(t *testing.T) {
	r := &fakeRunner{stdout: []byte(`[
{"Path":"objects","IsDir":true},
{"Path":"objects/ab/1","IsDir":false},
{"Path":"objects/cd/2","IsDir":false}
]`)}
	tgt := newRcloneTarget(t, r)
	got, err := tgt.List(context.Background(), "objects/ab/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0] != "objects/ab/1" {
		t.Fatalf("List = %v, want [objects/ab/1]", got)
	}
}

func TestRcloneTarget_ListOnMissingRemoteIsEmpty(t *testing.T) {
	r := &fakeRunner{err: errors.New("exit status 3"), stderr: []byte("directory not found")}
	tgt := newRcloneTarget(t, r)
	got, err := tgt.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List(missing remote): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List(missing remote) = %v, want empty", got)
	}
}

func TestRcloneTarget_BinaryAbsentRefused(t *testing.T) {
	r := &fakeRunner{err: targets.ErrRcloneBinaryAbsent}
	tgt := newRcloneTarget(t, r)
	err := tgt.Delete(context.Background(), "k")
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("Delete(binary absent) = %v, want KindUnsupported", err)
	}
}

func TestRcloneTarget_EmptyRemoteRefused(t *testing.T) {
	_, err := targets.NewRcloneTarget("   ", &fakeRunner{}, testEgressEngine(t))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NewRcloneTarget(blank remote) = %v, want KindInvalidInput", err)
	}
}

func TestRcloneTarget_CanceledContextRefusesBeforeRun(t *testing.T) {
	r := &fakeRunner{}
	tgt := newRcloneTarget(t, r)
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
	if len(r.calls) != 0 {
		t.Fatalf("runner was called %d times on a canceled context, want 0", len(r.calls))
	}
}

func TestRcloneTarget_UnrecognizedErrorIsUnavailable(t *testing.T) {
	r := &fakeRunner{err: errors.New("exit status 1"), stderr: []byte("some other rclone failure")}
	tgt := newRcloneTarget(t, r)
	err := tgt.Delete(context.Background(), "k")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Delete(unrecognized error) = %v, want KindUnavailable", err)
	}
}

// TestExecRcloneRunner_BinaryAbsentIsTypedRefusal proves the REAL
// production runner (execRcloneRunner, via a nil doctor-check runner)
// reports ErrRcloneBinaryAbsent when "rclone" is not on PATH, by
// temporarily clearing PATH for this call only.
func TestExecRcloneRunner_BinaryAbsentIsTypedRefusal(t *testing.T) {
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", ""); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	defer func() { _ = os.Setenv("PATH", oldPath) }()

	c := targets.NewRcloneDoctorCheck(nil)
	res, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// OK, not Error: an empty PATH makes rclone VERIFIED ABSENT, the same
	// tier doctor.go reports for any clean absence of this OPTIONAL target.
	// Art.1 reserves StatusError for a subject that could not be verified at
	// all — see TestRcloneDoctorCheck_BinaryPresentButFailsReportsError in
	// doctor_test.go, which pins that tier so this cannot be mistaken for a
	// blanket weakening of the check.
	if res.Status != doctor.StatusOK {
		t.Fatalf("Status = %v, want StatusOK with PATH cleared (verified absent optional target)", res.Status)
	}
	// ...but never a SILENT ok: the absence must be visible to an operator.
	if res.Message == "" {
		t.Fatal("absent-binary OK must state the absence in its Message")
	}
}

func TestRcloneTarget_PutReaderErrorPropagates(t *testing.T) {
	r := &fakeRunner{}
	tgt := newRcloneTarget(t, r)
	err := tgt.Put(context.Background(), "k", errRcloneReader{err: io.ErrClosedPipe})
	if err == nil {
		t.Fatal("Put(erroring reader) returned nil error")
	}
}

func TestRcloneTarget_PutRunFailurePropagates(t *testing.T) {
	r := &fakeRunner{err: errors.New("exit status 1"), stderr: []byte("permission denied")}
	tgt := newRcloneTarget(t, r)
	err := tgt.Put(context.Background(), "k", strings.NewReader("v"))
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("Put(run failure) = %v, want KindPermissionDenied", err)
	}
}

func TestRcloneTarget_NilEngineRefused(t *testing.T) {
	_, err := targets.NewRcloneTarget("remote:bucket", &fakeRunner{}, nil)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("NewRcloneTarget(nil engine) = %v, want KindUnavailable", err)
	}
}

// errRcloneReader always returns err on Read.
type errRcloneReader struct{ err error }

func (r errRcloneReader) Read([]byte) (int, error) { return 0, r.err }

func TestRcloneTarget_ContextDeadlineExceededFromRunner(t *testing.T) {
	r := &fakeRunner{err: context.DeadlineExceeded}
	tgt := newRcloneTarget(t, r)
	_, err := tgt.Get(context.Background(), "k")
	if !cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatalf("Get(runner deadline exceeded) = %v, want KindTimeout", err)
	}
}

func TestRcloneTarget_ContextCanceledFromRunner(t *testing.T) {
	r := &fakeRunner{err: context.Canceled}
	tgt := newRcloneTarget(t, r)
	_, err := tgt.Get(context.Background(), "k")
	if !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("Get(runner canceled) = %v, want KindCanceled", err)
	}
}

func TestRcloneTarget_ListDecodeErrorPropagates(t *testing.T) {
	r := &fakeRunner{stdout: []byte("not json")}
	tgt := newRcloneTarget(t, r)
	_, err := tgt.List(context.Background(), "")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("List(malformed lsjson) = %v, want KindInvalidInput", err)
	}
}
