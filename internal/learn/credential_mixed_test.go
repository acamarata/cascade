// Purpose: a credential-JSON base64 canary glued to 1..12 characters that
//
//	mix the two base64 alphabets (- _ + / = . : and alphanumerics) on
//	either side is still flagged, because the gate decodes the longest
//	same-alphabet run from every offset instead of trimming a fixed
//	window off the run's end (P1-BF-R94). Proven on real SQLite for JobID
//	and the five label fields, plus the registry decoder's head-only read
//	that the scan relies on. Canaries are assembled at run time, never
//	written as one literal.
//
// SPORT: learn/credential-mixed/ADD (P1-CAP-02).
package learn

import (
	"context"
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// labelFields are the five bounded label columns (labelPattern keeps '+').
var labelFields = []string{"TaskClass", "RiskClass", "LaneTier", "RetrievalStrategy", "Component"}

// mixedPrefixFamilies and mixedSuffixFamilies are the CR-5 probe's 6
// prefix and 9 suffix families; a form takes the first k characters.
var (
	mixedPrefixFamilies = []string{"job-1a2b3c4d", "x_Q9+/zZ-a_b", "+/-_+/-_+/-_",
		"a1B2c3D4e5F6", "=.:=.:=.:=.:", "ab+cd/ef-gh_"}
	mixedSuffixFamilies = []string{"-+-+-+-+-+-+", "_/_/_/_/_/_/", "+ab+cd+ef+gh",
		"/x_y/z-w/q+r", "-x9a-b8c-d7e", "=a=b=c=d=e=f", ".a.b:c:d.e:f", "a1B2c3D4e5F6", "+x9a/b8c+d7e"}
)

// mixedBlob returns an n-byte credential JSON blob (n >= 11) whose value
// repeats "???>>>": '?' and '>' encode to '/' and '+' (standard) or '_'
// and '-' (URL) at every byte alignment.
func mixedBlob(n int) []byte {
	head := `{"k` + `ey":"`
	return []byte(head + strings.Repeat("???>>>", n)[:n-len(head)-2] + `"}`)
}

// alnumBlob returns an n-byte credential JSON blob with an alphanumeric
// value, whose encodings carry neither alphabet's extra characters.
func alnumBlob(n int) []byte {
	head := `{"api_` + `token":"`
	return []byte(head + strings.Repeat("Q7", n)[:n-len(head)-2] + `"}`)
}

// encodedCanary encodes blob, checks the alphabet characters it must (or
// must not) carry, and returns it under a family name.
func encodedCanary(t *testing.T, enc *base64.Encoding, family string, blob []byte, must, mustNot string) (string, string) {
	t.Helper()
	c := enc.EncodeToString(blob)
	if (must != "" && !strings.ContainsAny(c, must)) || (mustNot != "" && strings.ContainsAny(c, mustNot)) {
		t.Fatalf("%s canary of %d chars lacks %q or carries %q", family, len(c), must, mustNot)
	}
	return fmt.Sprintf("%s-%d", family, len(c)), c
}

// generatedCanaries returns the probe's 46 generated canaries (24..62
// chars): 15 raw URL with '-'/'_', 13 raw standard with '+'/'/', 8 raw URL
// alphanumeric and 10 padded URL with '-'/'_'. short keeps one of each
// family: raw URL 24, raw standard 44, alphanumeric 40 and padded 60 chars.
func generatedCanaries(t *testing.T, short bool) map[string]string {
	t.Helper()
	out := map[string]string{}
	add := func(k, v string) { out[k] = v }
	keep := func(n, pick int) bool { return !short || n == pick }
	for n := 18; n <= 46; n += 2 {
		if keep(n, 18) {
			add(encodedCanary(t, base64.RawURLEncoding, "rawurl", mixedBlob(n), "-_", ""))
		}
	}
	for n := 19; n <= 43; n += 2 {
		if keep(n, 33) {
			add(encodedCanary(t, base64.RawStdEncoding, "rawstd", mixedBlob(n), "+/", ""))
		}
	}
	for n := 18; n <= 46; n += 4 {
		if keep(n, 30) {
			add(encodedCanary(t, base64.RawURLEncoding, "rawurl-alnum", alnumBlob(n), "", "-_+/"))
		}
	}
	for n := 18; n <= 45; n += 3 {
		if keep(n, 45) {
			add(encodedCanary(t, base64.URLEncoding, "url-padded", mixedBlob(n), "-_", ""))
		}
	}
	return out
}

// mixedForms returns c bare, with each prefix and each suffix of length
// 1..12 from every family, and with prefix and suffix of lengths
// (p, s) for p in pLens and s in 1..12 for every family pair, deduplicated.
func mixedForms(c string, pLens []int) []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	add(c)
	for k := 1; k <= 12; k++ {
		for _, pf := range mixedPrefixFamilies {
			add(pf[:k] + c)
		}
		for _, sf := range mixedSuffixFamilies {
			add(c + sf[:k])
		}
	}
	for _, p := range pLens {
		for s := 1; s <= 12; s++ {
			for _, pf := range mixedPrefixFamilies {
				for _, sf := range mixedSuffixFamilies {
					add(pf[:p] + c + sf[:s])
				}
			}
		}
	}
	return out
}

