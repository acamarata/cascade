package helm

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRunStub verifies that Run returns ErrNotImplemented.
func TestRunStub(t *testing.T) {
	err := Run(context.Background(), Share{}, nil, nil, time.Second)
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented, got %v", err)
	}
}
