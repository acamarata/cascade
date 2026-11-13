package graph

import "testing"

func TestRegisterSameNameSameKeySameID(t *testing.T) {
	r := NewRegistry()
	name := "mm6"
	key := []byte("key-one")

	first, err := r.Register(name, key)
	if err != nil {
		t.Fatalf("Register(%q, key) returned error: %v", name, err)
	}
	if first == "" {
		t.Fatal("Register returned empty id")
	}

	second, err := r.Register(name, key)
	if err != nil {
		t.Fatalf("second Register(%q, key) returned error: %v", name, err)
	}
	if second == "" {
		t.Fatal("second Register returned empty id")
	}

	if first != second {
		t.Fatalf("same name and key registered to different ids: %q and %q", first, second)
	}

	if got := r.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}
}

func TestRegisterSameNameDifferentKeyDistinct(t *testing.T) {
	r := NewRegistry()
	name := "mm6"
	keyOne := []byte("key-one")
	keyTwo := []byte("key-two")

	first, err := r.Register(name, keyOne)
	if err != nil {
		t.Fatalf("Register(%q, keyOne) returned error: %v", name, err)
	}
	if first == "" {
		t.Fatal("Register returned empty id")
	}

	second, err := r.Register(name, keyTwo)
	if err != nil {
		t.Fatalf("Register(%q, keyTwo) returned error: %v", name, err)
	}
	if second == "" {
		t.Fatal("second Register returned empty id")
	}

	if first == second {
		t.Fatalf("different keys for the same name merged into one id: %q", first)
	}

	if got := r.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}
}

func TestRegisterEmptyKeyFails(t *testing.T) {
	r := NewRegistry()

	if _, err := r.Register("mm6", nil); err == nil {
		t.Fatal("Register with nil key succeeded, want error")
	}
	if _, err := r.Register("mm6", []byte{}); err == nil {
		t.Fatal("Register with empty key succeeded, want error")
	}

	if got := r.Len(); got != 0 {
		t.Fatalf("Len() = %d after failed registrations, want 0", got)
	}
}

func TestRegisterEmptyNameFails(t *testing.T) {
	r := NewRegistry()

	if _, err := r.Register("", []byte("key-one")); err == nil {
		t.Fatal("Register with empty name succeeded, want error")
	}

	if got := r.Len(); got != 0 {
		t.Fatalf("Len() = %d after failed registration, want 0", got)
	}
}
