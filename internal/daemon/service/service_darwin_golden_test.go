//go:build darwin

// Purpose: darwin Art.2 real-counterpart golden-fixture validation — split
//
//	out of service_darwin_test.go purely to satisfy Art.10.3's 300-line
//	file cap (R-14.117: a ticket may split a file it owns into additional
//	sibling files in the same package to satisfy the cap; the split is
//	behaviour-preserving, moved code only). Structurally parses
//	testdata/golden_launchd.plist (see testdata/README.md for its
//	plutil-conversion provenance) and renderLaunchdPlist's own output
//	using only the standard library's encoding/xml — never a golden-blob
//	string compare — and proves the escaping/marker guarantees
//	renderLaunchdPlist makes.
//
// SPORT: internal/daemon/service (ADD, per T-2 sport_updates).
package service

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// plistEntry is one top-level dict key/value pair.
type plistEntry struct {
	Key   string
	Kind  string // "true", "false", "string", "array"
	Value string
	Array []string
}

// parsePlistTopLevel structurally parses a launchd plist's top-level
// <dict>, in document order, using only the standard library's
// encoding/xml (this module has no plist-parsing dependency, and adding
// one is out of this ticket's files_scope). A malformed document (e.g. an
// unescaped XML-special character corrupting the file) fails here with a
// decode error, which is this test suite's structural well-formedness
// proof — never a golden byte/string comparison alone.
func parsePlistTopLevel(t *testing.T, data []byte) []plistEntry {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(data))
	st := &plistParseState{}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode plist: %v", err)
		}
		switch se := tok.(type) {
		case xml.StartElement:
			st.handleStart(t, dec, se)
		case xml.EndElement:
			if se.Name.Local == "dict" {
				st.depth--
			}
		}
	}
	return st.entries
}

// plistParseState is parsePlistTopLevel's decode state, split out purely
// to keep both functions under Art.10.3's 50-line-function cap.
type plistParseState struct {
	entries    []plistEntry
	depth      int
	pendingKey string
}

// handleStart processes one xml.StartElement token, appending a
// plistEntry to the top-level dict's list of entries when appropriate.
func (st *plistParseState) handleStart(t *testing.T, dec *xml.Decoder, se xml.StartElement) {
	t.Helper()
	switch se.Name.Local {
	case "dict":
		st.depth++
	case "key":
		if st.depth == 1 {
			var val string
			if err := dec.DecodeElement(&val, &se); err != nil {
				t.Fatalf("decode <key>: %v", err)
			}
			st.pendingKey = val
		}
	case "string":
		if st.depth == 1 && st.pendingKey != "" {
			var val string
			if err := dec.DecodeElement(&val, &se); err != nil {
				t.Fatalf("decode <string>: %v", err)
			}
			st.entries = append(st.entries, plistEntry{Key: st.pendingKey, Kind: "string", Value: val})
			st.pendingKey = ""
		}
	case "true", "false":
		if st.depth == 1 && st.pendingKey != "" {
			st.entries = append(st.entries, plistEntry{Key: st.pendingKey, Kind: se.Name.Local})
			st.pendingKey = ""
		}
	case "array":
		if st.depth == 1 && st.pendingKey != "" {
			st.entries = append(st.entries, plistEntry{Key: st.pendingKey, Kind: "array", Array: decodePlistArray(t, dec)})
			st.pendingKey = ""
		}
	}
}

func decodePlistArray(t *testing.T, dec *xml.Decoder) []string {
	t.Helper()
	var out []string
	for {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("decode plist array: %v", err)
		}
		switch se := tok.(type) {
		case xml.StartElement:
			if se.Name.Local == "string" {
				var val string
				if err := dec.DecodeElement(&val, &se); err != nil {
					t.Fatalf("decode array <string>: %v", err)
				}
				out = append(out, val)
			}
		case xml.EndElement:
			if se.Name.Local == "array" {
				return out
			}
		}
	}
}

func entryKeys(entries []plistEntry) map[string]plistEntry {
	out := make(map[string]plistEntry, len(entries))
	for _, e := range entries {
		out[e.Key] = e
	}
	return out
}

