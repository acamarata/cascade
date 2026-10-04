// A header comment and a blank line may precede the constraint.

//go:build integration

// Package fixture (header_then_integration_test.go) is a no-network gate
// constraint fixture.
package fixture

import (
	"net"
	"testing"
)

func TestFixture(t *testing.T) { _ = net.IPv4len }
