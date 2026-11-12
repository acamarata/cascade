package mod

import (
	"reflect"
	"testing"
)

func TestApplyNotesShareOffStripsAllCasings(t *testing.T) {
	input := map[string]any{
		"share": 1,
		"Share": 2,
		"SHARE": 3,
	}
	result := ApplyNotesShare(input, false)

	for key := range result {
		if key == "share" || key == "Share" || key == "SHARE" {
			t.Errorf("Expected no share keys, but found %q", key)
		}
	}
}

func TestApplyNotesShareOnWritesOneKey(t *testing.T) {
	input := map[string]any{}
	result := ApplyNotesShare(input, true)

	shareCount := 0
	for key := range result {
		if key == "share" {
			shareCount++
		}
	}

	if shareCount != 1 {
		t.Errorf("Expected exactly one 'share' key, got %d", shareCount)
	}

	if len(result) != 1 {
		t.Errorf("Expected result to have exactly 1 key, got %d", len(result))
	}
}

func TestApplyNotesShareOnCollapsesVariants(t *testing.T) {
	input := map[string]any{
		"Share": 1,
		"SHARE": 2,
	}
	result := ApplyNotesShare(input, true)

	shareCount := 0
	var shareKey string
	for key := range result {
		if key == "share" || key == "Share" || key == "SHARE" {
			shareCount++
			shareKey = key
		}
	}

	if shareCount != 1 {
		t.Errorf("Expected exactly one share key, got %d", shareCount)
	}

	if shareKey != "share" {
		t.Errorf("Expected canonical key to be 'share', got %q", shareKey)
	}
}

func TestApplyNotesShareKeepsOtherKeys(t *testing.T) {
	input := map[string]any{
		"title": "x",
		"Share": 1,
	}
	expected := map[string]any{
		"title": "x",
	}

	result := ApplyNotesShare(input, false)

	if !reflect.DeepEqual(result, expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
}

func TestApplyNotesSharePureFunction(t *testing.T) {
	input := map[string]any{
		"share": 1,
		"Share": 2,
	}
	inputCopy := make(map[string]any)
	for k, v := range input {
		inputCopy[k] = v
	}

	ApplyNotesShare(input, false)

	if !reflect.DeepEqual(input, inputCopy) {
		t.Errorf("Input map was mutated. Expected %v, got %v", inputCopy, input)
	}
}
