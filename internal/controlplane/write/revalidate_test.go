package write

import (
	"errors"
	"testing"
)

type fakeParker struct {
	calls []Item
	err   error
	errAt int // index at which to return error, -1 for never
}

func (f *fakeParker) Park(item Item) error {
	f.calls = append(f.calls, item)
	if f.errAt >= 0 && len(f.calls)-1 == f.errAt {
		return f.err
	}
	return nil
}

type fakeNotifier struct {
	calls []Item
	err   error
	errAt int
}

func (f *fakeNotifier) Notify(item Item) error {
	f.calls = append(f.calls, item)
	if f.errAt >= 0 && len(f.calls)-1 == f.errAt {
		return f.err
	}
	return nil
}

func TestRevalidateDefaultModeStillParks(t *testing.T) {
	cases := []struct {
		name   string
		mode   string
		caller string
	}{
		{"empty mode owner", "", "owner"},
		{"park mode owner", "park", "owner"},
		{"empty mode guest", "", "guest"},
		{"park mode guest", "park", "guest"},
	}

	items := []Item{{ID: "a"}, {ID: "b"}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeParker{errAt: -1}
			req := Request{
				Mode:     tc.mode,
				Caller:   tc.caller,
				Stale:    items,
				Parker:   p,
				Notifier: nil, // explicitly nil
			}
			res, err := Revalidate(req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Notices != 0 {
				t.Errorf("expected 0 Notices, got %d", res.Notices)
			}
			if res.Parked != 2 {
				t.Errorf("expected 2 Parked, got %d", res.Parked)
			}
			if len(p.calls) != 2 {
				t.Fatalf("expected 2 Park calls, got %d", len(p.calls))
			}
			if p.calls[0].ID != "a" || p.calls[1].ID != "b" {
				t.Errorf("unexpected Park call order: %v", p.calls)
			}
		})
	}
}

func TestRevalidateValidationAndDependencyFailure(t *testing.T) {
	errTest := errors.New("test error")

	cases := []struct {
		name      string
		req       Request
		wantErr   error
		wantPark  int
		wantNotif int
	}{
		{
			name: "unknown mode",
			req: Request{
				Mode:   "unknown",
				Caller: "owner",
				Stale:  []Item{{ID: "a"}},
				Parker: &fakeParker{errAt: -1},
			},
			wantErr: ErrInvalidRevalidate,
		},
		{
			name: "blank ID",
			req: Request{
				Mode:   "park",
				Caller: "owner",
				Stale:  []Item{{ID: ""}},
				Parker: &fakeParker{errAt: -1},
			},
			wantErr: ErrInvalidRevalidate,
		},
		{
			name: "padded ID",
			req: Request{
				Mode:   "park",
				Caller: "owner",
				Stale:  []Item{{ID: " a "}},
				Parker: &fakeParker{errAt: -1},
			},
			wantErr: ErrInvalidRevalidate,
		},
		{
			name: "duplicate ID",
			req: Request{
				Mode:   "park",
				Caller: "owner",
				Stale:  []Item{{ID: "a"}, {ID: "a"}},
				Parker: &fakeParker{errAt: -1},
			},
			wantErr: ErrInvalidRevalidate,
		},
		{
			name: "blank park Caller",
			req: Request{
				Mode:   "park",
				Caller: "  ",
				Stale:  []Item{{ID: "a"}},
				Parker: &fakeParker{errAt: -1},
			},
			wantErr: ErrInvalidRevalidate,
		},
		{
			name: "missing park collaborator",
			req: Request{
				Mode:   "park",
				Caller: "owner",
				Stale:  []Item{{ID: "a"}},
				Parker: nil,
			},
			wantErr: ErrInvalidRevalidate,
		},
		{
			name: "missing notify collaborator",
			req: Request{
				Mode:     "notify_only",
				Caller:   "owner",
				Stale:    []Item{{ID: "a"}},
				Notifier: nil,
			},
			wantErr: ErrInvalidRevalidate,
		},
		{
			name: "non-owner notify_only",
			req: Request{
				Mode:     "notify_only",
				Caller:   "guest",
				Stale:    []Item{{ID: "a"}},
				Notifier: &fakeNotifier{errAt: -1},
			},
			wantErr: ErrInvalidRevalidate,
		},
		{
			name: "nil Stale",
			req: Request{
				Mode:   "park",
				Caller: "owner",
				Stale:  nil,
				Parker: &fakeParker{errAt: -1},
			},
			wantErr: nil,
		},
		{
			name: "empty Stale",
			req: Request{
				Mode:   "park",
				Caller: "owner",
				Stale:  []Item{},
				Parker: &fakeParker{errAt: -1},
			},
			wantErr: nil,
		},
		{
			name: "first park error",
			req: Request{
				Mode:   "park",
				Caller: "owner",
				Stale:  []Item{{ID: "a"}, {ID: "b"}},
				Parker: &fakeParker{err: errTest, errAt: 0},
			},
			wantErr:  errTest,
			wantPark: 0,
		},
		{
			name: "later park error",
			req: Request{
				Mode:   "park",
				Caller: "owner",
				Stale:  []Item{{ID: "a"}, {ID: "b"}, {ID: "c"}},
				Parker: &fakeParker{err: errTest, errAt: 1},
			},
			wantErr:  errTest,
			wantPark: 1,
		},
		{
			name: "first notify error",
			req: Request{
				Mode:     "notify_only",
				Caller:   "owner",
				Stale:    []Item{{ID: "a"}, {ID: "b"}},
				Notifier: &fakeNotifier{err: errTest, errAt: 0},
			},
			wantErr:   errTest,
			wantNotif: 0,
		},
		{
			name: "later notify error",
			req: Request{
				Mode:     "notify_only",
				Caller:   "owner",
				Stale:    []Item{{ID: "a"}, {ID: "b"}, {ID: "c"}},
				Notifier: &fakeNotifier{err: errTest, errAt: 1},
			},
			wantErr:   errTest,
			wantNotif: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Revalidate(tc.req)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("expected error %v, got %v", tc.wantErr, err)
			}
			if err == nil && tc.wantErr == nil {
				if res != (RevalidateResult{}) {
					t.Errorf("expected zero result, got %+v", res)
				}
			} else if err != nil {
				if res.Parked != tc.wantPark {
					t.Errorf("expected %d Parked, got %d", tc.wantPark, res.Parked)
				}
				if res.Notices != tc.wantNotif {
					t.Errorf("expected %d Notices, got %d", tc.wantNotif, res.Notices)
				}
			}
		})
	}
}
