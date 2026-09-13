// Purpose: unit coverage for cascadepa_soul_wiring.go's adapter, isolated
//
//	from the real home directory (Art.7.1: a fake pathResolver stands in
//	for runtime.NewDefaultPathProvider) and, per internal/build's
//	no-network-unit-lane gate (Art.7.2), never naming the "net" package
//	itself -- the real client.UnixDialer (production code) is used
//	against a socket path nothing listens on, matching
//	cascadepa_wiring_test.go's own pattern for the identical class of
//	call site.
//
// SPORT: internal/plugins:cascadepa-soul-wiring (TEST) -- P1-E22-W5-S47-T3.
package plugins

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/runtime"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

func TestCascadePASoulClient_Show_PathResolutionFailure(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	c := newCascadePASoulClient(nil, time.Second, func() (runtime.PathProvider, error) {
		return nil, wantErr
	})
	_, err := c.Show(context.Background())
	if err == nil {
		t.Fatal("Show: err = nil, want a path-resolution error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("Show: err = %v, want it to wrap %v", err, wantErr)
	}
}

func TestCascadePASoulClient_Show_TransportUnreachable(t *testing.T) {
	socket := missingSocketPath(t)
	c := newCascadePASoulClient(client.UnixDialer, time.Second, func() (runtime.PathProvider, error) {
		return fakePathProvider{socket: socket}, nil
	})
	view, err := c.Show(context.Background())
	if err == nil {
		t.Fatal("Show: err = nil, want a transport error")
	}
	if view != (cascadepa.SoulChatView{}) {
		t.Errorf("Show: view = %+v on error, want the zero value", view)
	}
	if got := err.Error(); !containsAll(got, "daemon not running or unreachable") {
		t.Errorf("Show: err = %q, want internal/client's own classified transport error", got)
	}
}

func TestCascadePASoulClient_Edit_PathResolutionFailure(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	c := newCascadePASoulClient(nil, time.Second, func() (runtime.PathProvider, error) {
		return nil, wantErr
	})
	_, err := c.Edit(context.Background(), cascadepa.SoulChatDocument{Body: "x"})
	if err == nil {
		t.Fatal("Edit: err = nil, want a path-resolution error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("Edit: err = %v, want it to wrap %v", err, wantErr)
	}
}

func TestCascadePASoulClient_Edit_TransportUnreachable(t *testing.T) {
	socket := missingSocketPath(t)
	c := newCascadePASoulClient(client.UnixDialer, time.Second, func() (runtime.PathProvider, error) {
		return fakePathProvider{socket: socket}, nil
	})
	res, err := c.Edit(context.Background(), cascadepa.SoulChatDocument{Body: "x", Schema: "s"})
	if err == nil {
		t.Fatal("Edit: err = nil, want a transport error")
	}
	if res != (cascadepa.SoulChatEditResult{}) {
		t.Errorf("Edit: res = %+v on error, want the zero value", res)
	}
	if got := err.Error(); !containsAll(got, "daemon not running or unreachable") {
		t.Errorf("Edit: err = %q, want internal/client's own classified transport error", got)
	}
}
