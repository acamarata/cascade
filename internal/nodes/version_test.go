package nodes

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestParseReleaseVersion_Table(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
		major   int
		minor   int
		patch   int
	}{
		{"v2.3.4", false, 2, 3, 4},
		{"2.3.4", false, 2, 3, 4},
		{"v2.3.4-rc1", false, 2, 3, 4},
		{"dev", true, 0, 0, 0},
		{"", true, 0, 0, 0},
		{"v2.3", true, 0, 0, 0},
		{"v2.x.4", true, 0, 0, 0},
	}
	for _, c := range cases {
		got, err := ParseReleaseVersion(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseReleaseVersion(%q): expected an error, got %+v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseReleaseVersion(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got.Major != c.major || got.Minor != c.minor || got.Patch != c.patch {
			t.Errorf("ParseReleaseVersion(%q) = %+v, want {%d %d %d}", c.in, got, c.major, c.minor, c.patch)
		}
	}
}

func TestNegotiateVersion_SameMinorWindow(t *testing.T) {
	res, err := NegotiateVersion("v2.3.0", "v2.3.9")
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if !res.InWindow {
		t.Fatal("InWindow = false, want true")
	}
}

func TestNegotiateVersion_OutOfWindowRefused(t *testing.T) {
	_, err := NegotiateVersion("v2.3.0", "v2.4.0")
	if err == nil {
		t.Fatal("expected an out-of-window refusal, got nil")
	}
	kind, _ := cascade.KindOf(err)
	if kind != cascade.KindUnsupported {
		t.Fatalf("kind = %v, want KindUnsupported", kind)
	}
	msg := err.Error()
	if !contains(msg, "v2.3.0") || !contains(msg, "v2.4.0") {
		t.Fatalf("refusal message %q does not name both versions", msg)
	}
}

func TestNegotiateVersion_UnparseableRefusedNotAssumedCompatible(t *testing.T) {
	if _, err := NegotiateVersion("v2.3.0", "dev"); err == nil {
		t.Fatal("expected a refusal for an unparseable node version, got nil (must never assume compatible)")
	}
	if _, err := NegotiateVersion("dev", "v2.3.0"); err == nil {
		t.Fatal("expected a refusal for an unparseable controller version, got nil")
	}
}

func TestWouldFallOutOfWindow(t *testing.T) {
	if WouldFallOutOfWindow("v2.3.0", "v2.3.9") {
		t.Fatal("same-minor pair reported as falling out of window")
	}
	if !WouldFallOutOfWindow("v2.3.0", "v2.4.0") {
		t.Fatal("different-minor pair not reported as falling out of window")
	}
	if !WouldFallOutOfWindow("v2.3.0", "dev") {
		t.Fatal("unparseable node version must fail closed toward warning, not silently pass")
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
