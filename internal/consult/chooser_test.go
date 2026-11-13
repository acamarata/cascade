package consult

import (
	"errors"
	"testing"
)

func TestChooserUsesMatrixDefault(t *testing.T) {
	c := NewChooser(map[string]string{"critic": "sonnet"}, nil)
	m, err := c.Choose("critic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m != "sonnet" {
		t.Fatalf("expected sonnet, got %s", m)
	}
}

func TestChooserOverrideWins(t *testing.T) {
	c := NewChooser(
		map[string]string{"critic": "sonnet"},
		map[string]string{"critic": "opus"},
	)
	m, err := c.Choose("critic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m != "opus" {
		t.Fatalf("expected opus, got %s", m)
	}
}

func TestChooserOverrideOnlyRole(t *testing.T) {
	c := NewChooser(
		map[string]string{},
		map[string]string{"judge": "opus"},
	)
	m, err := c.Choose("judge")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m != "opus" {
		t.Fatalf("expected opus, got %s", m)
	}
}

func TestChooserUnknownRoleFailsClosed(t *testing.T) {
	c := NewChooser(map[string]string{}, map[string]string{})
	_, err := c.Choose("critic")
	if !errors.Is(err, ErrNoModel) {
		t.Fatalf("expected ErrNoModel, got %v", err)
	}
}
