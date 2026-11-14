package consult

import "testing"

// TestValidateCheckClass verifies that check and read-long return exact Class, ReadOnly=true, and RequiresVerify=true.
func TestValidateCheckClass(t *testing.T) {
	cases := []string{"check", "read-long"}
	for _, tc := range cases {
		c, err := ValidateWorkClass(tc)
		if err != nil {
			t.Errorf("ValidateWorkClass(%q) returned error: %v", tc, err)
			continue
		}
		if c.Class != tc {
			t.Errorf("ValidateWorkClass(%q).Class = %q, want %q", tc, c.Class, tc)
		}
		if !c.ReadOnly {
			t.Errorf("ValidateWorkClass(%q).ReadOnly = false, want true", tc)
		}
		if !c.RequiresVerify {
			t.Errorf("ValidateWorkClass(%q).RequiresVerify = false, want true", tc)
		}
	}
}

// TestValidateExtractClass verifies that extract returns Class=extract, ReadOnly=true, and RequiresVerify=true.
func TestValidateExtractClass(t *testing.T) {
	c, err := ValidateWorkClass("extract")
	if err != nil {
		t.Fatalf("ValidateWorkClass(\"extract\") returned error: %v", err)
	}
	if c.Class != "extract" {
		t.Errorf("Class = %q, want %q", c.Class, "extract")
	}
	if !c.ReadOnly {
		t.Errorf("ReadOnly = false, want true")
	}
	if !c.RequiresVerify {
		t.Errorf("RequiresVerify = false, want true")
	}
}

// TestValidateArchitectClass verifies that architect, review, and review-deep return exact Class, ReadOnly=true, and RequiresVerify=true.
func TestValidateArchitectClass(t *testing.T) {
	cases := []string{"architect", "review", "review-deep"}
	for _, tc := range cases {
		c, err := ValidateWorkClass(tc)
		if err != nil {
			t.Errorf("ValidateWorkClass(%q) returned error: %v", tc, err)
			continue
		}
		if c.Class != tc {
			t.Errorf("ValidateWorkClass(%q).Class = %q, want %q", tc, c.Class, tc)
		}
		if !c.ReadOnly {
			t.Errorf("ValidateWorkClass(%q).ReadOnly = false, want true", tc)
		}
		if !c.RequiresVerify {
			t.Errorf("ValidateWorkClass(%q).RequiresVerify = false, want true", tc)
		}
	}
}

// TestValidateGateClass verifies that gate returns Class=gate, ReadOnly=true, and RequiresVerify=true.
func TestValidateGateClass(t *testing.T) {
	c, err := ValidateWorkClass("gate")
	if err != nil {
		t.Fatalf("ValidateWorkClass(\"gate\") returned error: %v", err)
	}
	if c.Class != "gate" {
		t.Errorf("Class = %q, want %q", c.Class, "gate")
	}
	if !c.ReadOnly {
		t.Errorf("ReadOnly = false, want true")
	}
	if !c.RequiresVerify {
		t.Errorf("RequiresVerify = false, want true")
	}
}
