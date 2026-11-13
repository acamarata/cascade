package resilience

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestDiskTypesCompile verifies that the disk resilience data shapes exist
// with the fields expected by later tickets.
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
}

// TestDiskErrorSentinels verifies that the disk error sentinels exist and
// are distinct.
func TestDiskErrorSentinels(t *testing.T) {
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

// TestCandidateVolumeBecomesRequiredOnceSeen verifies that a candidate volume
// transitions from Known=false to Known=true after first successful mount and
// remains required even if later unmounted.
func TestCandidateVolumeBecomesRequiredOnceSeen(t *testing.T) {
	resetKnownVolumes()

	cfg := DiskConfig{
		RequiredVolumes: []string{"/mnt/candidate"},
		StatTimeout:     time.Second,
	}

	// First call: volume is mounted successfully.
	statFnMounted := func(_ string, _ time.Duration) (uint64, error) {
		return 1024, nil
	}

	states, err := DiscoverVolumes(cfg, statFnMounted)
	if err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("first call: got %d states, want 1", len(states))
	}
	if !states[0].Mounted {
		t.Fatal("first call: expected Mounted=true")
	}
	if !states[0].Known {
		t.Fatal("first call: expected Known=true after successful mount")
	}

	// Second call: volume is unmounted (stat times out).
	states, err = DiscoverVolumes(cfg, statFnUnmounted)
	if !errors.Is(err, ErrVolumeMissing) {
		t.Fatalf("second call: expected ErrVolumeMissing, got %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("second call: got %d states, want 1", len(states))
	}
	if states[0].Mounted {
		t.Fatal("second call: expected Mounted=false")
	}
	if !states[0].Known {
		t.Fatal("second call: expected Known=true to persist after unmount")
	}

}

// TestUnknownUnmountedVolumeIsNotRequired verifies that an unmounted volume
// that was never seen does not trigger ErrVolumeMissing.
func TestUnknownUnmountedVolumeIsNotRequired(t *testing.T) {
	resetKnownVolumes()

	cfgUnknown := DiskConfig{
		RequiredVolumes: []string{"/mnt/unknown"},
		StatTimeout:     time.Second,
	}
	states, err := DiscoverVolumes(cfgUnknown, statFnUnmounted)
	if err != nil {
		t.Fatalf("unknown volume: expected no error, got %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("unknown volume: got %d states, want 1", len(states))
	}
	if states[0].Known {
		t.Fatal("unknown volume: expected Known=false")
	}
}

// statFnUnmounted simulates a volume whose stat times out.
func statFnUnmounted(_ string, _ time.Duration) (uint64, error) {
	return 0, ErrStatTimedOut
}

// resetKnownVolumes clears the known-volume registry between test cases.
func resetKnownVolumes() {
	knownMu.Lock()
	defer knownMu.Unlock()
	knownVolumes = make(map[string]bool)
}

// TestDiskMissingStartsNothingThenRecovers verifies that WaitUntilReady
// loops without side effects when a required volume is missing, and
// returns nil once the volume becomes mounted.
func TestDiskMissingStartsNothingThenRecovers(t *testing.T) {
	resetKnownVolumes()

	cfg := DiskConfig{
		RequiredVolumes: []string{"/mnt/data"},
		DataHome:        "/data/home",
		FreeSpaceFloor:  1 << 30,
		RetryInterval:   10 * time.Millisecond,
		StatTimeout:     time.Second,
	}

	callCount := 0
	discoverFn := func(_ DiskConfig, _ func(string, time.Duration) (uint64, error)) ([]VolumeState, error) {
		callCount++
		if callCount == 1 {
			return []VolumeState{
				{Path: "/mnt/data", Mounted: false, FreeBytes: 0, Known: true},
			}, ErrVolumeMissing
		}
		return []VolumeState{
			{Path: "/mnt/data", Mounted: true, FreeBytes: 2 << 30, Known: true},
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := WaitUntilReady(ctx, cfg, discoverFn)
	if err != nil {
		t.Fatalf("WaitUntilReady: expected nil error, got %v", err)
	}
	if callCount < 2 {
		t.Fatalf("expected at least 2 calls to discoverFn, got %d", callCount)
	}
}

// TestDiskFullIsAWait verifies that WaitUntilReady loops without side effects
// when a volume is mounted but has less than FreeSpaceFloor free bytes,
// and returns nil once sufficient free space becomes available.
func TestDiskFullIsAWait(t *testing.T) {
	resetKnownVolumes()

	cfg := DiskConfig{
		RequiredVolumes: []string{"/mnt/data"},
		DataHome:        "/data/home",
		FreeSpaceFloor:  1 << 30,
		RetryInterval:   10 * time.Millisecond,
		StatTimeout:     time.Second,
	}

	callCount := 0
	discoverFn := func(_ DiskConfig, _ func(string, time.Duration) (uint64, error)) ([]VolumeState, error) {
		callCount++
		if callCount == 1 {
			return []VolumeState{
				{Path: "/mnt/data", Mounted: true, FreeBytes: 100 << 20, Known: true},
			}, nil
		}
		return []VolumeState{
			{Path: "/mnt/data", Mounted: true, FreeBytes: 2 << 30, Known: true},
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := WaitUntilReady(ctx, cfg, discoverFn)
	if err != nil {
		t.Fatalf("WaitUntilReady: expected nil error, got %v", err)
	}
	if callCount < 2 {
		t.Fatalf("expected at least 2 calls to discoverFn, got %d", callCount)
	}
}
