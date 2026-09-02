package deadpkg

import "testing"

func TestSomethingUnrelated(t *testing.T) {
	if UsedFunc() != 2 {
		t.Fatal("unexpected")
	}
}
