// Package policy (classifier_words.go): Purpose: resolution of the parts of
//   a command that are not its argv: words (with their quoting and
//   escaping), output redirections, and environment prefixes.
// Inputs: AST words, redirections and assignments.
// Outputs: the literal string a word will expand to when that can be known,
//   and the risk a redirection or an environment prefix adds on its own.
// Constraints: resolution is exact or it fails. mvdan.cc/sh keeps
//   backslashes in literal values, so unescaping is this classifier's job:
//   without it `\rm` and `r"m"` reach the table as strings that miss the
//   `rm` row while still running rm. A word carrying any expansion the
//   parser cannot evaluate statically (a parameter, a substitution, an
//   arithmetic expression, a glob) resolves to nothing, and every caller
//   treats that as a refusal rather than as an empty string. Redirections
//   that write are at least a workspace mutation even when the command in
//   front of them only reads, and an environment prefix that changes how a
//   command is RESOLVED or LOADED is destructive-privileged: it can turn
//   any invocation into an attacker's binary.
// SPORT: internal/policy resolveWord/ADDED (P1-E09-W2-S17-T3).
package policy

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// nodeTypeName returns the AST node's Go type name, used to name an
// unmodeled form in a refusal message.
func nodeTypeName(n syntax.Node) string { return fmt.Sprintf("%T", n) }

// quoteName renders a command name for a message without letting its
// contents run together with the surrounding prose.
func quoteName(name string) string { return "\"" + name + "\"" }

// resolveWord returns the literal value of a word, and whether that value
// is knowable without running the shell.
func resolveWord(w *syntax.Word) (string, bool) {
	if w == nil || len(w.Parts) == 0 {
		return "", false
	}
	var out strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			out.WriteString(unescapeUnquoted(p.Value))
		case *syntax.SglQuoted:
			if p.Dollar {
				// $'...' carries C-style escapes, which are a second
				// encoding this classifier does not decode.
				return "", false
			}
			out.WriteString(p.Value)
		case *syntax.DblQuoted:
			inner, ok := resolveDblQuoted(p)
			if !ok {
				return "", false
			}
			out.WriteString(inner)
		default:
			return "", false
		}
	}
	return out.String(), true
}

// resolveDblQuoted resolves the interior of a double-quoted section.
func resolveDblQuoted(q *syntax.DblQuoted) (string, bool) {
	var out strings.Builder
	for _, part := range q.Parts {
		lit, ok := part.(*syntax.Lit)
		if !ok {
			return "", false
		}
		out.WriteString(unescapeDouble(lit.Value))
	}
	return out.String(), true
}

// unescapeUnquoted removes backslash escaping outside quotes, where a
// backslash removes any special meaning from the character that follows.
func unescapeUnquoted(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			out.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) {
			i++
			out.WriteByte(s[i])
		}
	}
	return out.String()
}

// dblQuoteEscapable is the set of characters a backslash escapes inside
// double quotes. Before any other character the backslash is literal.
const dblQuoteEscapable = "$`\"\\\n"

// unescapeDouble removes backslash escaping inside double quotes.
func unescapeDouble(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && strings.IndexByte(dblQuoteEscapable, s[i+1]) >= 0 {
			i++
			out.WriteByte(s[i])
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

// writeRedirOps are the redirection operators that write.
var writeRedirOps = map[syntax.RedirOperator]bool{
	syntax.RdrOut: true, syntax.AppOut: true, syntax.RdrInOut: true,
	syntax.ClbOut: true, syntax.RdrAll: true, syntax.AppAll: true,
	syntax.DplOut: true,
}

// readRedirOps are the redirection operators that only supply input.
var readRedirOps = map[syntax.RedirOperator]bool{
	syntax.RdrIn: true, syntax.DplIn: true, syntax.Hdoc: true,
	syntax.DashHdoc: true, syntax.WordHdoc: true,
}

// harmlessTargets are write targets that discard or re-address output
// rather than change the machine.
var harmlessTargets = map[string]bool{
	"/dev/null": true, "/dev/stdout": true, "/dev/stderr": true,
	"1": true, "2": true, "-": true,
}

// redirectLevel returns the risk a statement's redirections add. A write
// is at least a workspace mutation, and a write whose target cannot be
// read is refused: an unknown destination is exactly the case that must
// not be waved through.
func redirectLevel(redirs []*syntax.Redirect) (RiskLevel, error) {
	worst := L0
	for _, r := range redirs {
		if r == nil {
			return L4, newUnknownError("the command has an empty redirection")
		}
		if readRedirOps[r.Op] {
			continue
		}
		if !writeRedirOps[r.Op] {
			return L4, newUnknownError("the command uses a redirection operator this classifier does not model")
		}
		target, ok := resolveWord(r.Word)
		if !ok {
			return L4, newUnknownError(
				"the target of an output redirection is not a static literal, so what would be written cannot be determined")
		}
		if harmlessTargets[target] {
			continue
		}
		worst = maxLevel(worst, L2)
	}
	return worst, nil
}

// loaderVars are the environment variables that change how a command is
// resolved or loaded. Setting one in a command prefix can substitute an
// attacker's binary or library for the one the argv names, so a prefix
// that sets one is destructive-privileged whatever the command is.
var loaderVars = map[string]bool{
	"PATH": true, "IFS": true, "SHELL": true, "ENV": true, "BASH_ENV": true,
	"PS4": true, "GLOBIGNORE": true, "LD_PRELOAD": true, "LD_LIBRARY_PATH": true,
	"LD_AUDIT": true, "DYLD_INSERT_LIBRARIES": true, "DYLD_LIBRARY_PATH": true,
	"DYLD_FRAMEWORK_PATH": true,
}

// assignmentLevel returns the risk a command's environment prefix adds.
// An ordinary assignment is local development state; a loader variable is
// refused at L4.
func assignmentLevel(assigns []*syntax.Assign) (RiskLevel, error) {
	if len(assigns) == 0 {
		return L0, nil
	}
	for _, a := range assigns {
		if a == nil || a.Name == nil {
			return L4, newUnknownError("the command has an assignment this classifier cannot read")
		}
		if loaderVars[a.Name.Value] {
			return L4, newUnknownError("the assignment to " + quoteName(a.Name.Value) +
				" changes how commands are resolved or loaded")
		}
	}
	return L1, nil
}
