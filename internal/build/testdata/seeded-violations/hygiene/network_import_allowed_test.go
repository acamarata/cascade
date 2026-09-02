//go:build integration

// Package violation (network_import_allowed_test.go) proves the escape:
// the SAME "net/http" import, tagged integration, must produce zero
// findings.
package violation

import (
	"net/http"
	"testing"
)

func TestReachesOutTaggedIntegration(t *testing.T) {
	_, _ = http.Get("https://example.invalid")
}
