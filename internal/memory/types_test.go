package memory

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fixedNow is the instant every test in this package judges TTLs against
// and every frozen clock starts at. It is a constant so no test result can
// depend on when the suite ran.
var fixedNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// newTestClock returns a frozen clock at fixedNow. The clock comes from
// internal/testkit rather than being declared here, so this package tests
// against the same frozen-clock implementation as the rest of the module.
func newTestClock() *testkit.FrozenClock { return testkit.NewFrozenClock(fixedNow) }

// validEntry returns a record that passes validation, for tests that want
// to vary exactly one field.
func validEntry() MemoryEntry {
	return MemoryEntry{
		Name:        "a-record",
		Kind:        KindProject,
		Description: "a description",
		Body:        "a body\n",
		ScopeRef:    "global",
		Confidence:  0.5,
		Provenance:  Provenance{Origin: OriginSession, SessionID: "s-1"},
	}
}

// TestBlake3ReferenceVectors pins HashBody to the published BLAKE3
// reference test vectors rather than to this package's own output. The
// inputs are the reference suite's own: a zero-length input, and inputs of
// length n filled with the repeating byte pattern i mod 251. A digest
// asserted against the specification catches a swapped or misconfigured
// hash implementation; a digest asserted against our own previous run
// would not.
func TestBlake3ReferenceVectors(t *testing.T) {
	cases := []struct {
		length int
		want   string
	}{
		{0, "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262"},
		{1, "2d3adedff11b61f14c886e35afa036736dcd87a74d27b5c1510225d0f592e213"},
		{1023, "10108970eeda3eb932baac1428c7a2163b0e924c9a9e25b35bba72b28f70bd11"},
	}
	for _, c := range cases {
		in := make([]byte, c.length)
		for i := range in {
			in[i] = byte(i % 251)
		}
		if got := HashBody(string(in)); got != c.want {
			t.Errorf("HashBody(reference input of %d bytes) = %s, want %s", c.length, got, c.want)
		}
		if _, err := hex.DecodeString(HashBody(string(in))); err != nil {
			t.Errorf("digest for %d-byte input is not hex: %v", c.length, err)
		}
	}
}

// TestHashBodyDeterministic proves the digest does not vary between calls,
// which is what lets it be stored and compared across runs and machines.
func TestHashBodyDeterministic(t *testing.T) {
	const body = "مرحبا 世界\nline two\n"
	first := HashBody(body)
	for i := 0; i < 3; i++ {
		if got := HashBody(body); got != first {
			t.Fatalf("call %d returned %s, first call returned %s", i, got, first)
		}
	}
	if HashBody(body) == HashBody(body+" ") {
		t.Error("bodies differing by one byte hashed the same")
	}
}

func TestParseKind(t *testing.T) {
	for _, k := range AllKinds() {
		got, err := ParseKind(string(k))
		if err != nil || got != k {
			t.Errorf("ParseKind(%q) = %q, %v", k, got, err)
		}
	}
	for _, bad := range []string{"", "USER", "lesson", "user ", "../user"} {
		got, err := ParseKind(bad)
		if !errors.Is(err, ErrInvalidKind) {
			t.Errorf("ParseKind(%q) error = %v, want ErrInvalidKind", bad, err)
		}
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("ParseKind(%q) is not a KindInvalidInput taxonomy error", bad)
		}
		if got != "" {
			t.Errorf("ParseKind(%q) returned %q alongside its error", bad, got)
		}
	}
}

// TestAllKindsIsACopy proves the taxonomy cannot be mutated through the
// slice AllKinds hands out.
func TestAllKindsIsACopy(t *testing.T) {
	got := AllKinds()
	got[0] = "tampered"
	if AllKinds()[0] != KindUser {
		t.Fatal("mutating the returned slice changed the taxonomy")
	}
	if KindUser.String() != "user" || !KindUser.Valid() || MemoryKind("x").Valid() {
		t.Error("MemoryKind String/Valid disagree with the taxonomy")
	}
}

func TestParseOrigin(t *testing.T) {
	for _, o := range []Origin{OriginSession, OriginFile, OriginHarness} {
		got, err := ParseOrigin(string(o))
		if err != nil || got != o {
			t.Errorf("ParseOrigin(%q) = %q, %v", o, got, err)
		}
		if got.String() != string(o) {
			t.Errorf("Origin(%q).String() = %q", o, got.String())
		}
	}
	if _, err := ParseOrigin("agent"); !errors.Is(err, ErrInvalidOrigin) {
		t.Errorf("ParseOrigin(agent) error = %v, want ErrInvalidOrigin", err)
	}
}

