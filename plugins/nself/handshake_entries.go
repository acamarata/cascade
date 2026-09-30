// Purpose (this file): the handshake's pure helpers (P1-E25-W5-S103-T1),
//
//	split from handshake.go for the 300-line cap: the descriptor-only
//	diff built from probed values, the credential-shape withhold, the
//	required env-ref presence check, the version floor, and the response
//	scrub.
//
// Inputs: probed nself config values and the invoking environment's
//
//	env-ref PRESENCE (never a value).
//
// Outputs: ConfigEntry literals, withheld path names, missing env-ref
//
//	names, and the scrubbed response.
//
// Constraints: literals are TOML-valid (tomlQuote escapes every control
//
//	character; tomlIntLiteral emits a canonical integer, so "05432"
//	becomes 5432). ApplyDiff re-validates every literal anyway; this
//	file's job is to never propose a credential.
//
// SPORT: plugins/nself handshake (ADD) — P1-E25-W5-S103-T1.

package nself

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// postgresDSNEnvRef is the env-ref whose export shape missing_env hints.
const postgresDSNEnvRef = "CASCADE_STORAGE_POSTGRES_DSN"

// postgresDSNShape is the contract's exact DSN shape, placeholders left
// unfilled. It is a code-chosen constant carrying no value, attached after
// the scrub (whose userinfo pattern would otherwise replace it).
const postgresDSNShape = "postgres://" + "<user>:<password>@" + "<postgres_host>:<postgres_port>/<postgres_db>"

// buildProposedEntries assembles the descriptor-only diff (never a
// credential) from dir and the probed config values.
func buildProposedEntries(dir string, values map[string]string) []ConfigEntry {
	prefix := "plugins." + handshakeOwner + "."
	entries := []ConfigEntry{
		{Path: prefix + "project_dir", Literal: tomlQuote(dir)},
		{Path: prefix + "pgvector", Literal: boolLiteral(strings.Contains(values["POSTGRES_EXTENSIONS"], "vector"))},
		{Path: prefix + "redis", Literal: boolLiteral(values["REDIS_ENABLED"] == "true")},
		{Path: prefix + "s3", Literal: boolLiteral(values["MINIO_ENABLED"] == "true")},
	}
	entries = appendIfSet(entries, prefix+"postgres_host", values["POSTGRES_HOST"], tomlQuote)
	entries = appendIfSet(entries, prefix+"postgres_port", values["POSTGRES_PORT"], tomlIntLiteral)
	entries = appendIfSet(entries, prefix+"postgres_db", values["POSTGRES_DB"], tomlQuote)
	entries = appendIfSet(entries, prefix+"redis_port", values["REDIS_PORT"], tomlIntLiteral)
	entries = appendIfSet(entries, prefix+"s3_bucket", values["S3_BUCKET"], tomlQuote)
	entries = appendIfSet(entries, prefix+"s3_port", values["MINIO_PORT"], tomlIntLiteral)
	return entries
}

func appendIfSet(entries []ConfigEntry, path, value string, literal func(string) string) []ConfigEntry {
	if value == "" {
		return entries
	}
	return append(entries, ConfigEntry{Path: path, Literal: literal(value)})
}

func boolLiteral(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// tomlIntLiteral renders v as a canonical TOML integer when it parses as
// one ("05432" -> 5432, which TOML would reject with the leading zero),
// else as a quoted string (never invents a value).
func tomlIntLiteral(v string) string {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		return strconv.Itoa(n)
	}
	return tomlQuote(v)
}

// tomlQuote renders s as a TOML basic string. strconv.Quote is not TOML:
// it emits \a and \x01, which TOML rejects.
func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// withholdCredentialShaped drops every entry whose literal payload.go's
// scrub would replace (a URL with userinfo, a secret-named pair, a bearer
// token, a bare hex token) or the bound LiteralScreener refuses (the
// config writer's own validator: a known-prefix or split secret), and
// returns the dropped paths, sorted. The screener's error text is never
// echoed: a withheld value is named only by its path.
func withholdCredentialShaped(entries []ConfigEntry, screen LiteralScreener) ([]ConfigEntry, []string) {
	kept := make([]ConfigEntry, 0, len(entries))
	var withheld []string
	for _, e := range entries {
		if scrub(e.Literal) != e.Literal || screen.ScreenLiteral(e.Path, e.Literal) != nil {
			withheld = append(withheld, e.Path)
			continue
		}
		kept = append(kept, e)
	}
	sort.Strings(withheld)
	return kept, withheld
}

// missingServerEnvRefs reports which requiredServerEnvRefs are unset in
// this process's environment: PRESENCE only, never a value.
func missingServerEnvRefs() []string {
	var missing []string
	for _, name := range requiredServerEnvRefs {
		if handshakeGetenv(name) == "" {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

// finishResponse scrubs every string in resp, then attaches the
// code-chosen DSN shape when the Postgres DSN env-ref is missing.
func finishResponse(resp handshakeResponse, missing []string) handshakeResponse {
	resp = resp.scrubbed()
	for _, name := range missing {
		if name == postgresDSNEnvRef {
			resp.DSNShape = postgresDSNShape
		}
	}
	return resp
}

// scrubbed returns r with every string field passed through scrub.
func (r handshakeResponse) scrubbed() handshakeResponse {
	r.Status, r.Note = scrub(r.Status), scrub(r.Note)
	for i := range r.Proposed {
		r.Proposed[i] = diffEntryWire{Path: scrub(r.Proposed[i].Path), Literal: scrub(r.Proposed[i].Literal)}
	}
	for _, list := range [][]outcomeWire{r.Applied, r.Unchanged, r.Skipped} {
		for i := range list {
			list[i] = outcomeWire{Path: scrub(list[i].Path), Reason: scrub(list[i].Reason)}
		}
	}
	for _, list := range [][]string{r.Withheld, r.MissingEnv} {
		for i := range list {
			list[i] = scrub(list[i])
		}
	}
	return r
}

// versionAtLeast reports whether got >= floor as dotted X.Y.Z integers
// (missing/non-numeric components compare as 0).
func versionAtLeast(got, floor string) bool {
	g, f := parseSemver(got), parseSemver(floor)
	for i := range g {
		if g[i] != f[i] {
			return g[i] > f[i]
		}
	}
	return true
}

func parseSemver(s string) [3]int {
	var out [3]int
	parts := strings.SplitN(s, ".", 3)
	for i := 0; i < len(parts) && i < 3; i++ {
		n, _ := strconv.Atoi(parts[i])
		out[i] = n
	}
	return out
}
