// Package build's identifier-sweep allow list.
//
// Allow patterns are separated from the deny sweep in sweep.go because they
// pull in the opposite direction and the distinction is worth keeping visible
// in the file layout. sweep.go answers "does this line name something that
// must not be public". This file answers the much narrower question "is this
// one line the rare case where the answer is yes and that is correct", which
// today means the copyright holder named in LICENSE.
package build

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// allowPrefix marks an allow pattern in a pattern source. A line beginning
// with it is compiled as an exemption rather than a denial.
const allowPrefix = "!"

// ParseAllowPatterns decodes the allow ("!"-prefixed) lines of a pattern
// source. Allow patterns exist for the narrow case where a denied string is
// REQUIRED to appear somewhere: the copyright holder's name in LICENSE is
// the motivating example, since a licence that does not name its holder is
// not a licence.
//
// An allow pattern suppresses a hit only when it matches the SAME line that
// tripped the denial. It is deliberately not a file-level exemption. Skipping
// the whole file would stop scanning it for every other pattern, so a leaked
// address or lane identifier buried in that file would sail through. Here the
// file stays fully scanned and only the one justified line is forgiven.
//
// Unlike ParsePatterns, an empty result is NOT an error: having no exemptions
// is the normal, stricter case.
func ParseAllowPatterns(raw string) ([]*regexp.Regexp, error) {
	var out []*regexp.Regexp
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, allowPrefix) {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, allowPrefix))
		if body == "" {
			return nil, errors.New("identifier sweep: empty allow pattern (fail closed)")
		}
		re, err := regexp.Compile(body)
		if err != nil {
			return nil, fmt.Errorf("identifier sweep: invalid allow pattern %q: %w", body, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// LoadAllowPatterns resolves allow patterns from the same source LoadPatterns
// uses, so both live in one file and cannot drift apart.
func LoadAllowPatterns() ([]*regexp.Regexp, error) {
	switch {
	case os.Getenv(IdentifierPatternsFileEnvVar) != "":
		data, err := os.ReadFile(os.Getenv(IdentifierPatternsFileEnvVar))
		if err != nil {
			return nil, fmt.Errorf("identifier sweep: reading pattern file: %w (fail closed)", err)
		}
		return ParseAllowPatterns(string(data))
	case os.Getenv(IdentifierPatternsEnvVar) != "":
		return ParseAllowPatterns(os.Getenv(IdentifierPatternsEnvVar))
	default:
		return nil, fmt.Errorf("identifier sweep: no pattern source configured (fail closed)")
	}
}

// FilterAllowed drops violations whose snippet is matched by an allow
// pattern. It reports how many it dropped so a caller can surface the count:
// an exemption that silently swallows hits is how a gate stops being a gate.
func FilterAllowed(violations []SweepViolation, allows []*regexp.Regexp) ([]SweepViolation, int) {
	if len(allows) == 0 {
		return violations, 0
	}
	var kept []SweepViolation
	dropped := 0
	for _, v := range violations {
		exempt := false
		for _, re := range allows {
			if re.MatchString(v.Snippet) {
				exempt = true
				break
			}
		}
		if exempt {
			dropped++
			continue
		}
		kept = append(kept, v)
	}
	return kept, dropped
}

// LoadPatternsFromFile reads and parses the pattern file at path.
func LoadPatternsFromFile(path string) ([]*regexp.Regexp, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("identifier sweep: reading pattern file %s: %w (fail closed)", path, err)
	}
	return ParsePatterns(string(data))
}

// LoadPatternsFromEnv reads and parses the pattern set from env var name.
func LoadPatternsFromEnv(name string) ([]*regexp.Regexp, error) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return nil, fmt.Errorf("identifier sweep: env var %s not set (fail closed)", name)
	}
	return ParsePatterns(raw)
}

// LoadPatterns resolves the external pattern source per this file's
// package doc (file pointer env var first, then inline CI env var). It
// returns an error — never an empty, silently-skipped result — when
// neither source is configured or the configured one fails to parse.
func LoadPatterns() ([]*regexp.Regexp, error) {
	switch {
	case os.Getenv(IdentifierPatternsFileEnvVar) != "":
		return LoadPatternsFromFile(os.Getenv(IdentifierPatternsFileEnvVar))
	case os.Getenv(IdentifierPatternsEnvVar) != "":
		return LoadPatternsFromEnv(IdentifierPatternsEnvVar)
	default:
		return nil, fmt.Errorf("identifier sweep: no pattern source configured (set %s or %s) (fail closed)",
			IdentifierPatternsFileEnvVar, IdentifierPatternsEnvVar)
	}
}
