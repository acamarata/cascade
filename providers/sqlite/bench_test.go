// Purpose: Art.12 risk-spike benchmark for "pure-Go SQLite under write
//
//	load" — BenchmarkStoreWriteConcurrent drives the write executor with
//	several concurrent domain writers and reports write-ops/sec and p99
//	latency, the numbers the ticket's bench-spike-findings.md journal
//	records and A-T3's bench harness registers for AB/S-58.T2. Split from
//	driver_test.go under R-14.117.
//
// SPORT: providers.sqlite.WriteExecutor/ADDED (P1-E02-W1-S02-T2).
package sqlite_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/providers/sqlite"
)

// benchDomains mirrors a representative slice of the R-14.5 ten-domain
// cascade.db set, so the spike measures the fairness queue under a
// realistic multi-domain write mix rather than one hot namespace.
var benchDomains = []string{"context", "memory", "audit", "config", "secrets", "sessions", "retrieval", "jobs"}

// BenchmarkStoreWriteConcurrent is the Art.12 spike: at GOMAXPROCS=4 and
// GOMAXPROCS=8 (`go test -bench=BenchmarkStoreWriteConcurrent -cpu=4,8`),
// b.RunParallel drives one writer goroutine per GOMAXPROCS slot, each
// bound to one domain in round-robin, all funneling through the single
// write-connection executor. Latency is sampled per-op (test code, so
// time.Now is permitted — forbidigo exempts _test.go) and reduced to p99
// after the timed region closes.
func BenchmarkStoreWriteConcurrent(b *testing.B) {
	path := filepath.Join(b.TempDir(), "bench.db")
	d, err := sqlite.Open(context.Background(), path)
	if err != nil {
		b.Fatalf("sqlite.Open: %v", err)
	}
	defer func() { _ = d.Close() }()

	var mu sync.Mutex
	latencies := make([]time.Duration, 0, b.N)
	var worker int64

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		id := atomic.AddInt64(&worker, 1)
		domain := benchDomains[int(id)%len(benchDomains)]
		n := 0
		for pb.Next() {
			key := fmt.Sprintf("k-%d-%d", id, n)
			start := time.Now()
			if err := d.Put(ctx, domain, key, []byte("bench-value")); err != nil {
				b.Fatalf("Put: %v", err)
			}
			sample := time.Since(start)
			mu.Lock()
			latencies = append(latencies, sample)
			mu.Unlock()
			n++
		}
	})
	elapsed := b.Elapsed()
	b.StopTimer()

	if len(latencies) == 0 {
		return
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p99 := latencies[int(float64(len(latencies)-1)*0.99)]

	opsPerSec := float64(len(latencies)) / elapsed.Seconds()
	b.ReportMetric(opsPerSec, "ops/sec")
	b.ReportMetric(float64(p99.Microseconds()), "p99-us")
}
