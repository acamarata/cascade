// Package testfile is a legitimate-use fixture proving the forbidigo rules
// this ticket adds do not fire inside _test.go files (Art.7.3's clock/rand
// discipline is enforced on domain logic; tests are explicitly exempted by
// .golangci.yml's exclusions.rules path on "_test\\.go$"). See
// badtime/violation.go for the fixture-data discipline this mirrors.
package testfile

import (
	"math/rand"
	"testing"
	"time"
)

func TestBareTimeAndRandAreFineHere(t *testing.T) {
	_ = time.Now()
	_ = time.Since(time.Now())
	_ = rand.Intn(6)
}
