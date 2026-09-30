//go:build linux

package governor

// Purpose: the linux collector never publishes a missing reading as a
//
//	measured one. /proc fixtures are fed through procCollector's read
//	seam and driven through Sampler.tick with a FixedClock; no real /proc
//	read, no sleep.
import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const healthyMeminfo = "MemTotal: 16000000 kB\nMemAvailable: 8000000 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n"

// procFixture serves /proc/stat and /proc/meminfo from memory.
type procFixture struct{ stat, meminfo string }

func (f *procFixture) read(path string) ([]byte, error) {
	switch path {
	case procStatPath:
		return []byte(f.stat), nil
	case procMeminfoPath:
		return []byte(f.meminfo), nil
	}
	return nil, os.ErrNotExist
}

// procRig is a Sampler whose collector is a fresh procCollector over fx.
func procRig(fx *procFixture) (*Sampler, *procCollector, *runtime.FixedClock) {
	clk := runtime.NewFixedClock(time.Unix(7_000, 0))
	c := &procCollector{read: fx.read}
	s := NewSampler(SamplerConfig{}, clk, newFakeTicker(), nil)
	s.collect = c.collect
	return s, c, clk
}

func TestLinuxCPUFirstTickPublishesNothing(t *testing.T) {
	fx := &procFixture{stat: "cpu 100 0 100 800 0 0 0\n", meminfo: healthyMeminfo}
	_, err := (&procCollector{read: fx.read}).collect()
	assertSentinel(t, err, ErrCPUBaselinePending, "first /proc/stat read")

	s, _, clk := procRig(fx)
	s.tick()
	if got := s.Snapshot(); got != (ResourceSnapshot{}) {
		t.Fatalf("first tick published %+v, want nothing (no CPU baseline)", got)
	}
	fx.stat = "cpu 150 0 150 900 0 0 0\n" // +200 total, +100 idle
	clk.Advance(time.Second)
	s.tick()
	got := s.Snapshot()
	if got.SampledAt != clk.Now() || got.CPUFraction != 0.5 || got.MemTotalBytes != 16000000*1024 {
		t.Fatalf("second tick = %+v, want SampledAt=%v CPUFraction=0.5 and memory", got, clk.Now())
	}
}

func TestLinuxCPURegressionRepublishesNothing(t *testing.T) {
	regressions := map[string]string{
		"total went backwards":            "cpu 10 0 10 80 0 0 0\n",
		"total grew, idle went backwards": "cpu 400 0 400 850 0 0 0\n",
	}
	for name, stat := range regressions {
		t.Run(name, func(t *testing.T) {
			fx := &procFixture{stat: "cpu 100 0 100 800 0 0 0\n", meminfo: healthyMeminfo}
			s, c, clk := procRig(fx)
			s.tick()
			fx.stat = "cpu 150 0 150 900 0 0 0\n"
			clk.Advance(time.Second)
			s.tick()
			published := s.Snapshot()
			if published.SampledAt.IsZero() {
				t.Fatal("baseline ticks published nothing")
			}
			fx.stat = stat
			_, err := c.collect()
			assertSentinel(t, err, ErrCPUBaselinePending, "regressed")
			clk.Advance(time.Second)
			s.tick() // same counters again: still no delta to measure
			if got := s.Snapshot(); got != published {
				t.Fatalf("regressed counters republished %+v, want the old snapshot %+v", got, published)
			}
		})
	}

	// "cpu 110 0 100 900" after "cpu 100 0 100 800" is a real reading
	// (total +110, idle +100). The next line lowers user time so total
	// grows by 10 while idle grows by 100: only the idle-delta guard
	// catches it, and without it the uint64 difference underflows into a
	// huge CPUFraction.
	fx := &procFixture{stat: "cpu 100 0 100 800\n", meminfo: healthyMeminfo}
	c := &procCollector{read: fx.read}
	if _, err := c.collect(); err == nil {
		t.Fatal("first read measured a CPU fraction without a baseline")
	}
	fx.stat = "cpu 110 0 100 900\n"
	if snap, err := c.collect(); err != nil || snap.CPUFraction != 10.0/110.0 {
		t.Fatalf("cpu 110 0 100 900 = %+v, %v; want CPUFraction 10/110", snap, err)
	}
	fx.stat = "cpu 20 0 100 1000\n"
	snap, err := c.collect()
	assertSentinel(t, err, ErrCPUBaselinePending, "regressed")
	if snap != (ResourceSnapshot{}) {
		t.Fatalf("idle delta above total delta returned %+v, want nothing", snap)
	}
}

