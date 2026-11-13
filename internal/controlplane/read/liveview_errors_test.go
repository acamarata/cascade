package read

import (
	"errors"
	"testing"
	"time"
)

type errFakeStore struct {
	err error
}

func (f *errFakeStore) Ticket(_ string) (TicketRecord, bool, error) {
	return TicketRecord{}, false, f.err
}

type recFakeStore struct {
	rec TicketRecord
}

func (f *recFakeStore) Ticket(_ string) (TicketRecord, bool, error) {
	return f.rec, true, nil
}

type liveViewCase struct {
	name    string
	store   Store
	id      string
	now     time.Time
	wantErr error
}

func liveViewFailCases() []liveViewCase {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	storeErr := errors.New("store failure")

	return []liveViewCase{
		{
			name:    "nil store",
			store:   nil,
			id:      "T1",
			now:     now,
			wantErr: ErrInvalidLiveView,
		},
		{
			name:    "empty ID",
			store:   &errFakeStore{},
			id:      "   ",
			now:     now,
			wantErr: ErrInvalidLiveView,
		},
		{
			name:    "zero now",
			store:   &errFakeStore{},
			id:      "T1",
			now:     time.Time{},
			wantErr: ErrInvalidLiveView,
		},
		{
			name: "future state timestamp",
			store: &recFakeStore{
				rec: TicketRecord{
					StateEnteredAt: now.Add(time.Hour),
				},
			},
			id:      "T1",
			now:     now,
			wantErr: ErrInvalidLiveView,
		},
		{
			name: "negative attempts",
			store: &recFakeStore{
				rec: TicketRecord{
					Attempts: -1,
				},
			},
			id:      "T1",
			now:     now,
			wantErr: ErrInvalidLiveView,
		},
		{
			name:    "unwrapped store error",
			store:   &errFakeStore{err: storeErr},
			id:      "T1",
			now:     now,
			wantErr: storeErr,
		},
		{
			name:    "unknown ticket",
			store:   &errFakeStore{},
			id:      "T1",
			now:     now,
			wantErr: ErrUnknownTicket,
		},
	}
}

func TestLiveViewUnknownTicketFailsClosed(t *testing.T) {
	for _, tt := range liveViewFailCases() {
		t.Run(tt.name, func(t *testing.T) {
			view, err := GetLiveView(tt.store, tt.id, tt.now)
			if view != nil {
				t.Errorf("expected nil view, got %v", view)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("expected error %v, got %v", tt.wantErr, err)
			}
			if tt.wantErr == ErrUnknownTicket {
				if err.Error() != "D4608" {
					t.Errorf("expected exact text 'D4608', got %q", err.Error())
				}
			}
		})
	}
}
