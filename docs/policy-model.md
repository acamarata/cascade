# Policy model

How Cascade decides whether an action may run.

## Risk classification

Every shell command is put on a five-rung risk ladder before anything is
allowed to run it. The rung is resolved once, by the command classifier,
and the policy layers read it from there.

| Rung | What it means | Default disposition |
|---|---|---|
| L0 | Read-only. Nothing changes. | allow |
| L1 | Safe local development: tests, lint, build. | allow |
| L2 | Workspace mutation: staging, commits, writes to files. | ask |
| L3 | External side effect: push, pull request, network, messages. | ask, never automatically |
| L4 | Destructive or privileged. | deny, subject to same-turn authorization only |

Representative commands, one per rung:

| Rung | Example |
|---|---|
| L0 | `ls -la`, `git status`, `cat notes.txt` |
| L1 | `go test ./...`, `golangci-lint run ./...`, `go build ./...` |
| L2 | `git add .`, `git commit -m msg`, `mkdir build`, `echo x > notes.txt` |
| L3 | `git push origin main`, `gh pr create`, `curl https://example.com` |
| L4 | `rm -rf /tmp/data`, `sudo systemctl restart nginx`, `git reset --hard` |

### The classifier contract

```go
type CommandClassifier interface {
    Classify(ctx context.Context, cmd string) (RiskLevel, error)
}
```

`Classify` parses the command with a POSIX shell grammar and walks the
resulting syntax tree. It returns the rung the command sits on. When it
returns an error, the rung is always L4: a refusal is never paired with a
permissive answer.

The `RiskLevel` enum starts at 1 rather than 0. There is no permissive
zero value, so a field left unset reads as invalid rather than as
read-only, and every value passes through a helper that maps anything
invalid to L4.

### Fail-closed rules

The classifier refuses, at L4, in every one of these cases:

- The command does not parse.
- The command is empty or is only whitespace.
- The command name is not a static literal, so what would run cannot be
  determined. This covers variables (`$CMD`), command substitution
  (`$(echo rm)`) and C-style quoting (`$'\x72\x6d'`).
- The command uses a shell form the classifier does not model: functions,
  loops, conditionals, case clauses, arithmetic, process substitution,
  extended globs and the like.
- The command name, or a subcommand of it, is not in the risk table.
- An argument cannot be read on a command whose rung depends on its
  arguments.
- The target of an output redirection cannot be read.
- An environment prefix sets a variable that changes how a command is
  resolved or loaded (`PATH`, `LD_PRELOAD`, `DYLD_INSERT_LIBRARIES` and
  the rest of that family), because the argv then no longer says what
  will run.

A command that reaches none of those and is not in the table is refused
too. Absence from the table is a refusal, never a permissive default.

Two named refusals are distinguishable by the caller: one for input that
would not parse, one for input that parsed but was not recognised. Both
present as a policy denial.

### How a line combines

A line is as dangerous as its most dangerous part. Commands chained with
`&&`, `||`, `;` or a pipe, commands inside a subshell or a brace block,
and commands inside a substitution are all classified and the highest
rung wins. Quoting, partial quoting, backslash escaping and an absolute
or relative path in front of the command name change none of this: the
name is resolved the way a shell would resolve it before the table is
consulted.

An output redirection that writes raises the rung to at least L2 even
when the command in front of it only reads. Writes to `/dev/null` and
duplications of an existing descriptor do not.

### Wrapper and indirection forms

A wrapper is classified at the maximum of the wrapper's own rung and the
rung of the command it runs. An inner command that cannot be resolved
makes the whole form L4.

| Form | Wrapper rung | Inner |
|---|---|---|
| `sh -c`, and the bash/zsh/dash/ksh equivalents | L1 | the `-c` string, when it is a static literal |
| `xargs` | L1 | the argv after its options |
| `ssh` | L3 | the remote command after the destination |
| `npm`, `pnpm`, `yarn` | L1 | the verb, from a table of verbs |
| `make` | L2 | none |

`make` and the script-running npm verbs (`npm run`, `npm test`,
`yarn start` and so on) are always refused. Their body is a Makefile
recipe or a `package.json` script, neither of which is in the command
string, so there is no inner command to resolve at any level of effort.
An interactive shell or a bare `ssh` session is refused for the same
reason: the commands that would run are not in the string being
classified.

Nested wrappers are bounded. Beyond the bound the form is refused rather
than allowed to recurse.

### Windows

The classifier parses POSIX shell grammar. A command line written in that
grammar classifies identically on Windows and on Unix: there is one
table, compiled into every platform build, and no platform-specific
classification code.

PowerShell and cmd.exe are a different grammar. Cmdlets, cmd.exe
builtins, drive-letter paths and PowerShell's own variable and
redirection syntax are not something the classifier can reason about, so
it does not pretend to. Every Windows-native form falls through the same
table every other command reaches, misses, and is refused at L4. The
caller asks for permission and a person decides.
