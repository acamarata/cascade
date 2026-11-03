package jobs

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestGateAttemptPolicyDelay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	policy := GateAttemptPolicy{}
	for i, want := range []time.Duration{5 * time.Minute, 15 * time.Minute, 45 * time.Minute} {
		got, err := policy.Delay(i + 1)
		if err != nil || got != want {
			t.Fatalf("Delay(%d) = %v, %v; want %v", i+1, got, err, want)
		}
	}
	for _, attempt := range []int{0, 4, -1} {
		got, err := policy.Delay(attempt)
		if got != 0 || err != ErrGateAttemptOutOfRange || err.Error() != "invalid-input: jobs: gate attempt out of range" {
			t.Fatalf("Delay(%d) = %v, %v; want range sentinel", attempt, got, err)
		}
	}
}

func TestRecordGateDenial(t *testing.T) {
	t.Run("durable attempts and execution reset", testGateDenialPersistence)
	t.Run("concurrent distinct attempts", testGateDenialConcurrency)
	t.Run("store error returned", func(t *testing.T) {
		s, _ := newGateAttemptStore(t)
		if err := s.db.Close(); err != nil {
			t.Fatal(err)
		}
		if got, err := s.RecordGateDenial(context.Background(), "gate-job", "denied"); got != 0 || !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("closed store = %d, %v; want zero and unavailable", got, err)
		}
	})
	t.Run("canceled store operation", func(t *testing.T) {
		s, _ := newGateAttemptStore(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got, err := s.RecordGateDenial(ctx, "gate-job", "denied"); got != 0 || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled operation = %d, %v; want zero and cancellation", got, err)
		}
	})
	t.Run("schema guards", testGateSchemaGuards)
	t.Run("invalid and missing execution", func(t *testing.T) {
		s, _ := newGateAttemptStore(t)
		ctx := context.Background()
		if _, err := s.RecordGateDenial(ctx, "missing-job", "denied"); err == nil {
			t.Fatal("missing execution succeeded")
		}
		var missing *Store
		if _, err := missing.RecordGateDenial(ctx, "gate-job", "denied"); err == nil {
			t.Fatal("nil store succeeded")
		}
	})
}

func testGateDenialPersistence(t *testing.T) {
	s, path := newGateAttemptStore(t)
	ctx := context.Background()
	reason := "  denied\n\tverbatim\x00\xff  "
	for want := 1; want <= 3; want++ {
		got, err := s.RecordGateDenial(ctx, "gate-job", reason)
		if err != nil || got != want {
			t.Fatalf("RecordGateDenial = %d, %v; want %d", got, err, want)
		}
		checkGateReason(t, s, "gate-exec-1", reason)
	}
	reapplyGateAttemptSchema(t, s)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	s = NewStore(db)
	var attempts int
	if err := db.QueryRow(`SELECT attempts FROM jobs_gate_attempts WHERE execution_id = 'gate-exec-1'`).Scan(&attempts); err != nil || attempts != 3 {
		t.Fatalf("reopened attempts = %d, %v", attempts, err)
	}
	checkGateReason(t, s, "gate-exec-1", reason)
	seedGateExecution(t, s, "gate-exec-2", 2)
	if got, err := s.RecordGateDenial(ctx, "gate-job", reason); err != nil || got != 1 {
		t.Fatalf("new execution = %d, %v; want 1", got, err)
	}
}

func testGateDenialConcurrency(t *testing.T) {
	s, _ := newGateAttemptStore(t)
	const count = 12
	var wg sync.WaitGroup
	results := make(chan int, count)
	errors := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := s.RecordGateDenial(context.Background(), "gate-job", "denied")
			results <- n
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[int]bool)
	for n := range results {
		if n < 1 || n > count || seen[n] {
			t.Fatalf("invalid or duplicate attempt %d", n)
		}
		seen[n] = true
	}
}

func TestShouldStallThreshold(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if GateAttemptLimit != 3 {
		t.Fatalf("GateAttemptLimit = %d, want 3", GateAttemptLimit)
	}
	for _, attempt := range []int{-1, 0, 1, 2, 3, 4} {
		if got := (GateAttemptPolicy{}).ShouldStall(attempt); got != (attempt >= 3) {
			t.Fatalf("ShouldStall(%d) = %v", attempt, got)
		}
	}
}
