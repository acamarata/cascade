package write

import (
	"errors"
	"testing"
)

type notifyOnlyNotifier struct {
	calls  []Item
	err    error
	failOn int // index to fail on, -1 for no fail
}

func (f *notifyOnlyNotifier) Notify(item Item) error {
	f.calls = append(f.calls, item)
	if f.failOn == len(f.calls)-1 {
		return f.err
	}
	return nil
}

type notifyOnlyParker struct {
	calls []Item
	err   error
}

func (f *notifyOnlyParker) Park(item Item) error {
	f.calls = append(f.calls, item)
	return f.err
}

func TestRevalidateNotifyOnlyOwnerEmitsNoticesNoParks(t *testing.T) {
	n := &notifyOnlyNotifier{}
	p := (*notifyOnlyParker)(nil) // nil Parker is valid

	req := Request{
		Mode:     "notify_only",
		Caller:   "owner",
		Stale:    []Item{{ID: "a"}, {ID: "b"}},
		Parker:   p,
		Notifier: n,
	}

	res, err := Revalidate(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Notices != 2 {
		t.Errorf("expected Notices=2, got %d", res.Notices)
	}
	if res.Parked != 0 {
		t.Errorf("expected Parked=0, got %d", res.Parked)
	}
	if len(n.calls) != 2 {
		t.Fatalf("expected 2 notify calls, got %d", len(n.calls))
	}
	if n.calls[0].ID != "a" || n.calls[1].ID != "b" {
		t.Errorf("expected ordered calls [a,b], got %v", n.calls)
	}
}

func TestRevalidateNotifyOnlyNonOwnerRejected(t *testing.T) {
	cases := []struct {
		caller string
	}{
		{"guest"},
		{"Owner"},
		{""},
	}

	for _, tc := range cases {
		n := &notifyOnlyNotifier{}
		p := (*notifyOnlyParker)(nil)

		req := Request{
			Mode:     "notify_only",
			Caller:   tc.caller,
			Stale:    []Item{{ID: "a"}, {ID: "b"}},
			Parker:   p,
			Notifier: n,
		}

		res, err := Revalidate(req)
		if !errors.Is(err, ErrOwnerOnly) {
			t.Errorf("caller %q: expected ErrOwnerOnly, got %v", tc.caller, err)
		}
		if err != nil && err.Error() != "owner only" {
			t.Errorf("caller %q: expected error text 'owner only', got %q", tc.caller, err.Error())
		}
		if res.Notices != 0 || res.Parked != 0 {
			t.Errorf("caller %q: expected zero result, got %+v", tc.caller, res)
		}
		if len(n.calls) != 0 {
			t.Errorf("caller %q: expected 0 notify calls, got %d", tc.caller, len(n.calls))
		}
	}
}

func TestRevalidateNotifyOnlyNonOwnerEmitsNothing(t *testing.T) {
	// Rejected callers produce zero notifier and parker calls even with other invalid fields.
	n := &notifyOnlyNotifier{}
	p := &notifyOnlyParker{}

	req := Request{
		Mode:     "notify_only",
		Caller:   "guest",
		Stale:    []Item{{ID: " invalid "}, {ID: "a"}, {ID: "a"}}, // Invalid fields
		Parker:   p,
		Notifier: n,
	}

	res, err := Revalidate(req)
	if !errors.Is(err, ErrOwnerOnly) {
		t.Errorf("expected ErrOwnerOnly, got %v", err)
	}
	if res.Notices != 0 || res.Parked != 0 {
		t.Errorf("expected zero result, got %+v", res)
	}
	if len(n.calls) != 0 {
		t.Errorf("expected 0 notify calls, got %d", len(n.calls))
	}
	if len(p.calls) != 0 {
		t.Errorf("expected 0 park calls, got %d", len(p.calls))
	}
}
