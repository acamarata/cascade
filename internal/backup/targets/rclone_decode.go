// Purpose: this package's own decoders of two rclone output formats (06
// §5.7 — an own decoder of an external tool's output format requires a
// fuzz target): `rclone version`'s first line, and `rclone lsjson
// --recursive`'s JSON array. Both are exercised by FuzzRcloneVersionParse
// and FuzzRcloneListDecode in rclone_decode_test.go against seed corpus
// under testdata/fuzz/.
//
// Inputs: the raw stdout bytes rclone.go's runner captures.
// Outputs: a parsed RcloneVersion or []RcloneListEntry, or a typed
// refusal — never a panic on malformed input (that is exactly what
// fuzzing this file proves).
// Constraints: pure parsing, no I/O; every input, however malformed,
// returns an error rather than panicking.
//
// SPORT: internal.backup.targets.rclone/ADDED (P1-E19-W4-S41-T3).

package targets

import (
	"bufio"
	"encoding/json"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// RcloneVersion is the parsed result of `rclone version`'s first line
// ("rclone v1.66.0").
type RcloneVersion struct {
	// Raw is the exact first line, unparsed, for diagnostics.
	Raw string
	// Tag is the version tag with its leading "v" stripped ("1.66.0").
	Tag string
}

// ParseRcloneVersionOutput parses `rclone version`'s stdout, reading only
// its first non-empty line. A missing or unrecognized first line is a
// refusal, never a zero-value success: the doctor probe must be able to
// tell "no rclone" from "rclone but I could not parse it" from "rclone,
// parsed fine".
func ParseRcloneVersionOutput(output []byte) (RcloneVersion, error) {
	sc := bufio.NewScanner(strings.NewReader(string(output)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		return parseRcloneVersionLine(line)
	}
	return RcloneVersion{}, cascade.New(cascade.KindInvalidInput, "targets: rclone: version output has no non-empty line")
}

// parseRcloneVersionLine parses one candidate first line.
func parseRcloneVersionLine(line string) (RcloneVersion, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "rclone" || !strings.HasPrefix(fields[1], "v") {
		return RcloneVersion{}, cascade.Newf(cascade.KindInvalidInput, "targets: rclone: unrecognized version line %q", line)
	}
	tag := strings.TrimPrefix(fields[1], "v")
	if tag == "" {
		return RcloneVersion{}, cascade.Newf(cascade.KindInvalidInput, "targets: rclone: empty version tag in %q", line)
	}
	return RcloneVersion{Raw: line, Tag: tag}, nil
}

// RcloneListEntry is one entry from `rclone lsjson --recursive`'s JSON
// array — only the fields this package's List() needs.
type RcloneListEntry struct {
	Path  string `json:"Path"`
	IsDir bool   `json:"IsDir"`
}

// DecodeRcloneListOutput decodes `rclone lsjson --recursive`'s JSON
// array, returning every non-directory entry's Path. Malformed JSON is a
// refusal; an empty array ("[]", rclone's own output for an empty
// directory) is a valid empty result, not an error.
func DecodeRcloneListOutput(output []byte) ([]string, error) {
	var entries []RcloneListEntry
	if err := json.Unmarshal(output, &entries); err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "targets: rclone: decoding lsjson output")
	}
	var keys []string
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		if e.Path == "" {
			return nil, cascade.New(cascade.KindInvalidInput, "targets: rclone: lsjson entry has an empty Path")
		}
		keys = append(keys, e.Path)
	}
	return keys, nil
}

// filterAndSortByPrefix returns the sorted subset of keys with prefix.
func filterAndSortByPrefix(keys []string, prefix string) []string {
	var out []string
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
