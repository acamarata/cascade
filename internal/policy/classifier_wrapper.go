// Package policy (classifier_wrapper.go): Purpose: indirection resolution:
//
//	the 06-FORGE-SPEC §5.15 wrapper rule max(wrapper, resolved-inner), and
//	the word, redirection and environment-prefix resolution every
//	indirection depends on.
//
// Inputs: a wrapper command name and its argument words; individual AST
//
//	words, redirections and assignments.
//
// Outputs: a RiskLevel for the whole wrapper form, resolved literal
//
//	strings, or a refusal.
//
// Constraints: §5.15 says an unresolvable inner makes the whole form L4.
//
//	That is the load-bearing rule here, and it decides three questions
//	this file answers the same way every time: an inner that is not a
//	static literal is refused, an inner whose body is not in the command
//	string at all (a make recipe, an npm script) is refused, and a wrapper
//	with no inner at all (an interactive shell, a bare ssh session) is
//	refused. Word resolution unescapes deliberately: mvdan.cc/sh keeps
//	backslashes in literal values, so `\rm` and `r\m` reach this code as
//	distinct strings that must both resolve to `rm` or the table can be
//	walked straight past.
//
// SPORT: internal/policy classifyWrapper/ADDED (P1-E09-W2-S17-T3).
package policy

