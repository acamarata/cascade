//go:build !devkeys

package elevation

import (
	"errors"
	"testing"
)

func TestEnrollNeverFallsBack(t *testing.T) {
	sel, k := custodyFixture(t, CustodyPlatform)
	want := errors.New("storage refused")
	k.generateErr = want
	c, err := sel.Enroll()
	if err != want || c.Tier() != CustodyPlatform {
		t.Fatalf("enroll=%v,%v", c, err)
	}
}
func TestEnrollProtectedSource(t *testing.T) {
	sel, _ := custodyFixture(t, CustodyPresence)
	if c, err := sel.Enroll(); err != nil || c.Tier() != CustodyPresence {
		t.Fatalf("enroll=%v,%v", c, err)
	}
}
func TestDevkeysBuildConstant(t *testing.T) {
	if DevkeysBuild() {
		t.Fatal("default build enabled development keys")
	}
	if CustodyFile.SatisfiesElevation() {
		t.Fatal("default build accepts file custody")
	}
}
