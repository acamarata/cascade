package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// goldenPath returns the path of a checked-in format fixture.
func goldenPath(name string) string {
	return filepath.Join("testdata", "v1-goldens", name)
}

// mustReadGolden reads a fixture, failing the test rather than the parse
// if it is missing.
func mustReadGolden(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(goldenPath(name))
	if err != nil {
		t.Fatalf("reading golden %s: %v", name, err)
	}
	return data
}

func ptrTime(t time.Time) *time.Time { return &t }

// goldenCases pairs each checked-in fixture with the exact record it must
// decode to. The expected records are written out in full, by hand: a test
// that derived them from the decoder would agree with the decoder no
// matter what the decoder did.
// goldenCase pairs a fixture file with the record it must decode to.
type goldenCase struct {
	file string
	want MemoryEntry
}

// goldenCases is the full fixture set. It is assembled from two halves so
// each stays inside the 50-line function limit.
func goldenCases() []goldenCase {
	return append(goldenCasesPlain(), goldenCasesOptional()...)
}

// goldenCasesPlain covers the two fixtures with no TTL set.
func goldenCasesPlain() []goldenCase {
	return []goldenCase{
		{"entry_user.md", MemoryEntry{
			Name: "units-and-clock", Kind: KindUser,
			Description: "Stated unit and clock preferences",
			Body:        "Prefers metric units and a 24-hour clock.\n\nWorks primarily in Go and TypeScript.\n",
			ScopeRef:    "global", Confidence: 0.9,
			Provenance: Provenance{
				Origin: OriginSession, SessionID: "0f9c1d2e-4a5b-6c7d-8e9f-a0b1c2d3e4f5",
				CreatedAt:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
				UpdatedAt:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
				ContentHash: "aa874ea8d9ab7dca40dce19fe9e287d315527a30d1cbbdff7e61beae7ce19eb5",
			},
		}},
		{"entry_feedback.md", MemoryEntry{
			Name: "read-before-summarising", Kind: KindFeedback,
			Description: `Correction: "read the file first" #process; colon, quote and hash in one value`,
			Body: "Corrected on 2026-01-02: do not summarise a file before reading it.\n\n---\n\n" +
				"The line above is a horizontal rule inside the body, not a frontmatter\n" +
				"fence. A reader that stops at the first `---` anywhere in the file would\n" +
				"truncate this record here.\n",
			ScopeRef:   "repo/cascade",
			CommitSHA:  "9f2c1a4b7d3e5086af12c9d4b6e8017253ac9db4",
			Supersedes: "feedback/summarise-first",
			Confidence: 1,
			Provenance: Provenance{
				Origin: OriginHarness,
				// A record produced outside a session carries no session id.
				CreatedAt:   time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC),
				UpdatedAt:   time.Date(2026, 1, 3, 9, 10, 11, 0, time.UTC),
				ContentHash: "a5b859d08b9e94b4445f61c305d694f798bbfce7de96e3a3d35a43b9d4d8074d",
			},
		}},
	}
}

// goldenCasesOptional covers the fixtures exercising the optional fields.
func goldenCasesOptional() []goldenCase {
	return []goldenCase{
		{"entry_project.md", MemoryEntry{
			Name: "index_rebuild_direction", Kind: KindProject,
			Description: "Which side of the store is authoritative when the two disagree",
			Body: "The retrieval index is rebuilt from the record tree, never the reverse.\n\n" +
				"Mixed script check: \u0645\u0631\u062d\u0628\u0627, \u4e16\u754c, \u03a9, and a combining sequence e\u0301.\n",
			ScopeRef:  "repo/cascade",
			CommitSHA: "1b0d7e3a9c85f24610bd3e7a8c95104fb2d6e830",
			// Confidence deliberately not a round number.
			Confidence: 0.75,
			Provenance: Provenance{
				Origin: OriginFile, SessionID: "3c4d5e6f-7a8b-9c0d-1e2f-a3b4c5d6e7f8",
				CreatedAt:   time.Date(2025, 12, 31, 23, 59, 59, 500000000, time.UTC),
				UpdatedAt:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
				ContentHash: "3d9e5afcc20b3b30cb703820a8a630afc8f56750cc17dfa3517104d0ddfc7925",
			},
		}},
		{"entry_reference.md", MemoryEntry{
			Name: "rate.limits.2026Q1", Kind: KindReference,
			ScopeRef:   "global",
			Confidence: 0,
			ExpiresAt:  ptrTime(time.Date(2099, 6, 30, 0, 0, 0, 0, time.UTC)),
			Provenance: Provenance{
				Origin: OriginSession, SessionID: "a1b2c3d4-e5f6-7081-92a3-b4c5d6e7f809",
				CreatedAt:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
				UpdatedAt:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
				ContentHash: "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262",
			},
		}},
	}
}

// TestGolden_Decode asserts each checked-in fixture parses to exactly the
// record it is supposed to, field by field including provenance.
func TestGolden_Decode(t *testing.T) {
	for _, c := range goldenCases() {
		t.Run(c.file, func(t *testing.T) {
			got, err := decodeEntry(mustReadGolden(t, c.file))
			if err != nil {
				t.Fatalf("decoding %s: %v", c.file, err)
			}
			assertEntryEqual(t, got, c.want)
			if got.Provenance.ContentHash != got.BodyHash() {
				t.Errorf("fixture's stored hash %s does not match its own body hash %s",
					got.Provenance.ContentHash, got.BodyHash())
			}
		})
	}
}

