package redactnames

import (
	"reflect"
	"strings"
	"testing"
)

func TestNamesSortedAndStable(t *testing.T) {
	a := Names()
	b := Names()
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("Names() returned different slices on two calls: %v vs %v", a, b)
	}
	for i := 1; i < len(a); i++ {
		if a[i-1] > a[i] {
			t.Fatalf("Names() is not sorted: %v", a)
		}
	}
}

func TestCheckAllPresentPasses(t *testing.T) {
	if err := Check(Names()); err != nil {
		t.Fatalf("Check(Names()) returned an error: %v", err)
	}
}

func TestCheckMissingNameFails(t *testing.T) {
	names := Names()
	if len(names) == 0 {
		t.Skip("no pinned names to remove")
	}
	missingName := names[0]
	var current []string
	for _, n := range names {
		if n != missingName {
			current = append(current, n)
		}
	}
	err := Check(current)
	if err == nil {
		t.Fatalf("Check with missing name %q returned nil", missingName)
	}
	if !strings.Contains(err.Error(), missingName) {
		t.Fatalf("error %q does not contain missing name %q", err.Error(), missingName)
	}
}

func TestCheckRenamedNameFails(t *testing.T) {
	names := Names()
	if len(names) == 0 {
		t.Skip("no pinned names to rename")
	}
	oldName := names[0]
	newName := "CompletelyDifferentNameThatIsNotInTheList"
	current := make([]string, len(names))
	for i, n := range names {
		if n == oldName {
			current[i] = newName
		} else {
			current[i] = n
		}
	}
	err := Check(current)
	if err == nil {
		t.Fatalf("Check with renamed name %q to %q returned nil", oldName, newName)
	}
	if !strings.Contains(err.Error(), oldName) {
		t.Fatalf("error %q does not contain old name %q", err.Error(), oldName)
	}
}
