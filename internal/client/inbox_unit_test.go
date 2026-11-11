package client

// Purpose: the untagged half of the inbox client tests: every inbox method
// reaches the transport and reports a missing daemon as KindUnavailable.
// The full round trip through a registry is inbox_integration_test.go.

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestClientInboxMethodsSurfaceMissingDaemon drives each inbox method against
// a socket that does not exist.
func TestClientInboxMethodsSurfaceMissingDaemon(t *testing.T) {
	c := New(missingSocket(t), UnixDialer, 2*time.Second)
	ctx := context.Background()
	calls := map[string]func() error{
		"inbox.list": func() error { _, err := c.InboxList(ctx, InboxListParams{All: true}); return err },
		"inbox.read": func() error { _, err := c.InboxRead(ctx, "notice", 0); return err },
		"inbox.ack":  func() error { _, err := c.InboxAck(ctx, "notice"); return err },
	}
	for verb, call := range calls {
		if err := call(); !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Errorf("%s: err = %v, want KindUnavailable", verb, err)
		}
	}
}