func TestLinuxMeminfoWithoutMemAvailableRefuses(t *testing.T) {
	fx := &procFixture{stat: "cpu 100 0 100 800 0 0 0\n", meminfo: "MemTotal: 16000000 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n"}
	s, c, clk := procRig(fx)
	s.tick() // records the CPU baseline
	fx.stat = "cpu 150 0 150 900 0 0 0\n"
	_, err := c.collect()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable || !strings.Contains(err.Error(), "MemAvailable") {
		t.Fatalf("collect without MemAvailable = %v, want KindUnavailable naming MemAvailable", err)
	}
	fx.stat = "cpu 200 0 200 1000 0 0 0\n"
	clk.Advance(time.Second)
	s.tick()
	if got := s.Snapshot(); got != (ResourceSnapshot{}) {
		t.Fatalf("meminfo without MemAvailable published %+v, want nothing", got)
	}
	ac := NewAdmissionController(s, AdmissionConfig{MaxInflight: 4, QueueCap: 4}, clk)
	assertSentinel(t, admitOnce(ac), ErrStaleResourceSignal, "posture=stale")
	if ac.QueueDepth() != 0 || ac.Inflight() != 0 {
		t.Fatalf("Inflight=%d QueueDepth=%d, want 0,0", ac.Inflight(), ac.QueueDepth())
	}
}

func TestLinuxMeminfoOutOfRangeRefuses(t *testing.T) {
	cases := map[string]string{
		"MemAvailable above MemTotal": "MemTotal: 1000 kB\nMemAvailable: 1001 kB\n",
		"MemAvailable unparseable":    "MemTotal: 1000 kB\nMemAvailable: lots kB\n",
		"MemAvailable empty":          "MemTotal: 1000 kB\nMemAvailable:\n",
		"MemTotal zero":               "MemTotal: 0 kB\nMemAvailable: 0 kB\n",
	}
	for name, meminfo := range cases {
		t.Run(name, func(t *testing.T) {
			fx := &procFixture{stat: "cpu 100 0 100 800 0 0 0\n", meminfo: meminfo}
			s, c, clk := procRig(fx)
			s.tick() // records the CPU baseline
			fx.stat = "cpu 150 0 150 900 0 0 0\n"
			snap, err := c.collect()
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable || !strings.Contains(err.Error(), "MemAvailable") {
				t.Fatalf("collect = %+v, %v; want KindUnavailable naming MemAvailable", snap, err)
			}
			fx.stat = "cpu 200 0 200 1000 0 0 0\n"
			clk.Advance(time.Second)
			s.tick()
			if got := s.Snapshot(); got != (ResourceSnapshot{}) {
				t.Fatalf("published %+v, want nothing", got)
			}
		})
	}
	fx := &procFixture{stat: "cpu 100 0 100 800 0 0 0\n", meminfo: "MemTotal: 1000 kB\nMemAvailable: 1000 kB\n"}
	c := &procCollector{read: fx.read}
	_, _ = c.collect()
	fx.stat = "cpu 150 0 150 900 0 0 0\n"
	if snap, err := c.collect(); err != nil || snap.MemTotalBytes != 1000*1024 || snap.MemUsedBytes != 0 {
		t.Fatalf("MemAvailable == MemTotal = %+v, %v; want a measured 0 used", snap, err)
	}
}
