package learn

// Purpose: the privacy rules of the learned-config store. Every free string
//   a Submission carries (label, scope, source id, change value, evidence
//   ids, a revert reason) passes ValidateNoIdentifiers before any SQL runs;
//   ExportFilter strips provenance from what leaves the store.
// Inputs: a field name and its value; a LearnedConfig.
// Outputs: nil or KindInvalidInput naming the field and the rule, never the
//   value (the value may be the very identifier the rule keeps out, and an
//   error is what callers log); a filtered LearnedConfig copy.
// Constraints: refused shapes are an e-mail or user@host, any URL with an
//   authority (https, ssh, file), a host name or IP address, an absolute
//   POSIX or home path, a Windows drive path, a UNC path, and anything the
//   package's credential scan flags. A refusal is preferred to a leak, so a
//   dotted token with an alphabetic last label reads as a host name.
// SPORT: internal.learn.ValidateNoIdentifiers/ADDED,
//   internal.learn.ExportFilter/ADDED (P1-LRN-01).

import (
	"regexp"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

var (
	// hostNamePattern is a dotted name whose last label is alphabetic.
	hostNamePattern = regexp.MustCompile(`(?i)^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z][a-z0-9-]*[a-z]$`)
	// ipv4Pattern is a dotted quad.
	ipv4Pattern = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,3}){3}$`)
	// ipv6Pattern is two or more colons over hex digits only.
	ipv6Pattern = regexp.MustCompile(`(?i)^[0-9a-f]*:[0-9a-f]*:[0-9a-f:]*$`)
	// drivePathPattern is a Windows drive path.
	drivePathPattern = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
)

// identifierRule pairs a refused shape with its test over one token.
type identifierRule struct {
	name string
	hit  func(tok string) bool
}

// identifierRules is the closed list, checked in order per token.
var identifierRules = []identifierRule{
	{"a URL with a host", func(tok string) bool { return strings.Contains(tok, "://") }},
	{"an e-mail address or user@host", func(tok string) bool {
		at := strings.Index(tok, "@")
		return at >= 0 && strings.ContainsAny(tok[at+1:], ".:")
	}},
	{"a UNC path", func(tok string) bool { return strings.HasPrefix(tok, `\\`) }},
	{"a Windows drive path", drivePathPattern.MatchString},
	{"an absolute or home path", func(tok string) bool {
		return (strings.HasPrefix(tok, "/") && len(tok) > 1) || tok == "~" || strings.HasPrefix(tok, "~/")
	}},
	{"a host name or IP address", func(tok string) bool {
		for _, part := range strings.FieldsFunc(tok, func(r rune) bool { return strings.ContainsRune(`/\:@`, r) }) {
			if hostNamePattern.MatchString(part) || ipv4Pattern.MatchString(part) {
				return true
			}
		}
		return ipv6Pattern.MatchString(tok)
	}},
}

// ValidateNoIdentifiers refuses (KindInvalidInput) a value that carries an
// identifying or locating string or a credential. The error names field
// and the rule, never value.
func ValidateNoIdentifiers(field, value string) error {
	if credentialShaped(value) {
		return cascade.Newf(cascade.KindInvalidInput, "learn: field %q must not carry a credential-shaped value", field)
	}
	for _, tok := range strings.FieldsFunc(value, isTokenBreak) {
		for _, r := range identifierRules {
			if r.hit(tok) {
				return cascade.Newf(cascade.KindInvalidInput, "learn: field %q must not carry %s", field, r.name)
			}
		}
	}
	return nil
}

// isTokenBreak splits a value into tokens on whitespace and TOML/shell
// punctuation, keeping the characters paths, hosts and URLs are made of.
func isTokenBreak(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || strings.ContainsRune("\"'`,[]{}()=;<>|", r)
}

// ExportFilter controls what an exported LearnedConfig carries. The zero
// value strips provenance.
type ExportFilter struct {
	IncludeProvenance bool
}

// Apply returns a copy of c; without IncludeProvenance it drops Source and
// Rollback.PreviousValue. c itself is never modified.
func (f ExportFilter) Apply(c LearnedConfig) LearnedConfig {
	out := c
	out.Areas = append([]DenylistArea(nil), c.Areas...)
	out.Evidence = append([]EvidenceRef(nil), c.Evidence...)
	if c.Rollback != nil {
		rb := *c.Rollback
		out.Rollback = &rb
	}
	if f.IncludeProvenance {
		return out
	}
	out.Source = SourceRef{}
	if out.Rollback != nil {
		out.Rollback.PreviousValue = ""
	}
	return out
}
