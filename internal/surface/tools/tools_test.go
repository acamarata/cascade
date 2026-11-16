package tools

import (
	"errors"
	"testing"
)

func TestCatalogRegisterAndList(t *testing.T) {
	var c Catalog
	list := c.List()
	if list == nil {
		t.Fatal("zero-value catalog returned nil list")
	}
	if len(list) != 0 {
		t.Fatalf("zero-value catalog list has len %d, want 0", len(list))
	}

	if err := c.Register("fmt"); err != nil {
		t.Fatalf("Register(fmt) failed: %v", err)
	}
	if err := c.Register("lint"); err != nil {
		t.Fatalf("Register(lint) failed: %v", err)
	}

	list = c.List()
	if len(list) != 2 || list[0] != "fmt" || list[1] != "lint" {
		t.Fatalf("List() = %v, want [fmt lint]", list)
	}

	// Mutating the returned slice must not affect the catalog.
	list[0] = "mutated"
	list2 := c.List()
	if list2[0] != "fmt" {
		t.Fatalf("mutation affected catalog: %v", list2)
	}

	if err := c.Register("fmt"); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("Register(fmt) second time = %v, want ErrDuplicate", err)
	}

	if err := c.Register(""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Register('') = %v, want ErrInvalidInput", err)
	}

	if err := c.Register(" space "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Register(' space ') = %v, want ErrInvalidInput", err)
	}
}

func TestMCPEntryAddRemove(t *testing.T) {
	var m MCPEntries

	if err := m.Add("server", "s1"); err != nil {
		t.Fatalf("Add(server, s1) = %v", err)
	}
	if err := m.Add("client", "s1"); err != nil {
		t.Fatalf("Add(client, s1) = %v", err)
	}

	list, err := m.List("server")
	if err != nil {
		t.Fatalf("List(server) = %v", err)
	}
	if len(list) != 1 || list[0] != "s1" {
		t.Fatalf("List(server) = %v", list)
	}

	if err := m.Remove("server", "s1"); err != nil {
		t.Fatalf("Remove(server, s1) = %v", err)
	}

	list, err = m.List("server")
	if err != nil {
		t.Fatalf("List(server) after remove = %v", err)
	}
	if list == nil {
		t.Fatal("List(server) after remove is nil")
	}
	if len(list) != 0 {
		t.Fatalf("List(server) after remove = %v, want empty", list)
	}

	list, err = m.List("client")
	if err != nil {
		t.Fatalf("List(client) = %v", err)
	}
	if len(list) != 1 || list[0] != "s1" {
		t.Fatalf("List(client) = %v", list)
	}

	// Mutating the returned slice must not affect the registry.
	list[0] = "mutated"
	list2, _ := m.List("client")
	if list2[0] != "s1" {
		t.Fatalf("mutation affected client list")
	}

	if _, err := m.List("invalid"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("List(invalid) = %v, want ErrInvalidInput", err)
	}

	if err := m.Add("invalid", "x"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Add(invalid, x) = %v", err)
	}

	if err := m.Remove("server", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Remove(missing) = %v, want ErrNotFound", err)
	}
}
