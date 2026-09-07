package governor

// Purpose: CompileLockRegistry's own test suite, independent of
//
//	AdmissionController (which is exercised, via a real caller, by
//	admission_test.go's TestAdmissionCompileCeilingBinds and
//	TestPermitReleaseUnlocksCompileOnce).
import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestCompileLockRegistry covers Lock/Count/unlock across independent
// repo paths, path canonicalization, and concurrent Lock/unlock pairs
// under -race never driving a count negative.
func TestCompileLockRegistry(t *testing.T) {
	r := NewCompileLockRegistry()

	if got := r.Count("repo-a"); got != 0 {
		t.Fatalf("Count on empty registry = %d, want 0", got)
	}

	unlockA1 := r.Lock("repo-a")
	if got := r.Count("repo-a"); got != 1 {
		t.Fatalf("Count after one Lock = %d, want 1", got)
	}
	unlockA2 := r.Lock("./repo-a/") // canonicalizes to the same key as "repo-a"
	if got := r.Count("repo-a"); got != 2 {
		t.Fatalf("Count after two Locks = %d, want 2", got)
	}
	if got := r.Count("repo-b"); got != 0 {
		t.Fatalf("Count for a different repo = %d, want 0 (no cross-repo leak)", got)
	}

	unlockA1()
	if got := r.Count("repo-a"); got != 1 {
		t.Fatalf("Count after one unlock = %d, want 1", got)
	}
	unlockA2()
	if got := r.Count("repo-a"); got != 0 {
		t.Fatalf("Count after both unlocks = %d, want 0", got)
	}

	if clean := filepath.Clean("repo-a/../repo-a"); clean != "repo-a" {
		t.Fatalf("sanity: filepath.Clean(%q) = %q, want repo-a", "repo-a/../repo-a", clean)
	}
}

// TestCompileLockRegistryConcurrent locks and unlocks the same and
// different repo paths from many goroutines at once (-race) and asserts
// the count always returns to exactly zero, proving Lock/unlock never
// double-counts or under-counts under contention.
func TestCompileLockRegistryConcurrent(t *testing.T) {
	r := NewCompileLockRegistry()
	const goroutines = 32
	repos := []string{"repo-a", "repo-b", "repo-c"}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		repo := repos[i%len(repos)]
		go func() {
			defer wg.Done()
			unlock := r.Lock(repo)
			if r.Count(repo) < 1 {
				t.Errorf("Count(%s) < 1 while a Lock is outstanding", repo)
			}
			unlock()
		}()
	}
	wg.Wait()

	for _, repo := range repos {
		if got := r.Count(repo); got != 0 {
			t.Fatalf("Count(%s) = %d after all unlocks, want 0", repo, got)
		}
	}
}

func TestAdmissionControllerDrainCtxCancel(t *testing.T) {
	ac := newTestController(AdmissionConfig{MaxInflight: 1, QueueCap: 1}, ResourceSnapshot{})
	permit1, _ := ac.Admit(context.Background(), AdmissionRequest{})
	defer permit1.Release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ac.Drain(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Drain with a pre-cancelled ctx and outstanding in-flight work = %v, want context.Canceled", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindCanceled {
		t.Fatalf("Drain cancellation error kind = %v (ok=%v), want KindCanceled", kind, ok)
	}
}

func TestAdmissionCancelDuringQueueNoLeak(t *testing.T) {
	ac := newTestController(AdmissionConfig{MaxInflight: 1, QueueCap: 32}, ResourceSnapshot{})
	permit1, _ := ac.Admit(context.Background(), AdmissionRequest{})

	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			go cancel() // races Admit's internal enqueue/dequeue by design
			p, err := ac.Admit(ctx, AdmissionRequest{})
			if err == nil {
				p.Release()
			}
		}()
	}
	wg.Wait()
	permit1.Release()

	if got := ac.Inflight(); got != 0 {
		t.Fatalf("Inflight after every cancelled/released caller = %d, want 0 (a leaked slot)", got)
	}
	if got := ac.QueueDepth(); got != 0 {
		t.Fatalf("QueueDepth after all cancellations resolved = %d, want 0", got)
	}
}
