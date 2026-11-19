package consult

import (
	"errors"
	"math"
	"testing"
)

func TestFilterAdditionsNegativeBudgetFailsClosed(t *testing.T) {
	// Negative budget returns ErrInvalidAdditionBudget before any other validation.
	adds := []Addition{{Text: "valid", Tokens: 1, Relevance: 1.0}}
	out, err := FilterAdditions(adds, -1, 0.5)
	if out != nil {
		t.Errorf("expected nil output for negative budget, got %v", out)
	}
	if !errors.Is(err, ErrInvalidAdditionBudget) {
		t.Errorf("expected ErrInvalidAdditionBudget, got %v", err)
	}

	// Valid budget plus invalid threshold returns ErrInvalidRelevance before invalid entries.
	badAdds := []Addition{{Text: "", Tokens: 1, Relevance: 1.0}}
	out2, err2 := FilterAdditions(badAdds, 10, 1.5)
	if out2 != nil {
		t.Errorf("expected nil output for invalid threshold, got %v", out2)
	}
	if !errors.Is(err2, ErrInvalidRelevance) {
		t.Errorf("expected ErrInvalidRelevance, got %v", err2)
	}

	// NaN threshold
	out3, err3 := FilterAdditions(badAdds, 10, math.NaN())
	if out3 != nil {
		t.Errorf("expected nil output for NaN threshold, got %v", out3)
	}
	if !errors.Is(err3, ErrInvalidRelevance) {
		t.Errorf("expected ErrInvalidRelevance, got %v", err3)
	}

	// Zero budget, zero tokens, zero relevance is valid
	validAdds := []Addition{{Text: "zero", Tokens: 0, Relevance: 0.0}}
	out4, err4 := FilterAdditions(validAdds, 0, 0.0)
	if err4 != nil {
		t.Errorf("expected no error for zero valid additions, got %v", err4)
	}
	if len(out4) != 1 {
		t.Errorf("expected 1 result, got %d", len(out4))
	}

	// Nil/empty additions returns nil, nil after budget/threshold validation
	out5, err5 := FilterAdditions(nil, 10, 0.5)
	if out5 != nil {
		t.Errorf("expected nil output for nil additions, got %v", out5)
	}
	if err5 != nil {
		t.Errorf("expected no error for nil additions, got %v", err5)
	}
}
