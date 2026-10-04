//go:build t1 || t2 || t3 || t4 || t5 || t6 || t7 || t8 || t9 || t10 || t11 || t12 || t13 || t14 || t15 || t16 || t17 || integration

// Package fixture (many_tags_test.go) is a no-network gate constraint
// fixture: too many tags to enumerate, so the gate treats it as untagged.
package fixture

import (
	"net"
	"testing"
)

func TestFixture(t *testing.T) { _ = net.IPv4len }
