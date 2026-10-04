//go:build integration && !windows

// Package fixture (integration_not_windows_test.go) is a no-network gate constraint fixture.
package fixture

import (
	"net"
	"testing"
)

func TestFixture(t *testing.T) { _ = net.IPv4len }
