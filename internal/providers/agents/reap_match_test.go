package agents

// Purpose: the marker's identity rules and the sweep loop over a fake process
// table (survivor, recycled pid, error paths), with no process spawned: an environ
// entry matches only whole, the marker has the NewLeaseMarker shape and
// nothing wider, and the darwin KERN_PROCARGS2 buffer is cut at the right
// place on every platform.
//
// Constraints: pure functions over byte slices; no process, no file.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestMarkerMatchExactEntryOnly(t *testing.T) {
	entry := LeaseMarkerEnv + "=clm_a"
	cases := []struct {
		name string
		blob string
		want bool
	}{
		{"exact entry", LeaseMarkerEnv + "=clm_a\x00", true},
		{"exact entry, no trailing NUL", LeaseMarkerEnv + "=clm_a", true},
		{"marker with the wanted one as a prefix", LeaseMarkerEnv + "=clm_ab\x00", false},
		{"marker with the wanted one as a suffix", LeaseMarkerEnv + "=xclm_a\x00", false},
		{"key with a prefix", "X" + LeaseMarkerEnv + "=clm_a\x00", false},
		{"entry embedded in another value", "PATH=/x:" + entry + "\x00", false},
		{"entry among others", "A=1\x00" + entry + "\x00B=2\x00", true},
		{"other key", "OTHER=clm_a\x00", false},
		{"empty blob", "", false},
		{"only NULs", "\x00\x00", false},
	}
	for _, c := range cases {
		if got := environHas([]byte(c.blob), entry); got != c.want {
			t.Errorf("%s: environHas(%q, %q) = %v, want %v", c.name, c.blob, entry, got, c.want)
		}
	}
}

func TestValidMarkerRejectsAnythingWiderThanAMarker(t *testing.T) {
	m, err := NewLeaseMarker()
	if err != nil || !validMarker(m) {
		t.Fatalf("NewLeaseMarker() = %q, %v; want a valid marker", m, err)
	}
	for _, bad := range []string{
		"", "clm_", "clm_a", strings.TrimSuffix(m, m[len(m)-1:]), m + "0",
		"CLM_" + m[4:], "clm_" + strings.ToUpper(m[4:]), "clm_" + strings.Repeat("g", markerHexLen),
		" " + m, m + "\n",
	} {
		if validMarker(bad) {
			t.Errorf("validMarker(%q) = true, want false", bad)
		}
	}
}

func TestNewLeaseMarkerIsFreshAndFailsClosedOnNoEntropy(t *testing.T) {
	a, errA := NewLeaseMarker()
	b, errB := NewLeaseMarker()
	if errA != nil || errB != nil || a == b {
		t.Fatalf("two markers = %q, %q (%v, %v); want two distinct values", a, b, errA, errB)
	}
	if len(a) != len(markerPrefix)+markerHexLen {
		t.Fatalf("marker %q has length %d, want %d", a, len(a), len(markerPrefix)+markerHexLen)
	}
	_, err := newLeaseMarker(errReader{})
	if kind, _ := cascade.KindOf(err); err == nil || kind != cascade.KindUnavailable {
		t.Fatalf("newLeaseMarker(failing source) = %v, want KindUnavailable", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

// procArgs builds a KERN_PROCARGS2 buffer: argc, the exec path padded with
// NULs to 8 bytes as the kernel does, the arguments, then the environment.
func procArgs(argc uint32, path string, args, env []string) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.NativeEndian, argc)
	b.WriteString(path + strings.Repeat("\x00", 1+(8-(len(path)+1)%8)%8))
	for _, a := range args {
		b.WriteString(a + "\x00")
	}
	for _, e := range env {
		b.WriteString(e + "\x00")
	}
	return b.Bytes()
}

func TestProcArgsHasCutsAtTheEnvironment(t *testing.T) {
	entry := LeaseMarkerEnv + "=clm_a"
	buf := procArgs(2, "/bin/tool", []string{"tool", "--x"}, []string{"A=1", entry})
	if !procArgsHas(buf, entry) {
		t.Fatal("procArgsHas = false, want the environment entry found")
	}
	// Text that is only part of an argument, or only an argument, is not an entry.
	if procArgsHas(procArgs(2, "/bin/tool", []string{"tool", "--e=" + entry}, []string{"A=1"}), entry) {
		t.Fatal("an argument holding the entry as a substring matched as environment")
	}
	// An empty argv[0] merges with the padding: the entry must still be found.
	for _, args := range [][]string{{""}, {"", "--flag"}} {
		argc := uint32(len(args))
		if !procArgsHas(procArgs(argc, "/bin/tool", args, []string{entry}), entry) {
			t.Errorf("args %q: the environment entry was lost behind an empty argument", args)
		}
	}
	for name, bad := range map[string][]byte{
		"short":         {1, 0},
		"no exec path":  append([]byte{1, 0, 0, 0}, entry...),
		"argc too high": procArgs(9, "/bin/tool", []string{"tool"}, []string{entry}),
	} {
		if procArgsHas(bad, entry) {
			t.Errorf("%s: a malformed buffer produced a match", name)
		}
	}
}

// fakeSrc is an in-memory process table: pid -> environment entry.
type fakeSrc struct {
	mu       sync.Mutex
	env      map[int]string
	immortal map[int]bool
	killed   []int
	listErr  error
	recycled map[int]bool  // pids that belong to an unrelated process by the time of the kill
	inspect  map[int]error // pids whose environment cannot be inspected
	// clearAfter, when > 0, drops the inspect errors after that many list calls.
	clearAfter int
	lists      int
}

func (f *fakeSrc) list() ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int
	f.lists++
	if f.clearAfter > 0 && f.lists > f.clearAfter {
		f.inspect = nil
	}
	for pid := range f.env {
		out = append(out, pid)
	}
	slices.Sort(out)
	return out, f.listErr
}

