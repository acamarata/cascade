package docket

import (
	"errors"
	"testing"
)

// TestMessageRowSchemaAcceptsMetadataColumns verifies that every permutation
// of the required metadata columns with a nil row passes schema-only validation.
func TestMessageRowSchemaAcceptsMetadataColumns(t *testing.T) {
	cols := []string{"folder", "state", "time", "label"}
	perms := permutations(cols)
	for _, p := range perms {
		row, err := ValidateMessageRowMetadata(p, nil)
		if err != nil {
			t.Errorf("permutation %v: unexpected error: %v", p, err)
		}
		if row != nil {
			t.Errorf("permutation %v: expected nil row, got %v", p, row)
		}
	}
}

// TestMessageRowSchemaRejectsBodyColumn verifies that columns containing body,
// unknown names, missing names, empty or duplicate names are rejected.
func TestMessageRowSchemaRejectsBodyColumn(t *testing.T) {
	bad := [][]string{
		{"folder", "state", "time", "label", "body"},
		{"body"},
		{"folder", "state", "time", "unknown"},
		{"folder", "state", "time"},
		{"folder", "state"},
		{"folder"},
		{},
		{"folder", "folder", "state", "time"},
		{"", "folder", "state", "time"},
		{"folder", "state", "time", "label", "extra"},
	}
	for _, cols := range bad {
		row, err := ValidateMessageRowMetadata(cols, nil)
		if row != nil {
			t.Errorf("columns %v: expected nil row, got %v", cols, row)
		}
		if !errors.Is(err, ErrMessageMetadata) {
			t.Errorf("columns %v: expected ErrMessageMetadata, got %v", cols, err)
		}
		if err.Error() != "D5903" {
			t.Errorf("columns %v: expected Error() D5903, got %q", cols, err.Error())
		}
	}
}

func permutations(arr []string) [][]string {
	var result [][]string
	var generate func(int)
	generate = func(k int) {
		if k == 1 {
			tmp := make([]string, len(arr))
			copy(tmp, arr)
			result = append(result, tmp)
			return
		}
		for i := 0; i < k; i++ {
			generate(k - 1)
			if k%2 == 0 {
				arr[i], arr[k-1] = arr[k-1], arr[i]
			} else {
				arr[0], arr[k-1] = arr[k-1], arr[0]
			}
		}
	}
	generate(len(arr))
	return result
}
