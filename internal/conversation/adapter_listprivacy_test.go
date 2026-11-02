package conversation

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestHandleListThreads_ExcludesLocalOnly covers order and principal scope.
func TestHandleListThreads_ExcludesLocalOnly(t *testing.T) {
	store := newTestStore(t)
	a := NewAdapter(store, nil, nil, newTestClock(), "")
	for i, tier := range []provider.SensitivityTier{provider.SensitivityLocalOnly,
		provider.SensitivityRestricted, provider.SensitivityInternal, provider.SensitivityPublic} {
		id := []string{"local", "restricted", "internal", "public"}[i]
		if err := store.CreateThread(context.Background(), id, tier, "project:a", int64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	seedOwnedThread(t, store, "foreign", provider.SensitivityPublic)
	all, err := store.ListThreads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, ctx := range localOnlyCallers() {
		t.Run(name, func(t *testing.T) {
			res, err := a.scopedListThreads(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{}
			for _, thread := range all {
				if thread.ID == "local" && name != "owner" || thread.ID == "foreign" && stringsPrincipal(name) {
					continue
				}
				want = append(want, thread.ID)
			}
			if got := ids(res.(listThreadsResult).Threads); !reflect.DeepEqual(got, want) {
				t.Fatalf("list = %v, want %v", got, want)
			}
		})
	}
}

func stringsPrincipal(name string) bool { return name == "principal" || name == "principal-marker" }

type privacyReadFailure struct {
	Store
	err error
}

func (s privacyReadFailure) ThreadPrivacy(ctx context.Context, id string) (provider.SensitivityTier, error) {
	if id == "broken" {
		return 0, s.err
	}
	return s.Store.ThreadPrivacy(ctx, id)
}

// TestHandleListThreads_PropagatesPrivacyReadError rejects partial results.
func TestHandleListThreads_PropagatesPrivacyReadError(t *testing.T) {
	store := newTestStore(t)
	seedOwnedThread(t, store, "visible", provider.SensitivityPublic)
	seedOwnedThread(t, store, "broken", provider.SensitivityPublic)
	disk := errors.New("privacy disk failure")
	a := NewAdapter(privacyReadFailure{store, disk}, nil, nil, newTestClock(), "")
	for _, ctx := range []context.Context{context.Background(), ownerCLICtx()} {
		got, err := a.filterListableThreads(ctx, []Thread{{ID: "visible"}, {ID: "broken"}})
		if got != nil || !cascade.HasKind(err, cascade.KindUnavailable) || !errors.Is(err, disk) {
			t.Fatalf("filter = %v, %v; want no partial list and unavailable wrapping disk", got, err)
		}
		res, err := a.handleListThreads(ctx, nil)
		if res != nil || !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("handler = %v, %v; want no result and unavailable", res, err)
		}
	}
}

// TestHandleListThreads_FilterCannotBeBypassed binds the filter to the handler.
func TestHandleListThreads_FilterCannotBeBypassed(t *testing.T) {
	store := newTestStore(t)
	seedOwnedThread(t, store, "local", provider.SensitivityLocalOnly)
	seedOwnedThread(t, store, "public", provider.SensitivityPublic)
	a := NewAdapter(store, nil, nil, newTestClock(), "")
	res, err := a.handleListThreads(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res.(listThreadsResult).Threads); !reflect.DeepEqual(got, []string{"public"}) {
		t.Fatalf("list = %v, want [public]", got)
	}
	_, err = a.handleListThreads(context.Background(), []byte(`{"include_local_only":true}`))
	wantSentinel(t, err, ErrMalformedTurnPayload)
}

func ids(threads []Thread) []string {
	out := make([]string, 0, len(threads))
	for _, thread := range threads {
		out = append(out, thread.ID)
	}
	return out
}
