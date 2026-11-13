package parity

import (
	"reflect"
	"testing"
)

func TestCompareInventoryMissingTable(t *testing.T) {
	old := []Entry{{Table: "users", Rows: 10}}
	now := []Entry{}
	got := CompareInventory(old, now)
	want := []Diff{{Table: "users", Old: 10, Now: 0}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CompareInventory() = %v, want %v", got, want)
	}
}

func TestCompareInventoryShrunk(t *testing.T) {
	old := []Entry{{Table: "users", Rows: 10}}
	now := []Entry{{Table: "users", Rows: 8}}
	got := CompareInventory(old, now)
	want := []Diff{{Table: "users", Old: 10, Now: 8}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CompareInventory() = %v, want %v", got, want)
	}
}

func TestCompareInventoryGrowthOK(t *testing.T) {
	old := []Entry{{Table: "users", Rows: 10}}
	now := []Entry{{Table: "users", Rows: 12}}
	got := CompareInventory(old, now)
	if len(got) != 0 {
		t.Errorf("CompareInventory() = %v, want empty", got)
	}
}