import (
	"context"
	"path"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// maxWrapperDepth bounds re-parsing of wrapper strings.
const maxWrapperDepth = 8

// wrapperTable maps a wrapper command to the class the wrapper itself
// carries, before the inner command is taken into account.
var wrapperTable = map[string]ActionClass{
	"sh": ClassLocalDev, "bash": ClassLocalDev, "zsh": ClassLocalDev,
	"dash": ClassLocalDev, "ksh": ClassLocalDev,
	"xargs": ClassLocalDev,
	"make":  ClassWorkspaceMutation,
	"npm":   ClassLocalDev, "pnpm": ClassLocalDev, "yarn": ClassLocalDev,
	"ssh": ClassExternalSideEffect,
}

// classifyWrapper applies max(wrapper, resolved-inner) for one wrapper form.
func (c mvdanClassifier) classifyWrapper(
	ctx context.Context, base string, args []*syntax.Word,
) (RiskLevel, error) {
	wrapperLevel := wrapperTable[base].Risk()
	var inner RiskLevel
	var err error
	switch base {
	case "sh", "bash", "zsh", "dash", "ksh":
		inner, err = c.shellInner(ctx, base, args)
	case "xargs":
		var rest []*syntax.Word
		if rest, err = stripXargsFlags(args); err == nil {
			inner, err = c.commandInner(ctx, base, rest)
		}
	case "ssh":
		inner, err = c.sshInner(ctx, args)
	case "npm", "pnpm", "yarn":
		inner, err = npmInner(base, args)
	case "make":
		// A make target names a recipe in a Makefile. The recipe is not
		// in the command string, so there is no inner to resolve.
		err = newUnknownError("the recipe " + quoteName(base) +
			" would run is not in the command string, so it cannot be classified")
	default:
		err = newUnknownError("the wrapper " + quoteName(base) + " has no resolver")
	}
	if err != nil {
		return L4, err
	}
	return maxLevel(wrapperLevel, inner), nil
}

// shellInner resolves the -c string of a nested shell and classifies it.
func (c mvdanClassifier) shellInner(
	ctx context.Context, base string, args []*syntax.Word,
) (RiskLevel, error) {
	for i, arg := range args {
		value, ok := resolveWord(arg)
		if !ok {
			return L4, newUnknownError("an argument of " + quoteName(base) +
				" is not a static literal, so the shell's input cannot be read")
		}
		if value != "-c" {
			continue
		}
		if i+1 >= len(args) {
			return L4, newUnknownError(quoteName(base+" -c") + " has no command string")
		}
		script, ok := resolveWord(args[i+1])
		if !ok || strings.TrimSpace(script) == "" {
			return L4, newUnknownError("the " + quoteName(base+" -c") +
				" command string is empty or is not a static literal")
		}
		return c.deeper(ctx, script)
	}
	return L4, newUnknownError(quoteName(base) +
		" runs an interactive shell or a script file, neither of which is in the command string")
}

// deeper classifies a wrapper's inner command string, bounded so nested
// wrappers cannot recurse without limit.
func (c mvdanClassifier) deeper(ctx context.Context, script string) (RiskLevel, error) {
	if c.depth >= maxWrapperDepth {
		return L4, newUnknownError("the command nests wrappers more deeply than can be resolved")
	}
	return mvdanClassifier{depth: c.depth + 1}.Classify(ctx, script)
}

// commandInner classifies an inner argv handed to a wrapper.
func (c mvdanClassifier) commandInner(
	ctx context.Context, wrapper string, args []*syntax.Word,
) (RiskLevel, error) {
	if len(args) == 0 {
		return L4, newUnknownError(quoteName(wrapper) +
			" has no inner command in the command string")
	}
	name, ok := resolveWord(args[0])
	if !ok || name == "" {
		return L4, newUnknownError("the inner command of " + quoteName(wrapper) +
			" is not a static literal")
	}
	return c.classifyInvocation(ctx, path.Base(name), args[1:])
}

// sshInner splits an ssh invocation into its destination and the remote
// command, refusing an interactive session because it has no inner.
func (c mvdanClassifier) sshInner(ctx context.Context, args []*syntax.Word) (RiskLevel, error) {
	rest, err := stripSSHFlags(args)
	if err != nil {
		return L4, err
	}
	if len(rest) < 2 {
		return L4, newUnknownError(
			"ssh opens an interactive remote session, whose commands are not in the command string")
	}
	return c.commandInner(ctx, "ssh", rest[1:])
}

// npmInner classifies an npm-family invocation. Script-running verbs are
// refused: the script body lives in package.json, not in the command.
func npmInner(base string, args []*syntax.Word) (RiskLevel, error) {
	sub, ok := firstOperand(args)
	if !ok {
		return L4, newUnknownError("the subcommand of " + quoteName(base) +
			" is missing or is not a static literal")
	}
	switch sub {
	case "run", "run-script", "test", "start", "exec", "dlx":
		return L4, newUnknownError("the script body " + quoteName(base+" "+sub) +
			" would run lives in package.json, not in the command string")
	}
	class, known := npmSubcommands[sub]
	if !known {
		return L4, newUnknownError(quoteName(base+" "+sub) +
			" is not in the risk table, so its effects are unknown")
	}
	return class.Risk(), nil
}

// xargsValueFlags are the xargs options that consume the following token,
// which must not be mistaken for the inner command.
var xargsValueFlags = map[string]bool{
	"-n": true, "-I": true, "-i": true, "-P": true, "-a": true, "-d": true,
	"-E": true, "-L": true, "-s": true, "-J": true,
}

// stripXargsFlags returns the inner argv of an xargs invocation. An option
// it does not recognise refuses the form, since guessing whether it
// consumes the next token would mean guessing where the command starts.
func stripXargsFlags(args []*syntax.Word) ([]*syntax.Word, error) {
	for i := 0; i < len(args); i++ {
		value, ok := resolveWord(args[i])
		if !ok {
			return nil, newUnknownError("an argument of \"xargs\" is not a static literal")
		}
		if !strings.HasPrefix(value, "-") {
			return args[i:], nil
		}
		if xargsValueFlags[value] {
			i++
			continue
		}
		if !xargsNoValueFlags[value] {
			return nil, newUnknownError("the xargs option " + quoteName(value) +
				" is not recognised, so the inner command cannot be located")
		}
	}
	return nil, nil
}

// xargsNoValueFlags are the xargs options that stand alone.
var xargsNoValueFlags = map[string]bool{
	"-0": true, "-t": true, "-p": true, "-r": true, "-x": true,
	"--null": true, "--no-run-if-empty": true, "--verbose": true,
}

// sshValueFlags are the ssh options that consume the following token.
var sshValueFlags = map[string]bool{
	"-p": true, "-i": true, "-o": true, "-l": true, "-F": true, "-J": true,
	"-b": true, "-c": true, "-D": true, "-E": true, "-e": true, "-I": true,
	"-L": true, "-m": true, "-O": true, "-Q": true, "-R": true, "-S": true,
	"-W": true, "-w": true,
}

// stripSSHFlags returns the destination and remote command of an ssh
// invocation, refusing any option it does not recognise.
func stripSSHFlags(args []*syntax.Word) ([]*syntax.Word, error) {
	for i := 0; i < len(args); i++ {
		value, ok := resolveWord(args[i])
		if !ok {
			return nil, newUnknownError("an argument of \"ssh\" is not a static literal")
		}
		if !strings.HasPrefix(value, "-") {
			return args[i:], nil
		}
		if sshValueFlags[value] {
			i++
			continue
		}
		if !sshNoValueFlags[value] {
			return nil, newUnknownError("the ssh option " + quoteName(value) +
				" is not recognised, so the destination cannot be located")
		}
	}
	return nil, nil
}

// sshNoValueFlags are the ssh options that stand alone. An option in
// neither map refuses the form rather than being assumed harmless: an
// unrecognised option that consumed the next token would shift which word
// this code reads as the destination.
var sshNoValueFlags = map[string]bool{
	"-4": true, "-6": true, "-A": true, "-a": true, "-C": true, "-f": true,
	"-G": true, "-g": true, "-K": true, "-k": true, "-M": true, "-N": true,
	"-n": true, "-q": true, "-s": true, "-T": true, "-t": true, "-V": true,
	"-v": true, "-X": true, "-x": true, "-Y": true, "-y": true,
}
