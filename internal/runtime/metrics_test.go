package runtime

import (
	"sort"
	"sync"
	"testing"
	"time"
)

// Purpose: metrics.go's Counter/Gauge/Registry test suite — genuine
//   concurrent-access proofs under -race, a golden snapshot fixture, and
//   the duplicate-name/miss error-path cases. The periodic emitter's own
//   suite (fakes confined to their own file per Art.1) lives in
//   metrics_emitter_test.go; bootstrap wiring proof lives in
//   bootstrap_metrics_test.go — both split out of this file under
//   R-14.117 (Art.10.3's 300-line cap; a cap-driven split joins the
//   ticket's authorized write set automatically).
// Inputs: none from outside.
// Outputs: n/a (test file).
// Constraints: Art.7.1 — no test writes outside t.TempDir() (this suite
//   writes nothing to disk at all).
// SPORT: runtime/metrics (ADD, placeholder per T-1 sport_updates).

// --- Counter / Gauge concurrency -------------------------------------

func TestCounterConcurrent(t *testing.T) {
	const goroutines = 200
	const addsPerGoroutine = 500

	c := &Counter{}
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < addsPerGoroutine; j++ {
				if j%2 == 0 {
					c.Inc()
				} else {
					c.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	want := int64(goroutines * addsPerGoroutine)
	if got := c.Value(); got != want {
		t.Fatalf("Counter.Value() = %d, want exactly %d after all writers completed", got, want)
	}
}

func TestGaugeConcurrent(t *testing.T) {
	const goroutines = 200
	const opsPerGoroutine = 500

	g := &Gauge{}
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				g.Add(1)
			}
		}()
	}
	wg.Wait()

	want := int64(goroutines * opsPerGoroutine)
	if got := g.Value(); got != want {
		t.Fatalf("Gauge.Value() = %d, want exactly %d after all writers completed", got, want)
	}

	// Set is last-writer-wins; prove it is race-free (not exactness,
	// which is meaningless for concurrent Set) by racing N goroutines
	// each calling Set with their own goroutine index and reading Value
	// afterward — -race must stay clean, and the final value must be one
	// of the values actually written.
	written := make([]int64, goroutines)
	var wg2 sync.WaitGroup
	wg2.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		written[i] = int64(i)
		go func() {
			defer wg2.Done()
			g.Set(int64(i))
		}()
	}
	wg2.Wait()

	final := g.Value()
	found := false
	for _, v := range written {
		if v == final {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Gauge.Value() = %d after concurrent Set, want one of the written values", final)
	}
}

// --- Registry: duplicate panic, Get miss ------------------------------

func TestRegistryDuplicatePanic(t *testing.T) {
	r := NewRegistry()
	r.RegisterCounter("dup", nil)

	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("RegisterCounter with a duplicate name did not panic")
		}
	}()
	r.RegisterCounter("dup", nil)
}

func TestRegistryDuplicatePanic_Gauge(t *testing.T) {
	r := NewRegistry()
	r.RegisterGauge("dup-gauge", nil)

	defer func() {
		if recover() == nil {
			t.Fatal("RegisterGauge with a duplicate name did not panic")
		}
	}()
	r.RegisterGauge("dup-gauge", nil)
}

func TestRegistryDuplicatePanic_CrossType(t *testing.T) {
	r := NewRegistry()
	r.RegisterCounter("shared-name", nil)

	defer func() {
		if recover() == nil {
			t.Fatal("RegisterGauge reusing a Counter's name did not panic")
		}
	}()
	r.RegisterGauge("shared-name", nil)
}

func TestRegistryGetMiss(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("nope"); ok {
		t.Fatal("Get(\"nope\") = ok true, want false on an unregistered name")
	}
}

func TestRegistryGetHit(t *testing.T) {
	r := NewRegistry()
	want := r.RegisterCounter("hits", map[string]string{"unit": "count"})
	want.Inc()

	got, ok := r.Get("hits")
	if !ok {
		t.Fatal("Get(\"hits\") = ok false, want true")
	}
	if got.Value() != 1 {
		t.Errorf("Get(\"hits\").Value() = %d, want 1", got.Value())
	}
}

// --- Registry.Snapshot: golden fixture + concurrent-writer safety ----

func TestRegistrySnapshot(t *testing.T) {
	r := NewRegistry()
	reqs := r.RegisterCounter("requests_total", map[string]string{"route": "/health"})
	active := r.RegisterGauge("active_tasks", nil)
	reqs.Add(42)
	active.Set(-3)

	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	samples := r.Snapshot(ts)
	sortSamples(samples)

	want := []MetricSample{
		{Name: "active_tasks", Labels: nil, Value: -3, Ts: ts},
		{Name: "requests_total", Labels: map[string]string{"route": "/health"}, Value: 42, Ts: ts},
	}
	if !samplesEqual(samples, want) {
		t.Fatalf("Snapshot() = %+v, want golden fixture %+v", samples, want)
	}

	// Repeated calls with no writers in between must match exactly
	// (deterministic golden fixture, per the contract's acceptance
	// criterion).
	again := r.Snapshot(ts)
	sortSamples(again)
	if !samplesEqual(again, want) {
		t.Fatalf("repeated Snapshot() = %+v, want the same golden fixture %+v", again, want)
	}
}

// TestRegistrySnapshotConcurrentWriters proves Snapshot is race-free and
// structurally consistent (no torn/missing/duplicated entries) while
// writers are actively mutating already-registered metrics. It does not
// (and per Snapshot's documented consistency model, cannot) assert
// cross-metric exactness under concurrent load — see the Snapshot doc
// comment in metrics.go.
func TestRegistrySnapshotConcurrentWriters(t *testing.T) {
	r := NewRegistry()
	const numMetrics = 20
	counters := make([]*Counter, numMetrics)
	for i := 0; i < numMetrics; i++ {
		counters[i] = r.RegisterCounter(metricName(i), nil)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for _, c := range counters {
		c := c
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					c.Inc()
				}
			}
		}()
	}

	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 200; i++ {
		samples := r.Snapshot(ts)
		if len(samples) != numMetrics {
			close(stop)
			wg.Wait()
			t.Fatalf("Snapshot() returned %d samples mid-write, want exactly %d (structural tearing)", len(samples), numMetrics)
		}
		seen := make(map[string]bool, numMetrics)
		for _, s := range samples {
			if seen[s.Name] {
				close(stop)
				wg.Wait()
				t.Fatalf("Snapshot() returned duplicate entry %q", s.Name)
			}
			seen[s.Name] = true
		}
	}
	close(stop)
	wg.Wait()
}

func metricName(i int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	return "metric_" + string(letters[i%len(letters)]) + string(rune('0'+i/len(letters)))
}

func sortSamples(s []MetricSample) {
	sort.Slice(s, func(i, j int) bool { return s[i].Name < s[j].Name })
}

func samplesEqual(got, want []MetricSample) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].Name != want[i].Name || got[i].Value != want[i].Value || !got[i].Ts.Equal(want[i].Ts) {
			return false
		}
		if len(got[i].Labels) != len(want[i].Labels) {
			return false
		}
		for k, v := range want[i].Labels {
			if got[i].Labels[k] != v {
				return false
			}
		}
	}
	return true
}
