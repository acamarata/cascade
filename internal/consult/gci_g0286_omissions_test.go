package consult

import "testing"

func TestGCIBlockOmissionsOutline(t *testing.T) {
	// S1/body s and omissions O1 yields "# S1\ns\n\n# Omissions\nO1"
	sections := []GCISection{{Name: "S1", Body: "s"}}
	got, err := AssembleGCIBlock(sections, "O1")
	if err != nil {
		t.Fatalf("AssembleGCIBlock returned error: %v", err)
	}
	want := "# S1\ns\n\n# Omissions\nO1"
	if got != want {
		t.Errorf("with omissions: got %q, want %q", got, want)
	}

	// empty omissions adds nothing
	got, err = AssembleGCIBlock(sections, "")
	if err != nil {
		t.Fatalf("AssembleGCIBlock returned error: %v", err)
	}
	want = "# S1\ns"
	if got != want {
		t.Errorf("without omissions: got %q, want %q", got, want)
	}

	// nil/empty sections with empty omissions yields "", nil
	got, err = AssembleGCIBlock(nil, "")
	if err != nil {
		t.Fatalf("AssembleGCIBlock returned error: %v", err)
	}
	if got != "" {
		t.Errorf("nil sections: got %q, want %q", got, "")
	}
	got, err = AssembleGCIBlock([]GCISection{}, "")
	if err != nil {
		t.Fatalf("AssembleGCIBlock returned error: %v", err)
	}
	if got != "" {
		t.Errorf("empty sections: got %q, want %q", got, "")
	}
}
