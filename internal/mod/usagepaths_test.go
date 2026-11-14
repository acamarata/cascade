package mod

import (
	"errors"
	"reflect"
	"testing"
)

func TestRegistryPathsForReturnsOwnPaths(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("a", []string{"x"}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	paths, err := r.PathsFor("a")
	if err != nil {
		t.Fatalf("PathsFor failed: %v", err)
	}
	if !reflect.DeepEqual(paths, []string{"x"}) {
		t.Fatalf("expected [x], got %v", paths)
	}
}

func TestRegistryNoCrossContamination(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("a", []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("b", []string{"y"}); err != nil {
		t.Fatal(err)
	}
	paths, err := r.PathsFor("b")
	if err != nil {
		t.Fatalf("PathsFor failed: %v", err)
	}
	if !reflect.DeepEqual(paths, []string{"y"}) {
		t.Fatalf("expected [y], got %v", paths)
	}
	pathsA, _ := r.PathsFor("a")
	if !reflect.DeepEqual(pathsA, []string{"x"}) {
		t.Fatalf("expected [x], got %v", pathsA)
	}
}

func TestRegistryUnknownModuleFails(t *testing.T) {
	r := NewRegistry()
	_, err := r.PathsFor("zzz")
	if !errors.Is(err, ErrUnknownModule) {
		t.Fatalf("expected ErrUnknownModule, got %v", err)
	}
}

func TestRegistryDuplicateRegisterFails(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("a", []string{"x"}); err != nil {
		t.Fatal(err)
	}
	err := r.Register("a", []string{"y"})
	if !errors.Is(err, ErrDuplicateModule) {
		t.Fatalf("expected ErrDuplicateModule, got %v", err)
	}
}

func TestRegistryResolveRejectsForeignPath(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("a", []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("b", []string{"y"}); err != nil {
		t.Fatal(err)
	}
	ok, err := r.Resolve("b", "x")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if ok {
		t.Fatalf("expected false for foreign path, got true")
	}
}
