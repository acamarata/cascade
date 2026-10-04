/*
//go:build integration
*/

// Package fixture (block_commented_gobuild_test.go) is a no-network gate
// constraint fixture: a //go:build inside a block comment is not a constraint.
package fixture

import (
	"net"
	"testing"
)

func TestFixture(t *testing.T) { _ = net.IPv4len }
