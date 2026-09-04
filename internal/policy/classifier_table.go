// Package policy (classifier_table.go): Purpose: the 06-FORGE-SPEC §5.15
//   classification table: which command lands on which rung.
// Inputs: a resolved command base name and its argument words.
// Outputs: an ActionClass resolved to a RiskLevel, or a refusal.
// Constraints: the table is an ALLOW LIST of commands whose effect can be
//   decided from argv alone. A command that embeds an interpreter
//   (awk, sed, perl, python, node, ruby, php, osascript) is deliberately
//   absent: its argument is a program this classifier cannot read, so it
//   must be refused rather than guessed at. Absence is the fail-closed
//   answer, so a missing entry costs a refusal, never a permissive rung.
//   Where a command's effect depends on a subcommand or a flag, the rule
//   says so, and an argument that is not a static literal refuses the
//   whole invocation rather than classifying on the arguments it could
//   read.
// SPORT: internal/policy commandTable/ADDED (P1-E09-W2-S17-T3).
package policy

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// commandRule is one row of the table.
type commandRule struct {
	// class applies when the rule needs no refinement.
	class ActionClass
	// subcommands, when set, means the first operand chooses the class.
	// A missing or unresolvable operand refuses the invocation.
	subcommands map[string]ActionClass
	// argEscalations, when set, means these flags raise the class. A rule
	// with escalations refuses any argument it cannot read, because it
	// cannot then prove the escalating flag is absent.
	argEscalations map[string]ActionClass
}

// level resolves the rule against one invocation.
func (r commandRule) level(base string, args []*syntax.Word) (RiskLevel, error) {
	class := r.class
	if r.subcommands != nil {
		sub, ok := firstOperand(args)
		if !ok {
			return L4, newUnknownError("the subcommand of " + quoteName(base) +
				" is missing or is not a static literal")
		}
		class, ok = r.subcommands[sub]
		if !ok {
			return L4, newUnknownError(quoteName(base+" "+sub) +
				" is not in the risk table, so its effects are unknown")
		}
	}
	if r.argEscalations != nil {
		for _, arg := range args {
			value, ok := resolveWord(arg)
			if !ok {
				return L4, newUnknownError("an argument of " + quoteName(base) +
					" is not a static literal, and this command's level depends on its arguments")
			}
			if flag, _, found := strings.Cut(value, "="); found {
				value = flag
			}
			if escalated, hit := r.argEscalations[value]; hit && escalated > class {
				class = escalated
			}
		}
	}
	return class.Risk(), nil
}

// firstOperand returns the first argument that is not an option flag. It
// refuses as soon as it meets an argument it cannot read, since that
// argument might have been the operand.
func firstOperand(args []*syntax.Word) (string, bool) {
	for _, arg := range args {
		value, ok := resolveWord(arg)
		if !ok {
			return "", false
		}
		if strings.HasPrefix(value, "-") {
			continue
		}
		return value, true
	}
	return "", false
}

// simpleRules builds table rows for a list of names that share one class.
func simpleRules(class ActionClass, names ...string) map[string]commandRule {
	out := make(map[string]commandRule, len(names))
	for _, name := range names {
		out[name] = commandRule{class: class}
	}
	return out
}

// commandTable is the §5.15 table. Rows are grouped by rung so a reader can
// check a group against the spec sentence for that rung.
var commandTable = buildCommandTable()

