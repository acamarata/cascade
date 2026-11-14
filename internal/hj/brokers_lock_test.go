package hj

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRecoveryHoldsLockAgainstHealth(t *testing.T) {
	b := NewBrokers()
	recoverStarted := make(chan struct{})
	recoverDone := make(chan struct{})
	healthDone := make(chan struct{})

	go b.Recover(func() {
		close(recoverStarted)
		<-recoverDone
	})

	<-recoverStarted

	go func() {
		b.Health()
		close(healthDone)
	}()

	select {
	case <-healthDone:
		t.Fatal("Health completed while Recover was still running")
	case <-time.After(50 * time.Millisecond):
		// Expected: Health is blocked
	}

	close(recoverDone)
	<-healthDone
}

func TestRecoveryHoldsLockAgainstGC(t *testing.T) {
	b := NewBrokers()
	recoverStarted := make(chan struct{})
	recoverDone := make(chan struct{})
	gcDone := make(chan struct{})

	go b.Recover(func() {
		close(recoverStarted)
		<-recoverDone
	})

	<-recoverStarted

	go func() {
		b.GC()
		close(gcDone)
	}()

	select {
	case <-gcDone:
		t.Fatal("GC completed while Recover was still running")
	case <-time.After(50 * time.Millisecond):
		// Expected: GC is blocked
	}

	close(recoverDone)
	<-gcDone
}

func TestNoConcurrentRecoveryHealthGC(t *testing.T) {
	b := NewBrokers()
	var active int64
	var maxActive int64

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			work := func() {
				current := atomic.AddInt64(&active, 1)
				for {
					old := atomic.LoadInt64(&maxActive)
					if current <= old || atomic.CompareAndSwapInt64(&maxActive, old, current) {
						break
					}
				}
				// simulate some work
				time.Sleep(5 * time.Millisecond)
				atomic.AddInt64(&active, -1)
			}
			switch idx % 3 {
			case 0:
				b.Recover(work)
			case 1:
				b.Health(work)
			case 2:
				b.GC(work)
			}
		}(i)
	}
	wg.Wait()

	if maxActive > 1 {
		t.Fatalf("active counter went above 1, maxActive=%d", maxActive)
	}
}

func TestHealthRunsAfterRecoveryEnds(t *testing.T) {
	b := NewBrokers()
	b.Recover() // complete a recovery

	healthDone := make(chan struct{})
	go func() {
		b.Health()
		close(healthDone)
	}()

	select {
	case <-healthDone:
		// success
	case <-time.After(50 * time.Millisecond):
		t.Fatal("Health did not complete after Recover ended")
	}
}
