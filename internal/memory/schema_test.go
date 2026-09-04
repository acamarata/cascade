package memory

import (
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"lowercases and sorts", "Beta Alpha", []string{"alpha", "beta"}},
		{"deduplicates", "alpha alpha ALPHA", []string{"alpha"}},
		{"splits on punctuation", "a-b_c.d", []string{"a", "b", "c", "d"}},
		{"keeps digits", "sha256 v2", []string{"sha256", "v2"}},
		{"drops separators only", "--- ///", nil},
		{"empty input", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tokenize(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("tokenize(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("tokenize(%q) = %v, want %v", tc.in, got, tc.want)
				}
			}
		})
	}
}

// TestTokenizeTruncatesLongRuns pins the bound: a pathological run costs a
// bounded key, and the record stays findable by its truncated prefix
// rather than dropping out of the index entirely.
func TestTokenizeLongRunIsTruncatedNotDropped(t *testing.T) {
	long := strings.Repeat("x", maxTokenLen+40)
	got := tokenize(long)
	if len(got) != 1 || len(got[0]) != maxTokenLen {
		t.Fatalf("tokenize(long) = %v (lengths differ), want one %d-byte token", got, maxTokenLen)
	}
}

func TestKeyLayout(t *testing.T) {
	id := recordID(KindProject, "a-record")
	if id != "project/a-record" {
		t.Fatalf("recordID = %q", id)
	}
	if got := recordKey(id); got != "proj:rec:project/a-record" {
		t.Fatalf("recordKey = %q", got)
	}
	if got := tokenKey("alpha", id); got != "proj:tok:alpha:project/a-record" {
		t.Fatalf("tokenKey = %q", got)
	}
	if !strings.HasPrefix(tokenKey("alpha", id), tokenScanPrefix("alpha")) {
		t.Fatal("tokenKey is not under tokenScanPrefix")
	}
	if got := kindRowPrefix(KindUser); got != "proj:rec:user/" {
		t.Fatalf("kindRowPrefix = %q", got)
	}
	for _, k := range []string{recordKey(id), tokenKey("a", id), metaVersionKey} {
		if !strings.HasPrefix(k, projectionPrefix) {
			t.Fatalf("key %q is not under the projection prefix", k)
		}
	}
}

func TestIndexedRecordVisible(t *testing.T) {
	at := fixedNow
	past := at.Add(-time.Hour).UnixNano()
	future := at.Add(time.Hour).UnixNano()

	live := IndexedRecord{ID: "project/a"}
	if !live.Visible(at) {
		t.Fatal("a live row with no TTL is not visible")
	}
	retired := IndexedRecord{ID: "project/a", Deleted: true}
	if retired.Visible(at) {
		t.Fatal("a retired row is visible")
	}
	expired := IndexedRecord{ID: "project/a", ExpiresAtUnixNano: &past}
	if expired.Visible(at) {
		t.Fatal("an expired row is visible")
	}
	unexpired := IndexedRecord{ID: "project/a", ExpiresAtUnixNano: &future}
	if !unexpired.Visible(at) {
		t.Fatal("an unexpired row is not visible")
	}
}

func TestRowCodecRoundTrip(t *testing.T) {
	exp := fixedNow.Add(time.Hour).UnixNano()
	want := IndexedRecord{
		ID: "project/a-record", Name: "a-record", Kind: KindProject,
		Description: "d", Body: "b", Origin: OriginSession, SessionID: "s-1",
		ScopeRef: "global", ContentHash: HashBody("b"),
		CreatedAtUnixNano: fixedNow.UnixNano(), UpdatedAtUnixNano: fixedNow.UnixNano(),
		ExpiresAtUnixNano: &exp, Confidence: 0.5,
		IndexedAtUnixNano: fixedNow.UnixNano(), EmbedModel: "m", Tokens: []string{"b", "d"},
	}
	data, err := encodeRow(want)
	if err != nil {
		t.Fatalf("encodeRow: %v", err)
	}
	got, err := decodeRow(data)
	if err != nil {
		t.Fatalf("decodeRow: %v", err)
	}
	if got.ID != want.ID || got.ContentHash != want.ContentHash || got.EmbedModel != "m" {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	if got.ExpiresAtUnixNano == nil || *got.ExpiresAtUnixNano != exp {
		t.Fatalf("round trip lost the TTL: %+v", got.ExpiresAtUnixNano)
	}
	again, err := encodeRow(got)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if string(again) != string(data) {
		t.Fatalf("encoding is not stable:\n%s\n%s", data, again)
	}
}

// TestDecodeRowRefusesGarbage pins the fail-closed read: a row that cannot
// be decoded is an integrity refusal, never a silently skipped result.
func TestDecodeRowRefusesGarbage(t *testing.T) {
	_, err := decodeRow([]byte("{not json"))
	if err == nil {
		t.Fatal("decodeRow accepted garbage")
	}
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("decodeRow error kind = %v, want KindIntegrity", err)
	}
}

func TestRowTokensCoverNameDescriptionAndBody(t *testing.T) {
	row := IndexedRecord{Name: "alpha", Description: "beta", Body: "gamma"}
	got := strings.Join(rowTokens(row), ",")
	if got != "alpha,beta,gamma" {
		t.Fatalf("rowTokens = %q", got)
	}
}
