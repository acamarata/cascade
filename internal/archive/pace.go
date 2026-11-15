package archive

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PaceSliceDuration is the fixed wall-clock length of one copy slice.
const PaceSliceDuration = 5 * time.Minute

// ErrBrokerUnreachable reports that no broker answered the pace query.
var ErrBrokerUnreachable = errors.New("archive: pace broker unreachable")

// ErrBrokerInvalid reports that the broker answered with values that cannot pace a copy.
var ErrBrokerInvalid = errors.New("archive: pace broker returned invalid data")

// PaceConfig is the broker's current pacing answer for archive copies.
type PaceConfig struct {
	// CopyLoad is the number of copies the broker counts as running now.
	CopyLoad int64
	// BytesPerHour is the copy budget shared by all running copies.
	BytesPerHour int64
	// QuietHours lists local "HH:MM-HH:MM" windows in which copies hold.
	QuietHours []string
}

// SliceBounds caps the work of one copy slice.
type SliceBounds struct {
	// MaxBytes is the most a slice may copy; zero means the slice waits.
	MaxBytes int64
	// MaxDuration is the longest a slice may run.
	MaxDuration time.Duration
}

// Waiting reports whether the slice has no budget and must wait rather than fail.
func (b SliceBounds) Waiting() bool { return b.MaxBytes <= 0 }

// PaceBroker is the broker seam QueryBroker reads; it is the only load reader for archive pacing.
type PaceBroker interface {
	// Pace returns the broker's current copy load, budget and quiet hours.
	Pace(ctx context.Context) (PaceConfig, error)
}

type paceBrokerKey struct{}

// WithPaceBroker returns a context that carries b for QueryBroker.
func WithPaceBroker(ctx context.Context, b PaceBroker) context.Context {
	return context.WithValue(ctx, paceBrokerKey{}, b)
}

// QueryBroker asks the context's PaceBroker for the current pacing config.
// It returns ErrBrokerUnreachable when no broker is set or the broker fails,
// and ErrBrokerInvalid when the answer cannot pace a copy.
func QueryBroker(ctx context.Context) (PaceConfig, error) {
	if err := ctx.Err(); err != nil {
		return PaceConfig{}, err
	}
	b, ok := ctx.Value(paceBrokerKey{}).(PaceBroker)
	if !ok || b == nil {
		return PaceConfig{}, ErrBrokerUnreachable
	}
	cfg, err := b.Pace(ctx)
	if err != nil {
		return PaceConfig{}, fmt.Errorf("%w: %w", ErrBrokerUnreachable, err)
	}
	if err := validatePace(cfg); err != nil {
		return PaceConfig{}, err
	}
	cfg.QuietHours = append([]string(nil), cfg.QuietHours...)
	return cfg, nil
}

// validatePace refuses negative counts and malformed quiet-hour windows.
func validatePace(cfg PaceConfig) error {
	if cfg.CopyLoad < 0 {
		return fmt.Errorf("%w: copy load %d", ErrBrokerInvalid, cfg.CopyLoad)
	}
	if cfg.BytesPerHour < 0 {
		return fmt.Errorf("%w: bytes per hour %d", ErrBrokerInvalid, cfg.BytesPerHour)
	}
	for _, w := range cfg.QuietHours {
		if !validQuietWindow(w) {
			return fmt.Errorf("%w: quiet hours %q", ErrBrokerInvalid, w)
		}
	}
	return nil
}

// validQuietWindow reports whether w has the form "HH:MM-HH:MM".
func validQuietWindow(w string) bool {
	start, end, ok := strings.Cut(w, "-")
	if !ok {
		return false
	}
	for _, s := range []string{start, end} {
		if _, err := time.Parse("15:04", s); err != nil {
			return false
		}
	}
	return true
}

// ComputeSliceBounds derives one slice's limits from the hourly budget.
// The budget for one PaceSliceDuration is split across the running copies;
// a busy broker shrinks the slice to at least one byte so the copy slows and
// never fails. A zero budget yields a waiting slice.
func ComputeSliceBounds(cfg PaceConfig) SliceBounds {
	bounds := SliceBounds{MaxDuration: PaceSliceDuration}
	if cfg.BytesPerHour <= 0 {
		return bounds
	}
	slices := int64(time.Hour / PaceSliceDuration)
	share := cfg.BytesPerHour / slices
	if cfg.CopyLoad > 1 {
		share /= cfg.CopyLoad
	}
	if share < 1 {
		share = 1
	}
	bounds.MaxBytes = share
	return bounds
}