func buildCommandTable() map[string]commandRule {
	table := map[string]commandRule{}
	groups := []map[string]commandRule{
		// L0, read-only.
		simpleRules(ClassRead,
			"ls", "cat", "head", "tail", "wc", "pwd", "echo", "printf", "grep",
			"egrep", "fgrep", "rg", "sort", "uniq", "cut", "tr", "comm", "diff",
			"cmp", "file", "stat", "du", "df", "date", "whoami", "id", "hostname",
			"uname", "which", "dirname", "basename", "realpath", "readlink",
			"tree", "ps", "printenv", "md5sum", "shasum", "sha1sum", "sha256sum",
			"cksum", "column", "base64", "seq", "nl", "tac", "strings", "od",
			"xxd", "hexdump", "true", "false"),
		// L1, safe local development.
		simpleRules(ClassLocalDev,
			"golangci-lint", "staticcheck", "shellcheck", "pytest", "eslint",
			"tsc", "gotestsum"),
		// L2, workspace mutation.
		simpleRules(ClassWorkspaceMutation,
			"mkdir", "touch", "cp", "mv", "ln", "tee", "patch", "chmod"),
		// L3, external side effect.
		simpleRules(ClassExternalSideEffect,
			"curl", "wget", "scp", "rsync", "gh", "ping", "dig", "nslookup",
			"host", "ftp", "sftp"),
		// L4, destructive or privileged. A command here is refused by NAME
		// rather than by absence, so the refusal message can say why.
		simpleRules(ClassDestructivePrivileged,
			"rm", "rmdir", "shred", "dd", "mkfs", "fdisk", "diskutil", "parted",
			"sudo", "doas", "su", "chown", "chgrp", "chflags", "mount", "umount",
			"kill", "killall", "pkill", "shutdown", "reboot", "halt", "poweroff",
			"systemctl", "launchctl", "service", "crontab", "at", "iptables",
			"nft", "passwd", "useradd", "userdel", "usermod", "groupadd",
			"visudo", "csrutil", "spctl", "nvram", "kextload", "insmod", "rmmod",
			"modprobe", "sysctl", "chroot", "truncate", "eval", "exec", "source",
			".", "trap", "alias", "unalias", "nc", "ncat", "netcat", "socat",
			"telnet", "osascript", "open", "xdg-open"),
		refinedRules(),
	}
	for _, group := range groups {
		for name, rule := range group {
			table[name] = rule
		}
	}
	return table
}

// writeFlags are the in-place rewrite flags that turn a formatter from a
// reader into a workspace mutation.
var writeFlags = map[string]ActionClass{
	"-w": ClassWorkspaceMutation, "-i": ClassWorkspaceMutation,
	"--write": ClassWorkspaceMutation, "--in-place": ClassWorkspaceMutation,
	"--fix": ClassWorkspaceMutation,
}

// findFlags are the find(1) actions that run a command or delete files.
var findFlags = map[string]ActionClass{
	"-exec": ClassDestructivePrivileged, "-execdir": ClassDestructivePrivileged,
	"-ok": ClassDestructivePrivileged, "-okdir": ClassDestructivePrivileged,
	"-delete": ClassDestructivePrivileged,
}

// refinedRules holds the rows whose class depends on a subcommand or a
// flag rather than on the command name alone.
func refinedRules() map[string]commandRule {
	return map[string]commandRule{
		"gofmt":     {class: ClassRead, argEscalations: writeFlags},
		"goimports": {class: ClassRead, argEscalations: writeFlags},
		"prettier":  {class: ClassRead, argEscalations: writeFlags},
		"find":      {class: ClassRead, argEscalations: findFlags},
		"git":       {subcommands: gitSubcommands},
		"go":        {subcommands: goSubcommands},
		"cargo":     {subcommands: cargoSubcommands},
	}
}