// TestGolden_Encode closes the round trip in the other direction: the
// encoder must reproduce each fixture byte for byte. This is the assertion
// that makes the format a contract, so it is compared against the
// checked-in file and never against freshly generated output.
func TestGolden_Encode(t *testing.T) {
	for _, c := range goldenCases() {
		t.Run(c.file, func(t *testing.T) {
			want := mustReadGolden(t, c.file)
			got := encodeEntry(c.want.canonical())
			if string(got) != string(want) {
				t.Fatalf("encoding drifted from the fixture.\n got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// assertEntryEqual compares every field, reporting each difference rather
// than stopping at the first, so one run shows the whole divergence.
func assertEntryEqual(t *testing.T, got, want MemoryEntry) {
	t.Helper()
	if got.Name != want.Name || got.Kind != want.Kind {
		t.Errorf("identity = %s/%s, want %s/%s", got.Kind, got.Name, want.Kind, want.Name)
	}
	if got.Description != want.Description {
		t.Errorf("description = %q, want %q", got.Description, want.Description)
	}
	if got.Body != want.Body {
		t.Errorf("body = %q, want %q", got.Body, want.Body)
	}
	if got.ScopeRef != want.ScopeRef || got.CommitSHA != want.CommitSHA || got.Supersedes != want.Supersedes {
		t.Errorf("refs = %q/%q/%q, want %q/%q/%q",
			got.ScopeRef, got.CommitSHA, got.Supersedes,
			want.ScopeRef, want.CommitSHA, want.Supersedes)
	}
	if got.Confidence != want.Confidence {
		t.Errorf("confidence = %v, want %v", got.Confidence, want.Confidence)
	}
	assertTTLEqual(t, got.ExpiresAt, want.ExpiresAt)
	assertProvenanceEqual(t, got.Provenance, want.Provenance)
}

func assertTTLEqual(t *testing.T, got, want *time.Time) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Errorf("expires_at = %v, want %v", got, want)
	case !got.Equal(*want):
		t.Errorf("expires_at = %s, want %s", got, want)
	}
}

func assertProvenanceEqual(t *testing.T, got, want Provenance) {
	t.Helper()
	if got.Origin != want.Origin || got.SessionID != want.SessionID {
		t.Errorf("origin/session = %q/%q, want %q/%q",
			got.Origin, got.SessionID, want.Origin, want.SessionID)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("timestamps = %s/%s, want %s/%s",
			got.CreatedAt, got.UpdatedAt, want.CreatedAt, want.UpdatedAt)
	}
	if got.CreatedAt.Location() != time.UTC || got.UpdatedAt.Location() != time.UTC {
		t.Error("decoded timestamps are not in UTC, so they would compare unequal across zones")
	}
	if got.ContentHash != want.ContentHash {
		t.Errorf("content_hash = %s, want %s", got.ContentHash, want.ContentHash)
	}
}

// TestCodecRoundTripsHardValues drives values a hand-rolled serializer
// typically loses: characters YAML would reinterpret, an empty value, a
// value far longer than any line the format normally carries, and text in
// scripts outside ASCII.
func TestCodecRoundTripsHardValues(t *testing.T) {
	long := strings.Repeat("very long value, repeated. ", 4000)
	values := []string{
		"", " ", "plain",
		"has: a colon", "#leading hash", "- leading dash", "yes", "no", "null", "~",
		"1.0", "0x10", "2026-01-02", "[bracketed]", "{braced}", "*anchor", "&anchor",
		`quotes " and ' and \ backslash`, "tab\tand\nnewline\r\n", "trailing space ",
		"---", "\n---\n", "emoji \U0001F600 and مرحبا and 世界 and ́combining",
		"\x00 control bytes \x1f", long,
	}
	for i, v := range values {
		e := validEntry()
		e.Description, e.Body, e.Provenance.SessionID = v, v, v
		e.Provenance.CreatedAt = fixedNow
		e.Provenance.UpdatedAt = fixedNow
		e.Provenance.ContentHash = HashBody(v)
		got, err := decodeEntry(encodeEntry(e.canonical()))
		if err != nil {
			t.Fatalf("value %d (%q) failed to round trip: %v", i, truncate(v), err)
		}
		assertEntryEqual(t, got, e.canonical())
	}
}

// truncate shortens a value for an error message.
func truncate(s string) string {
	if len(s) <= 60 {
		return s
	}
	return s[:60] + "..."
}

// TestCodecRoundTripsTTLAndConfidencePrecision proves the two non-string
// fields survive exactly, including a float that has no short decimal form
// and a timestamp with nanosecond precision.
func TestCodecRoundTripsTTLAndConfidencePrecision(t *testing.T) {
	ttl := time.Date(2031, 2, 3, 4, 5, 6, 7, time.UTC)
	e := validEntry()
	e.Confidence = 0.1 + 0.2
	e.ExpiresAt = &ttl
	e.Provenance.CreatedAt = fixedNow
	e.Provenance.UpdatedAt = fixedNow.Add(999999999 * time.Nanosecond)
	got, err := decodeEntry(encodeEntry(e.canonical()))
	if err != nil {
		t.Fatalf("round trip failed: %v", err)
	}
	if got.Confidence != e.Confidence {
		t.Errorf("confidence = %v, want %v", got.Confidence, e.Confidence)
	}
	if !got.ExpiresAt.Equal(ttl) || !got.Provenance.UpdatedAt.Equal(e.Provenance.UpdatedAt.UTC()) {
		t.Error("a timestamp lost precision through the round trip")
	}
}
