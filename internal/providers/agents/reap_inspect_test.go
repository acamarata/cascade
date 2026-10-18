package agents

// Purpose: how the sweep treats a process it cannot inspect. A process that is
// gone, a zombie or denied shows no environment (the denied case is the
// documented residual); any other inspection failure fails the sweep closed
// instead of reporting a clean re-list it did not see.
//
// Constraints: a fake process table and error values, plus the test-only
// reapchild for the platform source; HOME is a fresh temp dir.

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestNoEntriesClassifiesInspectionErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"gone":        {fs.ErrNotExist, true},
		"no such pid": {syscall.ESRCH, true},
		"denied":      {fs.ErrPermission, true},
		"zombie":      {syscall.EINVAL, true},
		"io failure":  {syscall.EIO, false},
		"other":       {errors.New("table corrupt"), false},
	} {
		if got := noEntries(tc.err); got != tc.want {
			t.Errorf("%s: noEntries(%v) = %v, want %v", name, tc.err, got, tc.want)
		}
	}
}

func TestLeaseSweepFailsClosedOnUninspectableProcess(t *testing.T) {
	src := &fakeSrc{
		env:     map[int]string{4001: fakeEntry, 4002: "PATH=/usr/bin"},
		inspect: map[int]error{4002: syscall.EIO},
	}
	err := fastSweeper(src).sweep(context.Background(), fakeMarker)
	if kind, _ := cascade.KindOf(err); err == nil || kind != cascade.KindUnavailable {
		t.Fatalf("sweep with a persistently uninspectable process = %v, want KindUnavailable", err)
	}
	if err == ErrLeaseSweepIncomplete {
		t.Fatal("an inspection failure must not masquerade as the survivor sentinel")
	}
	if !slices.Equal(src.killed, []int{4001}) {
		t.Fatalf("killed = %v, want the marked pid signalled and the unreadable one left alone", src.killed)
	}
}

func TestLeaseSweepRetriesATransientInspectionError(t *testing.T) {
	src := &fakeSrc{
		env:        map[int]string{4001: fakeEntry, 4002: "PATH=/usr/bin"},
		inspect:    map[int]error{4002: syscall.EIO},
		clearAfter: 1,
	}
	if err := fastSweeper(src).sweep(context.Background(), fakeMarker); err != nil {
		t.Fatalf("sweep = %v, want nil once the transient error cleared", err)
	}
	if !slices.Equal(src.killed, []int{4001}) {
		t.Fatalf("killed = %v, want only the marked pid", src.killed)
	}
}

// A process can exec with a huge number of empty arguments; the parse must
// stay linear in the buffer, not rescan it once per possible padding length.
func TestProcArgsHasStaysLinearOnManyEmptyArguments(t *testing.T) {
	const n = 20000
	args := make([]string, n)
	buf := procArgs(n, "/bin/tool", args, []string{"A=1", leaseEntry(fakeMarker)})
	start := time.Now()
	if !procArgsHas(buf, leaseEntry(fakeMarker)) {
		t.Fatal("the marker behind 20000 empty arguments was not found")
	}
	if procArgsHas(procArgs(n, "/bin/tool", args, []string{"A=1"}), leaseEntry(fakeMarker)) {
		t.Fatal("a buffer without the marker matched")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("parsing two %d-argument buffers took %v, want linear time", n, d)
	}
}

func TestProcArgsHasWithoutArgumentsAndNeverMatchesAnArgument(t *testing.T) {
	entry := leaseEntry(fakeMarker)
	if !procArgsHas(procArgs(0, "/bin/tool", nil, []string{entry}), entry) {
		t.Error("argc 0: the environment entry was not found")
	}
	// An argument that is byte-for-byte the whole entry is not an environment
	// entry, whatever the path length (and so the padding).
	for _, path := range []string{"/bin/t", "/bin/to", "/bin/tool", "/usr/bin/grep", "/usr/bin/tool-x", "/usr/local/bin/tool-x"} {
		if procArgsHas(procArgs(2, path, []string{"tool", entry}, []string{"A=1"}), entry) {
			t.Errorf("path %q: an argument matched as an environment entry", path)
		}
	}
	// A layout with less padding than the kernel's (not the measured one) stays
	// permissive: an evader behind an empty argv[0] is never missed.
	short := append([]byte{1, 0, 0, 0}, "/b\x00\x00"...) // 1 NUL of padding where 8-byte alignment gives 5
	short = append(short, entry+"\x00"...)
	if !procArgsHas(short, entry) {
		t.Error("unknown layout: the entry was missed")
	}
}

func TestLeaseSweepSurvivorWinsOverAnUninspectableProcess(t *testing.T) {
	src := &fakeSrc{
		env:      map[int]string{4001: fakeEntry, 4002: "PATH=/usr/bin"},
		immortal: map[int]bool{4001: true},
		inspect:  map[int]error{4002: syscall.EIO},
	}
	requireSentinel(t, fastSweeper(src).sweep(context.Background(), fakeMarker), ErrLeaseSweepIncomplete)
}

func TestProcArgsHasAfterEightEmptyArgumentsOnAnAlignedPath(t *testing.T) {
	entry := leaseEntry(fakeMarker)
	args := make([]string, 8) // "/bin/to" + NUL is 8 bytes: no padding, so all 8 NULs are empty arguments
	if !procArgsHas(procArgs(8, "/bin/to", args, []string{entry}), entry) {
		t.Fatal("the marker behind eight empty arguments on a pad-0 path was missed")
	}
}

func TestMarkedStopsOnACanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := fastSweeper(&fakeSrc{env: map[int]string{4001: fakeEntry}}).marked(ctx, fakeEntry)
	if kind, _ := cascade.KindOf(err); kind != cascade.KindCanceled {
		t.Fatalf("marked on a canceled context = %v, want KindCanceled", err)
	}
}

// On the real process table: an argument that is the whole entry is not an
// environment entry, and an empty argv[0] does not hide one that is. This also
// catches a kernel that lays out the argument block differently.
func TestPlatformHasEntryOnRealArgumentLayouts(t *testing.T) {
	bin := reapChildBin(t)
	src, err := platformSource()
	if err != nil {
		t.Fatalf("platformSource: %v", err)
	}
	marker, _ := NewLeaseMarker()
	dir := t.TempDir()

	startChild(t, bin, "", "sleep", filepath.Join(dir, "arg.pid"), leaseEntry(marker))
	argPid := readPid(t, filepath.Join(dir, "arg.pid"))
	if has, err := src.hasEntry(argPid, leaseEntry(marker)); err != nil || has {
		t.Errorf("entry only as an argument: hasEntry = %v, %v; want false", has, err)
	}

	cmd := exec.Command(bin, "sleep", filepath.Join(dir, "empty.pid"))
	cmd.Args[0] = ""
	cmd.Env = childEnv(t, marker)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start with an empty argv[0]: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	emptyPid := readPid(t, filepath.Join(dir, "empty.pid"))
	if has, err := src.hasEntry(emptyPid, leaseEntry(marker)); err != nil || !has {
		t.Errorf("entry in the environment behind an empty argv[0]: hasEntry = %v, %v; want true", has, err)
	}
}
