package runtime

import (
	"sync"
	"sync/atomic"
	"time"
)

// Purpose: a lightweight, concurrent-safe in-process metrics registry —
//   Counter, Gauge, and a Registry that snapshots them for diagnostics and
//   periodic event-bus emission. This is INTERNAL operational
//   instrumentation, not the TELEMETRY egress system (H/S-16.T1's
//   enumerated inventory owns that separate, opt-in, anonymized surface —
//   see docs/developer/runtime.md for the boundary statement).
// Inputs: subsystem call sites register named Counter/Gauge instances at
//   startup (composition root, per Art.10.2) and mutate them from any
//   goroutine thereafter; the periodic emitter reads an injected Clock and
//   Ticker, never the wall clock or a real sleep.
// Outputs: Registry.Snapshot() returns a []MetricSample; the periodic
//   emitter (metrics_emitter.go) publishes a MetricsSnapshotEvent through
//   an injected EventBus.
// Constraints: 12-QUALITY-CONSTITUTION.md Art.4 (core-engine coverage
//   floor); no bare time.Now (02-TARGET-STRUCTURE §v1.1); RegisterCounter/
//   RegisterGauge panic on a duplicate name — a programming error caught
//   at startup, never at runtime traffic time (P1-E03-W1-S05-T4).
// SPORT: runtime/metrics (ADD, placeholder per T-1 sport_updates).

// Metric is the common read surface both Counter and Gauge satisfy, used
// by Registry to hold either kind in one map without an interface{} cast
// at every call site.
type Metric interface {
	// Value returns the metric's current int64 value.
	Value() int64
}

// Counter is a monotonically increasing int64, safe for concurrent use
// from any number of goroutines. It never decrements — there is
// deliberately no Sub/Dec method.
type Counter struct {
	v int64
}

// Inc increments the counter by 1.
func (c *Counter) Inc() { atomic.AddInt64(&c.v, 1) }

// Add increments the counter by n. n is expected to be non-negative;
// Counter does not enforce this (the caller owns that invariant), but a
// negative n would violate the "never decrements" contract and should
// never appear in domain code.
func (c *Counter) Add(n int64) { atomic.AddInt64(&c.v, n) }

// Value returns the counter's current value.
func (c *Counter) Value() int64 { return atomic.LoadInt64(&c.v) }

// Gauge is a settable int64, safe for concurrent use from any number of
// goroutines. Unlike Counter it may go negative and may decrease.
type Gauge struct {
	v int64
}

// Set replaces the gauge's value with v.
func (g *Gauge) Set(v int64) { atomic.StoreInt64(&g.v, v) }

// Add adjusts the gauge's value by delta, which may be negative.
func (g *Gauge) Add(delta int64) { atomic.AddInt64(&g.v, delta) }

// Value returns the gauge's current value.
func (g *Gauge) Value() int64 { return atomic.LoadInt64(&g.v) }

// MetricSample is a point-in-time read of one registered metric, as
// returned by Registry.Snapshot. Ts is stamped from the caller-supplied
// Clock, never a bare time.Now.
type MetricSample struct {
	Name   string
	Labels map[string]string
	Value  int64
	Ts     time.Time
}

// MetricsSnapshotEvent is the payload the periodic emitter (see
// metrics_emitter.go) publishes on the event bus: the full set of samples
// taken in one Registry.Snapshot call, plus the instant the emitter tick
// fired.
type MetricsSnapshotEvent struct {
	Samples []MetricSample
	Ts      time.Time
}

// registryEntry pairs a registered Metric with the labels it was
// registered under, so Snapshot can populate MetricSample.Labels without
// a second map lookup.
type registryEntry struct {
	metric Metric
	labels map[string]string
}

// Registry is a thread-safe collection of named metrics. The zero value is
// not usable; construct with NewRegistry.
//
// Cardinality: Registry places no numeric cap on the number of distinct
// names (the contract specifies none — R-14.107 forbids inventing one).
// It is bounded in practice by its access pattern, not a limit check:
// registration is a startup-time, composition-root call (Art.10.2 — "no
// subsystem imports the metrics package by init"), driven by a fixed set
// of names fixed subsystems compile in, never by untrusted runtime input
// (a request path, a plugin payload, a user string). A caller that did
// register names sourced from unbounded external input would be misusing
// the API outside its documented contract; that is a call-site concern
// this package cannot enforce without inventing a limit the contract does
// not ask for.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]registryEntry
}

// NewRegistry returns an empty, ready-to-use Registry.
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]registryEntry)}
}

// RegisterCounter creates and registers a new Counter under name with the
// given labels, returning it for the caller to hold and mutate. It panics
// if name is already registered — a duplicate registration is a
// programming error, meant to be caught at process startup, not handled
// as a runtime condition.
func (r *Registry) RegisterCounter(name string, labels map[string]string) *Counter {
	c := &Counter{}
	r.register(name, labels, c)
	return c
}

// RegisterGauge creates and registers a new Gauge under name with the
// given labels, returning it for the caller to hold and mutate. It panics
// if name is already registered, for the same reason as RegisterCounter.
func (r *Registry) RegisterGauge(name string, labels map[string]string) *Gauge {
	g := &Gauge{}
	r.register(name, labels, g)
	return g
}

// register inserts metric under name, holding the write lock for the
// duration. It panics on a duplicate name.
func (r *Registry) register(name string, labels map[string]string, metric Metric) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[name]; exists {
		panic("runtime: metric already registered: " + name)
	}
	r.entries[name] = registryEntry{metric: metric, labels: labels}
}

// Get looks up a registered metric by name. ok is false if no metric is
// registered under name.
func (r *Registry) Get(name string) (metric Metric, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[name]
	if !ok {
		return nil, false
	}
	return entry.metric, true
}

// Snapshot returns a MetricSample for every registered metric, read in one
// RLock-held pass.
//
// Consistency model, stated precisely: Snapshot is STRUCTURALLY atomic —
// no metric can be added, removed, or renamed while a Snapshot is in
// progress, because registration requires the write lock Snapshot holds
// for its whole pass. It is NOT a cross-metric transaction: each sample's
// Value is an independent atomic load (Counter/Gauge deliberately do not
// take the Registry's lock to mutate, so writers never block on a
// snapshot in progress). Two samples in the same Snapshot call may
// therefore reflect writes from different instants relative to each
// other if writers are concurrently active — there is no global write
// barrier. This is the same trade every lock-free counter registry makes;
// treat ratios between two samples from one Snapshot as approximate under
// concurrent load, and exact only when no writer is active during the
// call (see TestRegistrySnapshot for the exact case, and
// TestRegistrySnapshotConcurrentWriters for the concurrent case's
// guarantee: no torn/missing/duplicated entries, only possibly-in-flight
// values).
func (r *Registry) Snapshot(ts time.Time) []MetricSample {
	r.mu.RLock()
	defer r.mu.RUnlock()
	samples := make([]MetricSample, 0, len(r.entries))
	for name, entry := range r.entries {
		samples = append(samples, MetricSample{
			Name:   name,
			Labels: entry.labels,
			Value:  entry.metric.Value(),
			Ts:     ts,
		})
	}
	return samples
}
