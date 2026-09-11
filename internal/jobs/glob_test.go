package jobs

import "testing"

// TestScopePrefixCover covers the R-21.168 expansion, normalization and
// intersection table: literal-file reduction, trailing "/**" reduction,
// repo-root, round-trip through String/ParseScope, and every malformed
// shape this package must reject.
func TestScopePrefixCover(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		wantDir string
		wantRec bool
		wantErr bool
	}{
		{name: "recursive dir", pattern: "internal/jobs/**", wantDir: "internal/jobs", wantRec: true},
		{name: "literal file reduces to parent dir", pattern: "internal/jobs/lease.go", wantDir: "internal/jobs", wantRec: false},
		{name: "literal bare dir reduces to its parent (file-path rule)", pattern: "internal/jobs", wantDir: "internal", wantRec: false},
		{name: "repo root file", pattern: "README.md", wantDir: "", wantRec: false},
		{name: "repo root recursive", pattern: "**", wantDir: "", wantRec: true},
		{name: "mid-segment wildcard rejected", pattern: "internal/*/foo/**", wantErr: true},
		{name: "double-star not at end rejected", pattern: "internal/**/foo", wantErr: true},
		{name: "question mark rejected", pattern: "internal/jobs?.go", wantErr: true},
		{name: "bare star suffix rejected", pattern: "internal/jobs/*", wantErr: true},
		{name: "absolute path rejected", pattern: "/internal/jobs/**", wantErr: true},
		{name: "dotdot rejected", pattern: "internal/../jobs/**", wantErr: true},
		{name: "unbalanced class rejected", pattern: "internal/[abc/**", wantErr: true},
		{name: "empty rejected", pattern: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeScope(tc.pattern)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeScope(%q) = nil error, want rejection", tc.pattern)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeScope(%q): %v", tc.pattern, err)
			}
			if len(got.prefixes) != 1 || got.prefixes[0].dir != tc.wantDir || got.prefixes[0].recursive != tc.wantRec {
				t.Fatalf("NormalizeScope(%q) = %+v, want dir=%q recursive=%v", tc.pattern, got.prefixes, tc.wantDir, tc.wantRec)
			}
			roundTripped, err := ParseScope(got.String())
			if err != nil {
				t.Fatalf("ParseScope(%q): %v", got.String(), err)
			}
			if roundTripped.String() != got.String() {
				t.Errorf("round trip = %q, want %q", roundTripped.String(), got.String())
			}
		})
	}
}

// TestScopeIntersects proves the R-21.168 total containment rule over a
// deliberately adversarial table: siblings that must NOT intersect,
// ancestor/descendant pairs that MUST, and the repo-root prefix that
// intersects everything.
func TestScopeIntersects(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{name: "identical", a: "internal/jobs/**", b: "internal/jobs/**", want: true},
		{name: "descendant", a: "internal/**", b: "internal/jobs/**", want: true},
		{name: "ancestor (reversed order)", a: "internal/jobs/**", b: "internal/**", want: true},
		{name: "disjoint siblings", a: "internal/jobs/**", b: "internal/nodes/**", want: false},
		{name: "disjoint prefix-looking siblings", a: "internal/jobs/**", b: "internal/jobsx/**", want: false},
		{name: "root intersects everything", a: "**", b: "internal/nodes/**", want: true},
		{name: "distinct repos never compared here (scope only)", a: "a/**", b: "b/**", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := NormalizeScope(tc.a)
			if err != nil {
				t.Fatalf("NormalizeScope(%q): %v", tc.a, err)
			}
			b, err := NormalizeScope(tc.b)
			if err != nil {
				t.Fatalf("NormalizeScope(%q): %v", tc.b, err)
			}
			if got := a.Intersects(b); got != tc.want {
				t.Errorf("%q.Intersects(%q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			if got := b.Intersects(a); got != tc.want {
				t.Errorf("%q.Intersects(%q) [reversed] = %v, want %v", tc.b, tc.a, got, tc.want)
			}
		})
	}
}

// FuzzScopeGlob fuzzes the expander and the intersection predicate
// together: NormalizeScope must never panic on arbitrary input, and
// whenever it accepts BOTH of two independently-fuzzed patterns, calling
// Intersects on the results must never panic either. This is the
// ticket's required 30s no-panic fuzz target over the parser this
// ticket's license-gated doublestar dependency backs.
func FuzzScopeGlob(f *testing.F) {
	seeds := []string{
		"internal/jobs/**", "internal/jobs/lease.go", "**", "README.md",
		"internal/*/foo/**", "internal/**/foo", "/abs/**", "internal/../jobs/**",
		"internal/[abc/**", "", ",", "a/**,b/**",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, pattern string) {
		scope, err := NormalizeScope(pattern)
		if err != nil {
			return
		}
		other, err2 := NormalizeScope(pattern)
		if err2 != nil {
			t.Fatalf("NormalizeScope(%q) succeeded then failed on retry: %v", pattern, err2)
		}
		_ = scope.Intersects(other)
		_ = scope.String()
	})
}
