//go:build integration || linux

// Package fixture (integration_or_linux_test.go) is a no-network gate constraint fixture.
package fixture

import (
	"net"
	"testing"
)

func TestFixture(t *testing.T) { _ = net.IPv4len }
