//go:build !windows

// Purpose: the general Client.List dispatch tests, split out of
//   rpc_test.go because their assertion (List actually calls through to
//   the injected RPCCaller) is false on Windows by design: rpc.go's
//   Client.List refuses unconditionally there before ever calling
//   caller.Do (tier-2, no daemon exists to dial), and rpc_test.go's own
//   TestSessionsListClient_WindowsTier2Refusal already proves that
//   refusal for real on the Windows CI lane. These three tests exercise
//   the normal dispatch path a POSIX daemon actually has.

package sessions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestSessionsListClient_HappyPath(t *testing.T) {
	caller := &recordingCaller{}
	client := sessions.NewClient(caller)
	if _, err := client.List(context.Background(), sessions.Filter{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if !caller.called {
		t.Fatal("Client.List never called the RPCCaller")
	}
}

func TestSessionsListClient_TaxonomyErrorPassesThrough(t *testing.T) {
	want := cascade.New(cascade.KindUnavailable, "boom")
	client := sessions.NewClient(&failingCaller{err: want})
	_, err := client.List(context.Background(), sessions.Filter{})
	if !errors.Is(err, want) {
		t.Fatalf("List error = %v, want it to carry KindUnavailable", err)
	}
}

func TestSessionsListClient_PlainErrorWrappedAsInternal(t *testing.T) {
	client := sessions.NewClient(&failingCaller{err: errors.New("transport exploded")})
	_, err := client.List(context.Background(), sessions.Filter{})
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("List error = %v, want KindInternal", err)
	}
}
