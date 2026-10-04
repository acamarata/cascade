//go:build !integration

// Package fixture (negated_integration_test.go) is a no-network gate constraint fixture.
package fixture

import (
	"net"
	"testing"
)

func TestFixture(t *testing.T) { _ = net.IPv4len }
