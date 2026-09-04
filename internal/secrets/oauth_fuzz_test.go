// Purpose: fuzz the two decoders in the OAuth flow that read
//
//	attacker-influenced bytes - the loopback redirect query and the
//	token-endpoint JSON body (06-FORGE-SPEC §5.7).
//
// Constraints: the properties asserted are the security ones, not just
//
//	"no panic". A parse that SUCCEEDS must have produced a state and a
//	code, so there is no input that yields a silently-accepted callback
//	with a missing or ambiguous state; and no successful parse may come
//	from a query that repeats code or state, because "take the first" is
//	the ambiguity a parameter-injection attack needs.
//
// SPORT: OAUTH_BROKER: ADD (fuzz).

package secrets

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oauthFuzzSeedDir follows internal/client's by-target corpus convention.
const oauthFuzzSeedDir = "../testdata/fuzz/FuzzOAuthCallbackParse"

func TestFuzzOAuthCallbackParseSeedProvenanceExists(t *testing.T) {
	path := filepath.Join(oauthFuzzSeedDir, "README.md")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("provenance README missing at %s: %v", path, err)
	}
	if info.IsDir() {
		t.Fatalf("%s is a directory, want a file", path)
	}
}

// loadOAuthSeeds reads every .txt seed in the corpus directory.
func loadOAuthSeeds(f *testing.F) []string {
	f.Helper()
	entries, err := os.ReadDir(oauthFuzzSeedDir)
	if err != nil {
		f.Fatalf("reading %s: %v", oauthFuzzSeedDir, err)
	}
	var seeds []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".txt" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(oauthFuzzSeedDir, e.Name()))
		if readErr != nil {
			f.Fatalf("reading seed %s: %v", e.Name(), readErr)
		}
		seeds = append(seeds, string(data))
	}
	if len(seeds) == 0 {
		f.Fatalf("no .txt seeds in %s", oauthFuzzSeedDir)
	}
	return seeds
}

// FuzzOAuthCallbackParse proves parseOAuthCallback never panics and never
// silently accepts a callback that is not unambiguously bound to one state.
func FuzzOAuthCallbackParse(f *testing.F) {
	for _, seed := range loadOAuthSeeds(f) {
		f.Add(seed)
	}
	for _, seed := range []string{
		"", "?", "&&&", "state", "state=", "code=&state=x", "%", "%zz",
		"state=x&state=x", "error=&code=c&state=s", "code=" + strings.Repeat("c", maxCallbackCodeLen+1) + "&state=s",
		strings.Repeat("a=b&", maxCallbackQueryLen),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parseOAuthCallback panicked on a %d-byte query: %v", len(raw), r)
			}
		}()
		callback, err := parseOAuthCallback(raw)
		if err != nil {
			return
		}
		if callback.state == "" {
			t.Fatalf("a %d-byte query parsed with no state; that is an unbound callback", len(raw))
		}
		if callback.errorCode == "" && callback.code.Empty() {
			t.Fatalf("a %d-byte query parsed with neither a code nor an error", len(raw))
		}
		if callback.errorCode != "" && !callback.code.Empty() {
			t.Fatalf("a %d-byte query parsed as both a denial and a grant", len(raw))
		}
		values, parseErr := url.ParseQuery(raw)
		if parseErr != nil {
			t.Fatalf("a query that url.ParseQuery rejects was accepted: %d bytes", len(raw))
		}
		for _, key := range []string{"code", "state", "error"} {
			if len(values[key]) > 1 {
				t.Fatalf("a query repeating %s was accepted; that is the injection ambiguity", key)
			}
		}
		if callback.code.Len() > maxCallbackCodeLen {
			t.Fatalf("an oversized code (%d bytes) was accepted", callback.code.Len())
		}
	})
}

// FuzzOAuthTokenResponseDecode proves decodeTokenResponse never panics and
// never returns a "successful" response with no access token.
func FuzzOAuthTokenResponseDecode(f *testing.F) {
	for _, seed := range []string{
		"", "{", "null", "[]", `{"access_token":""}`,
		`{"access_token":"a","token_type":"Bearer","expires_in":3600}`,
		`{"access_token":"a","token_type":"mac"}`,
		`{"access_token":"a","expires_in":-1}`,
		`{"error":"invalid_grant"}`,
		`{"error":"` + strings.Repeat("e", maxErrorCodeLen+1) + `"}`,
		`{"access_token":"a","refresh_token":"r","scope":"one two"}`,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("decodeTokenResponse panicked on a %d-byte body: %v", len(raw), r)
			}
		}()
		tokens, err := decodeTokenResponse([]byte(raw))
		if err != nil {
			if !tokens.access.Empty() || !tokens.refresh.Empty() {
				t.Fatal("a refused token response still carried token material")
			}
			return
		}
		if tokens.access.Empty() {
			t.Fatalf("a %d-byte body decoded successfully with no access token", len(raw))
		}
		if tokens.expiresIn < 0 {
			t.Fatalf("a negative expires_in (%d) was accepted", tokens.expiresIn)
		}
	})
}
