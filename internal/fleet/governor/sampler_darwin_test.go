//go:build darwin

package governor

// Purpose: darwin used memory follows kern.memorystatus_level and never
//
//	degrades to a guessed figure when a sysctl is missing or malformed.
//	The unit test is a pure function over injected sysctl values; the live
//	test compares the real collector with memory_pressure on this host.
import (
	"errors"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

var errNoSysctl = errors.New("no such sysctl")

// fakeSysctls returns a reader over vals; a missing name errors.
func fakeSysctls(vals map[string]uint64) func(string) (uint64, error) {
	return func(name string) (uint64, error) {
		v, ok := vals[name]
		if !ok {
			return 0, errNoSysctl
		}
		return v, nil
	}
}

func assertUnavailable(t *testing.T, err error, what string) {
	t.Helper()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("%s: err=%v (kind %v), want KindUnavailable", what, err, kind)
	}
}

func TestDarwinUsedMemoryFromMemorystatusLevel(t *testing.T) {
	const total = uint64(16) << 30
	for _, tc := range []struct{ level, wantPct uint64 }{{66, 34}, {0, 100}, {100, 0}} {
		gotTotal, used, err := darwinMemoryFrom(fakeSysctls(map[string]uint64{"hw.memsize": total, "kern.memorystatus_level": tc.level}))
		if err != nil || gotTotal != total || used != total*tc.wantPct/100 {
			t.Fatalf("level %d: total=%d used=%d err=%v, want %d and %d%% used", tc.level, gotTotal, used, err, total, tc.wantPct)
		}
	}

	refused := map[string]map[string]uint64{
		"hw.memsize missing":              {"kern.memorystatus_level": 66},
		"kern.memorystatus_level missing": {"hw.memsize": total},
		"hw.memsize zero":                 {"hw.memsize": 0, "kern.memorystatus_level": 66},
		"level above 100":                 {"hw.memsize": total, "kern.memorystatus_level": 101},
	}
	for name, vals := range refused {
		gotTotal, used, err := darwinMemoryFrom(fakeSysctls(vals))
		assertUnavailable(t, err, name)
		if gotTotal != 0 || used != 0 {
			t.Fatalf("%s: total=%d used=%d, want 0,0 (sample refused)", name, gotTotal, used)
		}
	}

	if v, err := darwinSysctlValue("w4", []byte{0x42, 0, 0, 0}); err != nil || v != 0x42 {
		t.Fatalf("4-byte value = %d, %v; want 66", v, err)
	}
	if v, err := darwinSysctlValue("w8", []byte{0, 0, 0, 0, 4, 0, 0, 0}); err != nil || v != 1<<34 {
		t.Fatalf("8-byte value = %d, %v; want %d", v, err, uint64(1)<<34)
	}
	for _, width := range []int{0, 2, 3, 16} {
		v, err := darwinSysctlValue("wbad", make([]byte, width))
		assertUnavailable(t, err, strconv.Itoa(width)+"-byte value")
		if v != 0 || !strings.Contains(err.Error(), strconv.Itoa(width)+"-byte") {
			t.Fatalf("%d-byte value = %d, %v; want 0 and an error naming the width", width, v, err)
		}
	}
}

var memoryPressureFree = regexp.MustCompile(`System-wide memory free percentage: (\d+)%`)

// TestDarwinLiveUsedMemoryTracksMemoryPressure reads the real host. It is
// required on darwin: a missing memory_pressure binary or an unparseable
// line fails it, never skips it.
func TestDarwinLiveUsedMemoryTracksMemoryPressure(t *testing.T) {
	snap, err := collectMetrics()
	if err != nil {
		t.Fatalf("collectMetrics = %v", err)
	}
	if snap.MemTotalBytes == 0 {
		t.Fatal("collectMetrics reported a zero memory total")
	}
	usedPct := 100 * float64(snap.MemUsedBytes) / float64(snap.MemTotalBytes)
	out, err := exec.Command("memory_pressure", "-Q").Output()
	if err != nil {
		t.Fatalf("memory_pressure -Q: %v", err)
	}
	m := memoryPressureFree.FindSubmatch(out)
	if m == nil {
		t.Fatalf("memory_pressure -Q printed no free percentage line: %q", out)
	}
	free, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("free percentage %q: %v", m[1], err)
	}
	t.Logf("collector used=%.1f%% (%d of %d bytes); memory_pressure free=%d%% (used %d%%)",
		usedPct, snap.MemUsedBytes, snap.MemTotalBytes, free, 100-free)
	if diff := math.Abs(usedPct - float64(100-free)); diff > 5 {
		t.Fatalf("collector used %.1f%% vs memory_pressure used %d%%: %.1f points apart, want <= 5", usedPct, 100-free, diff)
	}
}
