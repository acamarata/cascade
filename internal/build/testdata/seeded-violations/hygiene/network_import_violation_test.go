// Package violation (network_import_violation_test.go) is a seeded
// violation for the no-network-unit-lane check (Art.7.2): an untagged
// _test.go file importing "net/http" directly.
package violation

import (
	"net/http"
	"testing"
)

func TestReachesOut(t *testing.T) {
	_, _ = http.Get("https://example.invalid")
}
