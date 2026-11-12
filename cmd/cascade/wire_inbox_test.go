//go:build !windows

package main

import (
	"slices"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestBuildRPCServerRegistersInbox composes the daemon with the production
// options and checks the three inbox methods are served, each with a
// declared owner-view access class.
func TestBuildRPCServerRegistersInbox(t *testing.T) {
	c := composeForTest(t)
	methods := c.Registry.Methods()
	for _, m := range []string{"inbox.list", "inbox.read", "inbox.ack"} {
		if !slices.Contains(methods, m) {
			t.Errorf("composed registry does not serve %s", m)
			continue
		}
		access, ok := rpc.AccessOf(m)
		if !ok {
			t.Errorf("%s has no declared access class", m)
			continue
		}
		if access != rpc.AccessOwnerView {
			t.Errorf("%s access = %v, want AccessOwnerView", m, access)
		}
	}
}

// TestWireInboxWithoutNotifyIsUnavailable refuses a composition that never
// built the notify runtime instead of serving an inbox over nothing.
func TestWireInboxWithoutNotifyIsUnavailable(t *testing.T) {
	cases := map[string]*daemon.NotifyRuntime{
		"no runtime":  nil,
		"nil service": daemon.NewNotifyRuntime(nil),
	}
	for name, rt := range cases {
		t.Run(name, func(t *testing.T) {
			reg := rpc.NewRegistry()
			w := &daemonWiring{Registry: reg}
			w.Deps.Notify = rt
			err := wireInbox(w)
			if !cascade.HasKind(err, cascade.KindUnavailable) {
				t.Fatalf("wireInbox = %v, want KindUnavailable", err)
			}
			if slices.Contains(reg.Methods(), "inbox.ack") {
				t.Fatal("inbox.ack registered without a notify service")
			}
		})
	}
}
