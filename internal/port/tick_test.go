package port

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestTickNeverOverlaps(t *testing.T) {
	tick := &Tick{
		MaxItems:      10,
		LeaseDuration: time.Second,
		Journal: func(_ context.Context) ([]string, error) {
			return []string{"a"}, nil
		},
	}

	var wg sync.WaitGroup
	wg.Add(2)

	block := make(chan struct{})
	started := make(chan struct{})

	fn := func(_ context.Context, _ []string) error {
		close(started)
		<-block
		return nil
	}

	var err1, err2 error

	go func() {
		defer wg.Done()
		err1 = tick.Run(context.Background(), fn)
	}()

	<-started

	go func() {
		defer wg.Done()
		time.Sleep(10 * time.Millisecond)
		err2 = tick.Run(context.Background(), fn)
	}()

	time.Sleep(50 * time.Millisecond)
	close(block)
	wg.Wait()

	if err1 != nil {
		t.Errorf("expected first run to succeed, got %v", err1)
	}
	if !errors.Is(err2, ErrTickOverlapping) {
		t.Errorf("expected second run to fail with ErrTickOverlapping, got %v", err2)
	}
}

func TestTickBoundsWork(t *testing.T) {
	tick := &Tick{
		MaxItems:      2,
		LeaseDuration: time.Second,
		Journal: func(_ context.Context) ([]string, error) {
			return []string{"a", "b", "c", "d", "e"}, nil
		},
	}

	var got []string
	fn := func(_ context.Context, items []string) error {
		got = append(got, items...)
		return nil
	}

	err := tick.Run(context.Background(), fn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) != 2 {
		t.Errorf("expected 2 items, got %d", len(got))
	}
}

func TestTickResumesFromJournal(t *testing.T) {
	journalCalled := false
	tick := &Tick{
		MaxItems:      10,
		LeaseDuration: time.Second,
		Journal: func(_ context.Context) ([]string, error) {
			journalCalled = true
			return []string{"resume1", "resume2"}, nil
		},
	}

	var got []string
	fn := func(_ context.Context, items []string) error {
		got = append(got, items...)
		return nil
	}

	err := tick.Run(context.Background(), fn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !journalCalled {
		t.Error("expected Journal callback to be called")
	}

	if len(got) != 2 || got[0] != "resume1" || got[1] != "resume2" {
		t.Errorf("expected items from journal, got %v", got)
	}
}