// TestRegistryDecodeReadsOnlyTheHead: the registry's Decode step accepts a
// credential canary followed by any 0..12 characters of its own alphabet
// (cut so the length is not 1 mod 4), which is the property that lets
// decodesAnyRun decode the longest run instead of trimming its end.
func TestRegistryDecodeReadsOnlyTheHead(t *testing.T) {
	calls := 0
	p := countingDecodePattern(t, &calls)
	tails := map[string]string{"rawurl": "-x9a_b8c-d7e", "rawurl-alnum": "-x9a_b8c-d7e",
		"rawstd": "+x9a/b8c+d7e", "url-padded": "-x9a_b8c-d7e"}
	checked := 0
	for name, c := range generatedCanaries(t, false) {
		tail := tails[name[:strings.LastIndex(name, "-")]]
		head := strings.TrimRight(c, "=")
		for k := 0; k <= 12; k++ {
			w := head + tail[:k]
			if len(w)%4 == 1 {
				w = w[:len(w)-1]
			}
			checked++
			if !p.Decode(w) {
				t.Errorf("%s + %d tail chars: Decode rejects; trailing characters hide the credential", name, k)
			}
		}
	}
	if checked != 46*13 {
		t.Errorf("checked = %d, want %d", checked, 46*13)
	}
}

// TestCredentialDetectsMixedAlphabetSuffix: for one canary of each probe
// family (the four generated families and the four registry and CR-4
// base64-JSON variants), every form from mixedForms (prefixes and suffixes 1..12 from the
// 6 and 9 mixed families, and combined) is flagged and refused with
// KindInvalidInput in JobID and the five label fields of a real SQLite db;
// afterwards only the seed row exists and no column holds a canary.
func TestCredentialDetectsMixedAlphabetSuffix(t *testing.T) {
	db, w := seededOutcomeDB(t)
	ctx := context.Background()
	canaries := generatedCanaries(t, true)
	for k, v := range probeCanaries() {
		if strings.HasPrefix(k, "b64-") || k == "base64-json" {
			canaries[k] = v
		}
	}
	cases, seq := 0, 0
	for name, c := range canaries {
		for _, v := range mixedForms(c, []int{12}) {
			if !credentialShaped(v) {
				t.Errorf("%s: a %d-char form around the %d-char canary is not flagged", name, len(v), len(c))
			}
			for _, field := range append([]string{"JobID"}, labelFields...) {
				seq++
				o := baseOutcome(fmt.Sprintf("job-mx-%d", seq))
				reflect.ValueOf(&o).Elem().FieldByName(field).SetString(v)
				cases++
				if leaked(w.Record(ctx, o), c) {
					t.Errorf("%s in %s: a %d-char form is not refused cleanly", name, field, len(v))
				}
			}
		}
		assertCanaryInNoColumn(t, db, name, c)
	}
	if n := telemetryRowCount(t, db); n != 1 {
		t.Errorf("rows = %d, want only the seeded outcome", n)
	}
	t.Logf("canaries = %d mixed-alphabet cases = %d", len(canaries), cases)
}
