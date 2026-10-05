//go:build windows

package elevation

import "testing"

func TestWindowsCustodyIsNone(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(name, dir)
	}
	if c := SelectCustody(dir); c.Tier() != CustodyNone || c.Reason() != "windows-tier2" {
		t.Fatalf("custody=%+v", c)
	}
}