// TestDarwinPlistGolden_RequiredKeysPresent asserts every key present in
// the real-counterpart golden (Label, ProgramArguments, KeepAlive,
// RunAtLoad, StandardOutPath, StandardErrorPath) is also present, with a
// structurally matching kind, in renderLaunchdPlist's own output — never a
// self-authored key list, and never a golden-blob string compare.
func TestDarwinPlistGolden_RequiredKeysPresent(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("testdata", "golden_launchd.plist"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	goldenEntries := entryKeys(parsePlistTopLevel(t, golden))
	if len(goldenEntries) == 0 {
		t.Fatal("golden plist parsed to zero top-level keys — parser or fixture is broken")
	}

	cfg := testConfig(t, &fakeRunner{})
	generated := entryKeys(parsePlistTopLevel(t, renderLaunchdPlist(cfg)))

	var goldenKeys []string
	for k := range goldenEntries {
		goldenKeys = append(goldenKeys, k)
	}
	sort.Strings(goldenKeys)

	for _, key := range goldenKeys {
		wantKind := goldenEntries[key].Kind
		got, ok := generated[key]
		if !ok {
			t.Errorf("generated plist missing required key %q (from golden)", key)
			continue
		}
		if got.Kind != wantKind {
			t.Errorf("key %q kind = %q, want %q (matching golden)", key, got.Kind, wantKind)
		}
	}
}

// TestDarwinPlistGolden_ProgramArgumentsIsArrayOfStrings pins the one
// structural detail a flat key-presence check would miss: ProgramArguments
// must decode as an <array> of <string> elements in both the golden and
// the generated output.
func TestDarwinPlistGolden_ProgramArgumentsIsArrayOfStrings(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("testdata", "golden_launchd.plist"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	ge := entryKeys(parsePlistTopLevel(t, golden))["ProgramArguments"]
	if ge.Kind != "array" || len(ge.Array) == 0 {
		t.Fatalf("golden ProgramArguments = %+v, want a non-empty array", ge)
	}

	cfg := testConfig(t, &fakeRunner{})
	genEntries := entryKeys(parsePlistTopLevel(t, renderLaunchdPlist(cfg)))
	pe := genEntries["ProgramArguments"]
	if pe.Kind != "array" || len(pe.Array) == 0 {
		t.Fatalf("generated ProgramArguments = %+v, want a non-empty array", pe)
	}
	if pe.Array[0] != cfg.Executable {
		t.Errorf("ProgramArguments[0] = %q, want %q", pe.Array[0], cfg.Executable)
	}
}

// TestDarwinPlist_Escaping proves an executable/log path containing an
// XML-special character, a quote, and a space survives the round trip:
// the document still decodes (well-formedness), and the decoded value
// equals the original raw string exactly (no corruption, no truncation).
func TestDarwinPlist_Escaping(t *testing.T) {
	tricky := []string{
		`/opt/cascade bin/cascade`,          // space
		`/opt/"cascade"/bin/cascade`,        // double quote
		`/opt/cascade & co/bin/cascade`,     // ampersand
		`/opt/cascade <daemon>/bin/cascade`, // angle brackets
		"/opt/cascade's-dir/bin/cascade",    // apostrophe
	}
	for _, exe := range tricky {
		t.Run(exe, func(t *testing.T) {
			cfg := testConfig(t, &fakeRunner{})
			cfg.Executable = exe
			cfg.LogPath = exe + ".log"

			data := renderLaunchdPlist(cfg)
			entries := entryKeys(parsePlistTopLevel(t, data))

			pa := entries["ProgramArguments"]
			if len(pa.Array) == 0 || pa.Array[0] != exe {
				t.Errorf("ProgramArguments[0] = %q, want %q", firstOrEmpty(pa.Array), exe)
			}
			if got := entries["StandardOutPath"].Value; got != cfg.LogPath {
				t.Errorf("StandardOutPath = %q, want %q", got, cfg.LogPath)
			}
		})
	}
}

func firstOrEmpty(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// TestDarwinPlist_ManagedMarkerPresent proves the clobber-refusal marker
// this package's writeManagedFile relies on is actually emitted.
func TestDarwinPlist_ManagedMarkerPresent(t *testing.T) {
	cfg := testConfig(t, &fakeRunner{})
	data := renderLaunchdPlist(cfg)
	if !isManagedPlist(data) {
		t.Error("renderLaunchdPlist output does not carry the managed marker")
	}
}