// gitSubcommands maps git's verbs onto the ladder. Verbs that can destroy
// uncommitted work (clean, reset, filter-branch) sit at L4 with the other
// destructive operations rather than with the ordinary mutations, and
// every verb that talks to a remote sits at L3.
var gitSubcommands = map[string]ActionClass{
	"status": ClassRead, "log": ClassRead, "diff": ClassRead, "show": ClassRead,
	"blame": ClassRead, "describe": ClassRead, "ls-files": ClassRead,
	"rev-parse": ClassRead, "shortlog": ClassRead, "grep": ClassRead,
	"cat-file": ClassRead, "check-ignore": ClassRead, "version": ClassRead,
	"add": ClassWorkspaceMutation, "commit": ClassWorkspaceMutation,
	"checkout": ClassWorkspaceMutation, "switch": ClassWorkspaceMutation,
	"restore": ClassWorkspaceMutation, "branch": ClassWorkspaceMutation,
	"merge": ClassWorkspaceMutation, "rebase": ClassWorkspaceMutation,
	"stash": ClassWorkspaceMutation, "tag": ClassWorkspaceMutation,
	"rm": ClassWorkspaceMutation, "mv": ClassWorkspaceMutation,
	"init": ClassWorkspaceMutation, "apply": ClassWorkspaceMutation,
	"cherry-pick": ClassWorkspaceMutation, "revert": ClassWorkspaceMutation,
	"am": ClassWorkspaceMutation, "config": ClassWorkspaceMutation,
	"worktree": ClassWorkspaceMutation, "notes": ClassWorkspaceMutation,
	"bisect": ClassWorkspaceMutation, "gc": ClassWorkspaceMutation,
	"push": ClassExternalSideEffect, "pull": ClassExternalSideEffect,
	"fetch": ClassExternalSideEffect, "clone": ClassExternalSideEffect,
	"remote": ClassExternalSideEffect, "submodule": ClassExternalSideEffect,
	"ls-remote": ClassExternalSideEffect, "send-email": ClassExternalSideEffect,
	"clean": ClassDestructivePrivileged, "reset": ClassDestructivePrivileged,
	"filter-branch": ClassDestructivePrivileged, "prune": ClassDestructivePrivileged,
}

// goSubcommands covers the go tool. run, generate and tool are absent on
// purpose: each executes code chosen by a file this classifier never sees.
var goSubcommands = map[string]ActionClass{
	"version": ClassRead, "env": ClassRead, "list": ClassRead, "doc": ClassRead,
	"build": ClassLocalDev, "test": ClassLocalDev, "vet": ClassLocalDev,
	"fmt": ClassWorkspaceMutation, "mod": ClassWorkspaceMutation,
	"work": ClassWorkspaceMutation, "clean": ClassWorkspaceMutation,
	"install": ClassWorkspaceMutation,
	"get":     ClassExternalSideEffect, "download": ClassExternalSideEffect,
}

// cargoSubcommands covers cargo. run is absent for the same reason go run
// is: it builds and then executes whatever the manifest names.
var cargoSubcommands = map[string]ActionClass{
	"version": ClassRead, "tree": ClassRead, "metadata": ClassRead,
	"build": ClassLocalDev, "check": ClassLocalDev, "test": ClassLocalDev,
	"clippy": ClassLocalDev, "bench": ClassLocalDev, "doc": ClassLocalDev,
	"fmt": ClassWorkspaceMutation, "fix": ClassWorkspaceMutation,
	"add": ClassWorkspaceMutation, "remove": ClassWorkspaceMutation,
	"update": ClassWorkspaceMutation, "clean": ClassWorkspaceMutation,
	"publish": ClassExternalSideEffect, "install": ClassExternalSideEffect,
	"search": ClassExternalSideEffect, "yank": ClassExternalSideEffect,
}

// npmSubcommands covers the npm family's non-script verbs. run, test,
// start and exec are absent: each executes a script body that lives in
// package.json, which is not in the command string, so they are refused by
// the wrapper rule instead.
var npmSubcommands = map[string]ActionClass{
	"ls": ClassRead, "list": ClassRead, "view": ClassRead, "outdated": ClassRead,
	"install": ClassExternalSideEffect, "ci": ClassExternalSideEffect,
	"publish": ClassExternalSideEffect, "audit": ClassExternalSideEffect,
	"update": ClassExternalSideEffect, "ping": ClassExternalSideEffect,
	"link": ClassWorkspaceMutation, "init": ClassWorkspaceMutation,
}
