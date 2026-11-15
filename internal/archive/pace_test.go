package archive

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakePaceBroker is a scripted PaceBroker for tests.
type fakePaceBroker struct {
	cfg PaceConfig
	err error
}

// Pace returns the scripted config and error.
func (f fakePaceBroker) Pace(context.Context) (PaceConfig, error) { return f.cfg, f.err }

// TestSliceBoundIsHonoured checks that a slice never exceeds its share of the hourly budget.
func TestSliceBoundIsHonoured(t *testing.T) {
	cases := []struct {
		name string
		cfg  PaceConfig
		want int64
	}{
		{"idle", PaceConfig{BytesPerHour: 12 * 1024}, 1024},
		{"one copy", PaceConfig{CopyLoad: 1, BytesPerHour: 12 * 1024}, 1024},
		{"busy", PaceConfig{CopyLoad: 4, BytesPerHour: 12 * 1024}, 256},
		{"tiny budget", PaceConfig{CopyLoad: 100, BytesPerHour: 12}, 1},
		{"no budget", PaceConfig{BytesPerHour: 0}, 0},
	}
	for _, tc := range cases {
		got := ComputeSliceBounds(tc.cfg)
		if got.MaxBytes != tc.want {
			t.Errorf("%s: MaxBytes = %d, want %d", tc.name, got.MaxBytes, tc.want)
		}
		if got.MaxDuration != PaceSliceDuration {
			t.Errorf("%s: MaxDuration = %v, want %v", tc.name, got.MaxDuration, PaceSliceDuration)
		}
		perHour := got.MaxBytes * int64(time.Hour/got.MaxDuration)
		if tc.cfg.BytesPerHour > 0 && got.MaxBytes > 1 && perHour > tc.cfg.BytesPerHour {
			t.Errorf("%s: %d bytes/hour exceeds budget %d", tc.name, perHour, tc.cfg.BytesPerHour)
		}
		if got.Waiting() != (tc.want == 0) {
			t.Errorf("%s: Waiting() = %v, want %v", tc.name, got.Waiting(), tc.want == 0)
		}
	}
}

// TestQueryBrokerUnreachable checks the error paths for a missing or failing broker.
func TestQueryBrokerUnreachable(t *testing.T) {
	if _, err := QueryBroker(context.Background()); !errors.Is(err, ErrBrokerUnreachable) {
		t.Fatalf("no broker: err = %v, want ErrBrokerUnreachable", err)
	}
	ctx := WithPaceBroker(context.Background(), fakePaceBroker{err: errors.New("dial refused")})
	if _, err := QueryBroker(ctx); !errors.Is(err, ErrBrokerUnreachable) {
		t.Fatalf("failing broker: err = %v, want ErrBrokerUnreachable", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := QueryBroker(cancelled); err == nil {
		t.Fatal("cancelled context: err = nil, want error")
	}
}

// TestQueryBrokerInvalidData checks that bad broker values are refused.
func TestQueryBrokerInvalidData(t *testing.T) {
	bad := []PaceConfig{
		{CopyLoad: -1, BytesPerHour: 10},
		{BytesPerHour: -5},
		{BytesPerHour: 10, QuietHours: []string{"25:00-06:00"}},
		{BytesPerHour: 10, QuietHours: []string{"22:00"}},
	}
	for i, cfg := range bad {
		ctx := WithPaceBroker(context.Background(), fakePaceBroker{cfg: cfg})
		if _, err := QueryBroker(ctx); !errors.Is(err, ErrBrokerInvalid) {
			t.Errorf("case %d: err = %v, want ErrBrokerInvalid", i, err)
		}
	}
}

// TestQueryBrokerFields checks that broker values reach PaceConfig unchanged.
func TestQueryBrokerFields(t *testing.T) {
	want := PaceConfig{CopyLoad: 3, BytesPerHour: 4096, QuietHours: []string{"22:00-06:00"}}
	ctx := WithPaceBroker(context.Background(), fakePaceBroker{cfg: want})
	got, err := QueryBroker(ctx)
	if err != nil {
		t.Fatalf("QueryBroker: %v", err)
	}
	if got.CopyLoad != want.CopyLoad || got.BytesPerHour != want.BytesPerHour {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if len(got.QuietHours) != 1 || got.QuietHours[0] != "22:00-06:00" {
		t.Fatalf("QuietHours = %v, want [22:00-06:00]", got.QuietHours)
	}
}
