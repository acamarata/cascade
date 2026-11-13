package hj

import (
	"errors"
	"testing"
	"time"
)

func TestStartBlocksUntilFirstScanCompletes(t *testing.T) {
	scanned := false
	b := Broker{
		Scan: func() (ScanState, error) {
			time.Sleep(50 * time.Millisecond)
			scanned = true
			return ScanState{Done: true}, nil
		},
	}

	_, err := b.Start()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !scanned {
		t.Fatal("Start returned before scan completed")
	}
}

func TestFirstScanExecutesSynchronously(t *testing.T) {
	called := false
	b := Broker{
		Scan: func() (ScanState, error) {
			called = true
			return ScanState{Done: true}, nil
		},
	}

	state, err := b.Start()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !state.Done {
		t.Fatal("scan state not Done")
	}
	if !called {
		t.Fatal("scan was not called synchronously")
	}
}

func TestStartReturnsErrorOnScanFailure(t *testing.T) {
	b := Broker{
		Scan: func() (ScanState, error) {
			return ScanState{}, errors.New("scan error")
		},
	}

	_, err := b.Start()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "M0881: first scan failed: scan error" {
		t.Fatalf("unexpected error message: %v", err)
	}
}
