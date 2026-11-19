package ci

import (
	"errors"
	"testing"
)

// TestMemoKeyDeclarations asserts that the required declarations for the
// gate-memo-key.v1 contract exist and are accessible.
func TestMemoKeyDeclarations(t *testing.T) {
	if ErrNotImplemented == nil {
		t.Fatal("ErrNotImplemented is nil")
	}
	if !errors.Is(ErrNotImplemented, ErrNotImplemented) {
		t.Fatal("ErrNotImplemented identity check failed")
	}

	var key MemoKey
	key.Toolchain = "go1.21"
	if key.Toolchain != "go1.21" {
		t.Fatalf("MemoKey.Toolchain field not accessible or working, got %q", key.Toolchain)
	}
}

// TestMemoKeyNamesNodeToolchain verifies that distinct toolchain strings
// produce distinct keys, and identical toolchains produce identical keys.
func TestMemoKeyNamesNodeToolchain(t *testing.T) {
	key1, err1 := ComputeMemoKey("go1.21", "input1")
	if err1 != nil {
		t.Fatalf("ComputeMemoKey(go1.21) error = %v", err1)
	}

	key2, err2 := ComputeMemoKey("go1.22", "input1")
	if err2 != nil {
		t.Fatalf("ComputeMemoKey(go1.22) error = %v", err2)
	}

	if key1 == key2 {
		t.Fatalf("distinct toolchains produced identical keys: %+v", key1)
	}

	key3, err3 := ComputeMemoKey("go1.21", "input1")
	if err3 != nil {
		t.Fatalf("ComputeMemoKey(go1.21) second call error = %v", err3)
	}

	if key1 != key3 {
		t.Fatalf("identical toolchains produced distinct keys: %+v != %+v", key1, key3)
	}
}
