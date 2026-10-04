//go:build (linux && windows) || (!linux && integration)

// Package fixture (short_circuit_test.go) is a no-network gate constraint
// fixture: it builds without the integration tag when linux and windows
// are both set, a tag an all-false evaluation never visits.
package fixture

import (
	"net"
	"testing"
)

func TestFixture(t *testing.T) { _ = net.IPv4len }
