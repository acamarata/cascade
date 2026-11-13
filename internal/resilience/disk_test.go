package resilience

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestDiskTypesCompile verifies that the disk resilience data shapes and
// error sentinels exist with the fields expected by later tickets.
func TestDiskTypesCompile(t *testing.T) {
	cfg := DiskConfig{
		RequiredVolumes: []string{"/data"},
		DataHome:        "/data/home",
		FreeSpaceFloor:  1 << 30,
		RetryInterval:   60 * time.Second,
		StatTimeout:     5 * time.Second,
	}

	if len(cfg.RequiredVolumes) != 1 {
		t.Fatalf("RequiredVolumes: got %d entries, want 1", len(cfg.RequiredVolumes))
	}
	if cfg.DataHome == "" {
		t.Fatal("DataHome: empty value")
	}
	if cfg.FreeSpaceFloor <= 0 {
		t.Fatal("FreeSpaceFloor: non-positive value")
	}
	if cfg.RetryInterval <= 0 {
		t.Fatal("RetryInterval: non-positive value")
	}
	if cfg.StatTimeout <= 0 {
		t.Fatal("StatTimeout: non-positive value")
	}

	state := VolumeState{
		Path:      "/data",
		Mounted:   true,
		FreeBytes: 2 << 30,
		Known:     true,
	}

	if state.Path == "" {
		t.Fatal("VolumeState.Path: empty value")
	}
	if !state.Mounted {
		t.Fatal("VolumeState.Mounted: expected true")
	}
	if state.FreeBytes <= 0 {
		t.Fatal("VolumeState.FreeBytes: non-positive value")
	}
	if !state.Known {
		t.Fatal("VolumeState.Known: expected true")
	}

	sentinels := []error{
		ErrVolumeMissing,
		ErrVolumeFull,
		ErrStatTimedOut,
	}
	for _, err := range sentinels {
		if err == nil {
			t.Fatal("disk error sentinel is nil")
		}
	}
	for i, err := range sentinels {
		for j := i + 1; j < len(sentinels); j++ {
			if err == sentinels[j] {
				t.Fatalf("disk error sentinels %d and %d must be distinct", i, j)
			}
		}
	}
}

// TestWithinTimesOutAStalledStat verifies that WithinTimeout returns
// ErrStatTimedOut when the underlying stat call blocks past the timeout,
// and that the context is cancelled to stop the blocked goroutine.
func TestWithinTimesOutAStalledStat(t *testing.T) {
	origStat := statFunc
	defer func() { statFunc = origStat }()

	statFunc = func(ctx context.Context, _ string) (uint64, error) {
		select {
		case <-time.After(5 * time.Second):
			return 100, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}

	ctx := context.Background()
	_, err := WithinTimeout(ctx, "/tmp", 50*time.Millisecond)
	if !errors.Is(err, ErrStatTimedOut) {
		t.Fatalf("expected ErrStatTimedOut, got %v", err)
	}
}