// TestValidateRejectsEachField walks every distinct refusal Validate can
// produce and asserts it comes back as its own sentinel, so a caller can
// tell them apart without reading message text.
func TestValidateRejectsEachField(t *testing.T) {
	past := fixedNow.Add(-time.Second)
	cases := []struct {
		name  string
		mutet func(*MemoryEntry)
		want  error
	}{
		{"empty name", func(e *MemoryEntry) { e.Name = "" }, ErrInvalidName},
		{"unknown kind", func(e *MemoryEntry) { e.Kind = "lesson" }, ErrInvalidKind},
		{"unknown origin", func(e *MemoryEntry) { e.Provenance.Origin = "cron" }, ErrInvalidOrigin},
		{"blank scope", func(e *MemoryEntry) { e.ScopeRef = "   " }, ErrInvalidScopeRef},
		{"bad supersedes", func(e *MemoryEntry) { e.Supersedes = "no-slash" }, ErrInvalidSupersedes},
		{"unknown kind in supersedes", func(e *MemoryEntry) { e.Supersedes = "lesson/x" }, ErrInvalidSupersedes},
		{"bad name in supersedes", func(e *MemoryEntry) { e.Supersedes = "user/../x" }, ErrInvalidSupersedes},
		{"confidence above one", func(e *MemoryEntry) { e.Confidence = 1.0001 }, ErrInvalidConfidence},
		{"confidence below zero", func(e *MemoryEntry) { e.Confidence = -0.0001 }, ErrInvalidConfidence},
		{"expired ttl", func(e *MemoryEntry) { e.ExpiresAt = &past }, ErrAlreadyExpired},
		{"ttl exactly now", func(e *MemoryEntry) { e.ExpiresAt = &fixedNow }, ErrAlreadyExpired},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := validEntry()
			c.mutet(&e)
			err := e.Validate(fixedNow)
			if !errors.Is(err, c.want) {
				t.Fatalf("Validate error = %v, want %v", err, c.want)
			}
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("Validate error %v is not a KindInvalidInput taxonomy error", err)
			}
		})
	}
}

// TestValidateAcceptsBoundaries proves the accepted range is closed at
// both ends and that a future TTL and a well-formed supersedes pass.
func TestValidateAcceptsBoundaries(t *testing.T) {
	future := fixedNow.Add(time.Hour)
	for _, conf := range []float64{0, 1, 0.5} {
		e := validEntry()
		e.Confidence = conf
		e.ExpiresAt = &future
		e.Supersedes = "project/older-record"
		if err := e.Validate(fixedNow); err != nil {
			t.Errorf("confidence %v rejected: %v", conf, err)
		}
	}
}

func TestExpiredAndBodyHash(t *testing.T) {
	e := validEntry()
	if e.Expired(fixedNow) {
		t.Error("a record with no TTL reported expired")
	}
	past := fixedNow.Add(-time.Hour)
	e.ExpiresAt = &past
	if !e.Expired(fixedNow) {
		t.Error("a record with a past TTL reported live")
	}
	if e.BodyHash() != HashBody(e.Body) {
		t.Error("BodyHash disagrees with HashBody")
	}
	e.Provenance.ContentHash = e.BodyHash()
	e.Body += "edited outside the store\n"
	if e.BodyHash() == e.Provenance.ContentHash {
		t.Error("an edited body did not change BodyHash, so drift is undetectable")
	}
}

// TestCanonicalNormalizesToUTC proves a record written from a machine in
// any zone reads back as the same instant, and that the TTL pointer is
// copied rather than shared with the caller.
func TestCanonicalNormalizesToUTC(t *testing.T) {
	zone := time.FixedZone("UTC+7", 7*60*60)
	ttl := fixedNow.Add(time.Hour).In(zone)
	e := validEntry()
	e.Provenance.CreatedAt = fixedNow.In(zone)
	e.Provenance.UpdatedAt = fixedNow.In(zone)
	e.ExpiresAt = &ttl

	got := e.canonical()
	if got.Provenance.CreatedAt.Location() != time.UTC || got.ExpiresAt.Location() != time.UTC {
		t.Fatal("canonical left a timestamp outside UTC")
	}
	if !got.Provenance.CreatedAt.Equal(fixedNow) || !got.ExpiresAt.Equal(ttl) {
		t.Fatal("canonical changed an instant while normalizing its zone")
	}
	if got.ExpiresAt == e.ExpiresAt {
		t.Fatal("canonical shared the caller's TTL pointer")
	}
}

// TestValidateNameRejectsUnsafeSegments is the path-safety test. Every
// input here would, if accepted, either escape the store's base directory
// or produce a file that cannot exist on one of the supported platforms.
func TestValidateNameRejectsUnsafeSegments(t *testing.T) {
	bad := []string{
		"", ".", "..", "../escape", "a/b", `a\b`, "/abs", "a:b", "a*b", "a?b",
		"a|b", "a<b", "a>b", "a\"b", "a\x00b", "a\nb", " leading", "trailing ",
		".hidden", "ends.", "ends-", "con", "CON.md", "nul", "lpt9", "com1.txt",
		"café", "emoji-\U0001F600", strings.Repeat("x", maxNameLen+1),
	}
	for _, name := range bad {
		err := ValidateName(name)
		if !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidateName(%q) = %v, want ErrInvalidName", name, err)
		}
	}
	good := []string{
		"a", "a-b", "a_b", "a.b", "record.2026Q1", "A1", strings.Repeat("x", maxNameLen),
		"units-and-clock", "feedback_max_planning", "console", "communication",
	}
	for _, name := range good {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}
}
