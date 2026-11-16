package harx

import (
	"context"
	"errors"
	"math"
	"testing"
)

type fakeSource struct {
	events []Event
}

func (f *fakeSource) Poll(after int64) ([]Event, error) {
	var res []Event
	for _, e := range f.events {
		if e.ID > after {
			res = append(res, e)
		}
	}
	return res, nil
}

func (f *fakeSource) Stream(ctx context.Context, sink func(Event)) error {
	for _, e := range f.events {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		sink(e)
	}
	return nil
}

func TestWatchBadAfterRejected(t *testing.T) {
	// Ensure ParseAfter rejects invalid cursors before any source/sink call
	// would be made.
	badCases := []struct {
		name string
		in   string
	}{
		{"negative", "-1"},
		{"positive sign", "+1"},
		{"leading whitespace", " 1"},
		{"trailing whitespace", "1 "},
		{"tab", "1\t"},
		{"newline", "1\n"},
		{"non-ascii digit", "\u0967"},
		{"letter", "1a"},
		{"decimal point", "1.0"},
		{"overflow", "9223372036854775808"},
		{"far overflow", "999999999999999999999999999999"},
	}

	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAfter(tc.in)
			if err == nil {
				t.Fatalf("ParseAfter(%q) = nil error, want ErrWatchBadAfter", tc.in)
			}
			if !errors.Is(err, ErrWatchBadAfter) {
				t.Fatalf("ParseAfter(%q) error = %v, want ErrWatchBadAfter", tc.in, err)
			}
			if err.Error() != "WATCH_BAD_AFTER" {
				t.Fatalf("ParseAfter(%q) error message = %q, want %q", tc.in, err.Error(), "WATCH_BAD_AFTER")
			}
		})
	}

	validCases := []struct {
		name string
		in   string
		want int64
	}{
		{"empty", "", 0},
		{"zero", "0", 0},
		{"leading zero", "01", 1},
		{"max int64", "9223372036854775807", math.MaxInt64},
	}
	for _, tc := range validCases {
		t.Run("valid_"+tc.name, func(t *testing.T) {
			got, err := ParseAfter(tc.in)
			if err != nil {
				t.Fatalf("ParseAfter(%q) = %v, want nil", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ParseAfter(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
