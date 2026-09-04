package memory

// Purpose: the destructive half of the memory.* handler tests —
//   neighbour safety, the part-way failure that must still count as a
//   deletion, the dry-run rehearsal, and every refusal. Split out of
//   rpc_test.go for the 300-line file cap.
// SPORT: internal.memory.rpc.Handler (ADD, P1-E07-W2-S13-T3).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRPCForgetLeavesNeighboursAlone is the neighbour-safety proof: the
// forgotten address disappears and every other record survives byte for
// byte, including its lexical neighbours and the other kinds.
func TestRPCForgetLeavesNeighboursAlone(t *testing.T) {
	h, store, base := newHandler(t)
	for _, name := range []string{"a-one", "b-two", "c-three"} {
		seed(t, store, "project", name, "", "body of "+name)
	}
	seed(t, store, "user", "b-two", "", "a different record with the same name")

	res := call[ForgetResult](t, h.Forget, ForgetParams{ID: "project/b-two"})
	if !res.Forgotten || res.DryRun {
		t.Fatalf("ForgetResult = %+v, want a real, completed forget", res)
	}

	listed := addressesOf(call[ListResult](t, h.List, ListParams{}).Units)
	want := "project/a-one,project/c-three,user/b-two"
	if strings.Join(listed, ",") != want {
		t.Fatalf("after forget list = %v, want %v", listed, want)
	}
	// The tombstone is what makes the deletion durable; assert it is there
	// rather than assuming the store wrote one.
	if _, err := os.Stat(filepath.Join(base, "project", "b-two.md.tombstone")); err != nil {
		t.Errorf("no tombstone beside the forgotten record: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "project", "b-two.md")); !os.IsNotExist(err) {
		t.Errorf("the forgotten record's file is still present: %v", err)
	}
}

// TestRPCForgetPartWayFailureStaysForgotten drives the failure that
// matters: the tombstone lands and the removal then fails. The call must
// report the failure AND the deletion must still be in force, because a
// forget that half-succeeded and reappeared would be the worst outcome
// available.
func TestRPCForgetPartWayFailureStaysForgotten(t *testing.T) {
	sys := newFailingFS()
	store := newFileStoreWithFS(t.TempDir(), newTestClock(), sys)
	h := NewHandler(store, newTestClock())
	ctx := context.Background()

	entry := validEntry()
	entry.Name = "doomed"
	if err := store.Write(ctx, entry); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	sys.failRemove = true

	err := callErr(t, h.Forget, ForgetParams{ID: Address(entry.Kind, entry.Name)})
	if kind, _ := cascade.KindOf(err); kind != cascade.KindUnavailable {
		t.Fatalf("kind = %v, want %v", kind, cascade.KindUnavailable)
	}
	if !errors.Is(err, ErrStoreIO) {
		t.Errorf("err = %v, want it to wrap ErrStoreIO", err)
	}
	sys.failRemove = false
	if got := call[ListResult](t, h.List, ListParams{}); len(got.Units) != 0 {
		t.Errorf("the record came back after a part-way forget: %v", addressesOf(got.Units))
	}
	if _, err := store.Read(ctx, entry.Kind, entry.Name); !errors.Is(err, ErrNoSuchEntry) {
		t.Errorf("Read after a part-way forget = %v, want ErrNoSuchEntry", err)
	}
}

func TestRPCForgetDryRunRemovesNothing(t *testing.T) {
	h, store, _ := newHandler(t)
	seed(t, store, "project", "kept", "", "body")

	res := call[ForgetResult](t, h.Forget, ForgetParams{ID: "project/kept", DryRun: true})
	if res.Forgotten || !res.DryRun {
		t.Fatalf("ForgetResult = %+v, want a rehearsal that removed nothing", res)
	}
	if got := call[ListResult](t, h.List, ListParams{}); len(got.Units) != 1 {
		t.Fatalf("a dry run removed the record: %v", addressesOf(got.Units))
	}
	if err := callErr(t, h.Forget, ForgetParams{ID: "project/absent", DryRun: true}); !errors.Is(err, ErrNoSuchEntry) {
		t.Errorf("dry run on an absent address = %v, want ErrNoSuchEntry", err)
	}
}

func TestRPCForgetRefusals(t *testing.T) {
	h, _, _ := newHandler(t)
	cases := []struct {
		name string
		id   string
		want cascade.Kind
	}{
		{"no separator", "notanaddress", cascade.KindInvalidInput},
		{"unknown kind", "bogus/name", cascade.KindInvalidInput},
		{"illegal name", "project/../escape", cascade.KindInvalidInput},
		{"absent record", "project/absent", cascade.KindNotFound},
		{"empty", "", cascade.KindInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := callErr(t, h.Forget, ForgetParams{ID: tc.id})
			if kind, _ := cascade.KindOf(err); kind != tc.want {
				t.Errorf("kind = %v, want %v (err %v)", kind, tc.want, err)
			}
		})
	}
}
