package consult

import (
	"strings"
	"testing"
)

const sampleGCI = `# System Instructions

<!-- binding:finance -->
Handle financial queries with care.
<!-- /binding -->

<!-- binding:health -->
Do not provide medical advice.
<!-- /binding -->

Other global instructions.
`

func TestLoadScopedSections(t *testing.T) {
	msg := "Please help me with @finance"
	res, err := LoadScopedSections(sampleGCI, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res, "Handle financial queries") {
		t.Errorf("expected result to contain finance section, got %q", res)
	}
	if strings.Contains(res, "Do not provide medical") {
		t.Errorf("result should not contain health section")
	}
	if strings.Contains(res, "Other global instructions") {
		t.Errorf("result should not contain global instructions")
	}
}

func TestPerMessageRecall(t *testing.T) {
	msg1 := "I need @finance help"
	res1, err := LoadScopedSections(sampleGCI, msg1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msg2 := "I have a @health question"
	res2, err := LoadScopedSections(sampleGCI, msg2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(res1, "health") {
		t.Errorf("res1 should not contain health")
	}
	if strings.Contains(res2, "finance") {
		t.Errorf("res2 should not contain finance")
	}
}

func TestNeverLoadsWholeGCI(t *testing.T) {
	msg := "Tell me about @finance"
	res, err := LoadScopedSections(sampleGCI, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res) >= len(sampleGCI) {
		t.Errorf("expected scoped result to be shorter than full GCI")
	}
	if strings.Contains(res, "# System Instructions") {
		t.Errorf("result should not contain whole GCI")
	}
}

func TestMissingBindingSectionFailsClosed(t *testing.T) {
	msg := "What about @unknown_section?"
	_, err := LoadScopedSections(sampleGCI, msg)
	if err == nil {
		t.Fatalf("expected error for unknown section")
	}
	if !strings.Contains(err.Error(), "M1010") {
		t.Errorf("expected error code M1010, got %v", err)
	}
	if !strings.Contains(err.Error(), "unknown_section") {
		t.Errorf("expected error to mention unknown_section, got %v", err)
	}
}