func (f *fakeSrc) hasEntry(pid int, entry string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.env[pid] == entry, f.inspect[pid]
}

func (f *fakeSrc) kill(pid int, entry string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recycled[pid] {
		f.env[pid] = "PATH=/usr/bin" // the unrelated process now owning the pid
		return nil
	}
	if f.env[pid] != entry {
		return nil
	}
	f.killed = append(f.killed, pid)
	if !f.immortal[pid] {
		delete(f.env, pid)
	}
	return nil
}

const fakeMarker = "clm_00000000000000000000000000000001"

var fakeEntry = LeaseMarkerEnv + "=" + fakeMarker

func fastSweeper(src procSource) sweeper { return sweeper{src: src, rounds: 3} }

func TestLeaseSweepFailsClosedOnSurvivor(t *testing.T) {
	src := &fakeSrc{env: map[int]string{4001: fakeEntry, 4002: fakeEntry}, immortal: map[int]bool{4002: true}}
	err := fastSweeper(src).sweep(context.Background(), fakeMarker)
	requireSentinel(t, err, ErrLeaseSweepIncomplete)
	if kind, _ := cascade.KindOf(err); kind != cascade.KindUnavailable {
		t.Fatalf("survivor error kind = %v, want KindUnavailable", kind)
	}
	if !slices.Contains(src.killed, 4001) || !slices.Contains(src.killed, 4002) {
		t.Fatalf("killed = %v, want both marked pids signalled", src.killed)
	}
}

func TestLeaseSweepSuccessFollowsAnEmptyRelist(t *testing.T) {
	src := &fakeSrc{env: map[int]string{4001: fakeEntry, 4002: LeaseMarkerEnv + "=clm_other"}}
	if err := fastSweeper(src).sweep(context.Background(), fakeMarker); err != nil {
		t.Fatalf("sweep = %v, want nil once the re-list is empty", err)
	}
	if !slices.Equal(src.killed, []int{4001}) {
		t.Fatalf("killed = %v, want only the marked pid", src.killed)
	}
	if got, _, _ := fastSweeper(src).marked(context.Background(), fakeEntry); len(got) != 0 {
		t.Fatalf("re-list after success = %v, want empty", got)
	}
}

func TestLeaseSweepNeverSignalsSelfOrInit(t *testing.T) {
	src := &fakeSrc{env: map[int]string{1: fakeEntry, os.Getpid(): fakeEntry}}
	if err := fastSweeper(src).sweep(context.Background(), fakeMarker); err != nil || len(src.killed) != 0 {
		t.Fatalf("sweep = %v, killed %v; want nil and no signal to pid 1 or self", err, src.killed)
	}
}

func TestLeaseSweepSkipsAPidThatNoLongerCarriesTheMarker(t *testing.T) {
	src := &fakeSrc{env: map[int]string{4001: fakeEntry}, recycled: map[int]bool{4001: true}}
	if err := fastSweeper(src).sweep(context.Background(), fakeMarker); err != nil || len(src.killed) != 0 {
		t.Fatalf("sweep = %v, killed %v; want a recycled pid left alone", err, src.killed)
	}
}

func TestLeaseSweepRefusesAWideMarker(t *testing.T) {
	for _, m := range []string{"", "clm_", "clm_a", "x"} {
		err := SweepLease(context.Background(), m)
		if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
			t.Errorf("SweepLease(%q) = %v, want KindInvalidInput", m, err)
		}
	}
}

func TestLeaseSweepListAndContextErrors(t *testing.T) {
	src := &fakeSrc{env: map[int]string{4001: fakeEntry}, listErr: errors.New("table gone")}
	if kind, _ := cascade.KindOf(fastSweeper(src).sweep(context.Background(), fakeMarker)); kind != cascade.KindUnavailable {
		t.Fatalf("unlistable table kind = %v, want KindUnavailable", kind)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if kind, _ := cascade.KindOf(fastSweeper(&fakeSrc{}).sweep(ctx, fakeMarker)); kind != cascade.KindCanceled {
		t.Fatalf("canceled sweep kind = %v, want KindCanceled", kind)
	}
}

func TestLeaseSweepUnsupportedOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}
	if kind, _ := cascade.KindOf(SweepLease(context.Background(), fakeMarker)); kind != cascade.KindUnsupported {
		t.Fatalf("SweepLease on windows kind = %v, want KindUnsupported", kind)
	}
}
