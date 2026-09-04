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

## Capabilities

A capability is a named permission the runtime exposes. Nothing in the
runtime can be reached by a name the capability registry does not hold.

Each capability carries three things:

| Field | Meaning |
|---|---|
| `Name` | The stable identifier, e.g. `memory.read`. Lowercase dot-separated segments of `[a-z0-9_-]`. |
| `Desc` | The description a user is shown when asked to reason about it. |
| `DefaultPolicy` | The action class the capability confers when nothing narrower applies. |

`DefaultPolicy` is an action class from the ladder above, not a second
enum, so there is one ladder in the code rather than two that can drift.
It is read through a fail-closed accessor: a capability whose default
policy is unset or out of range reads as `destructive_privileged` (L4,
deny), never as `read`.

The registry surface is `Add`, `Remove`, `Lookup` and `List`. Lookup of a
name that is not registered is `capability-not-found`. So is a lookup of a
name the grammar forbids: a name that could never have been registered can
never match a stored key either. `Add` refuses a duplicate rather than
overwriting one, because silently replacing a capability would silently
replace the action class it confers. An empty registry permits nothing.

## Grants

A grant records that one subject holds one capability.

| Field | Meaning |
|---|---|
| `Subject` | `{Kind, ID}`, where kind is `user`, `agent` or `plugin`. There is no anonymous and no wildcard subject. |
| `Capability` | The registered capability name held. |
| `Conditions` | Key/value pairs the request must match exactly. Empty means unconditional. |
| `ScopeClass` | The widest reach the grant confers, in the corpus visibility vocabulary. |
| `ExpiresAt` | When the grant stops applying. Zero means it does not expire on its own. |

Grants persist in the `policy` storage domain — the eleventh domain, added
to the closed domain set for exactly this purpose. Standing grants,
deny-list patterns, autonomy-profile state and the classifier cache share
it. The approval-token single-use ledger stays in the `audit` domain.
Everything is written through the storage abstraction; nothing in the
policy layer opens a database.

### How a check is decided

`Check` reads the store on every call. There is no grant cache, which is
what makes revocation immediate: the decision after a `Revoke` finds
nothing. The steps, in order, are all deny steps:

1. The subject must name somebody, or the answer is `subject-unknown`.
2. The capability must be registered, or the answer is
   `capability-not-found`. This happens before any grant row is read, so a
   grant naming a capability that was removed can never be honoured.
3. A grant row must exist for exactly this subject and capability.
4. The row must decode and must validate after decoding. An unparseable
   row is a denial, never an allow.
5. The decoded row must name the subject and capability its key claims.
   The key is not trusted to describe its own contents.
6. The grant must not have expired, measured against an injected clock. A
   grant expires *at* its expiry instant, not after it.
7. Every condition on the grant must be matched exactly by the request's
   attributes. A missing attribute denies.

Every refusal collapses to one of three identifiers —
`capability-not-found`, `subject-unknown`, `grant-denied` — so a caller
cannot learn from a refusal whether a grant was never made, was revoked,
or expired. A refusal always comes back with the zero decision, whose
`Granted` field is false, so a caller that drops the error still denies.

## Shared visibility and the team carrier

The visibility classes themselves are not defined by the policy layer.
They belong to the corpus model, which owns the enum, its ordering by
reach, and the resolution of a record's class against its corpus:

| Class | Reach |
|---|---|
| `private` | The owning scope only. |
| `scope-local` | The owning scope's chain. |
| `shared` | The chain, plus scopes reached by a declared edge. |
| `team` | Content intended to be carried to everyone who has the repository. |

The policy layer adds the team carrier: the rule that decides whether one
already-resolved record may actually be carried to the team.

Three conditions must all hold, and each is checked separately so a
refusal says which one failed:

1. The record's trust level is `trusted`. Untrusted-source content is
   data, never something the user vouched for, so it is not carried to
   anyone else however its visibility class is marked. The corpus model
   already forces a record inside an untrusted corpus down to
   untrusted-source; this is the check that acts on that.
2. The resolved visibility class is `team`.
3. The privacy tier is not `personal`.

**A grant narrows; it never widens.** The effective reach of a record
under a grant is the narrower of the record's own class and the grant's
scope class. A `team`-class grant over a private record yields private. A
grant is never consulted for permission to raise a record, so there is no
ordering of the two in which one widens the other. A scope class that
cannot be read collapses the result to `private` rather than passing the
other side through.

## Dry-run simulation

`Engine.Simulate` answers "what would happen if I did this" without doing
it. The supervisor's escalation ladder and the secret detector both need
to pre-classify a prospective action, and doing that through the live path
would fill the audit log and the approval queue with questions nobody
asked.

### There is one implementation of the rules

`Simulate` does not predict a decision. It runs the same
`Engine.Evaluate` every live caller runs, on an engine that differs from
the live one in exactly one field — the approval sink — and reports what
came back. The verdict, the rung, the deciding layer and the explanation
in a report are the engine's own values, copied and not recomputed.

This matters more than it sounds. A simulator that carried its own copy of
the rules would be indistinguishable from a correct one right up until the
two drifted, and by then the user would have made a decision on the wrong
one.

### It writes nothing

Evaluation's only write is the approval enqueue an `ask` verdict
produces. During a simulation that sink is replaced by a discarding one,
which asks the live queue the read-only half of its admission question —
is this action admissible, would it coalesce with a prompt already open,
is there room — and files nothing. Nothing else on the path writes: the
capability registry, the grant store and the autonomy profile are reads,
and the single-use ledger and the audit log are reached only from the
queue's write half, which a simulation never enters.

A simulation therefore leaves the audit domain, the policy domain and the
pending set byte-identical to how it found them.

### It is conservative where it cannot be exact

Two states the live queue resolves by mutating first — closing a batch
whose window has elapsed, and pruning entries past their retention window
— are read as they stand. Both disagreements run in the same direction:
the report refuses where the live path might have admitted, never the
reverse. A dry run is never more permissive than the action itself.

An approval sink built outside `internal/policy` cannot be previewed at
all, because there is no way to know that its enqueue does not write. Such
a sink is refused rather than called, which downgrades the report to
`deny`.

### It does not widen what the caller can see

A report carries the simulated subject's own grants on the capability that
was asked about, and nothing else. A request that failed at the subject or
capability gate carries no grants at all, so a simulation cannot be used
to enumerate another principal's entitlements or to probe the registry.

### The report

| Field | Meaning |
| --- | --- |
| `verdict` | The canonical verdict the live path would reach: allow, ask or deny. |
| `elevation_required` | The action's verb is elevation-class (§5.14). A flag, never a fourth verdict value; derived from the RPC layer's canonical table. |
| `risk_level` | The rung actually evaluated, after the capability's own class was folded in. Never lower than the requested rung. |
| `matched_rule` | The layer that decided, e.g. `standing-grant`. |
| `applicable_grants` | The subject's own grants on this capability, with the deciding one marked. |
| `explanation` | The engine's own reason for the decision. |
| `auto_advance` | Whether an autonomous loop could proceed without a human turn. |
| `effective_scope` | How far this action's material could travel once the resolved sensitivity tier narrows the grant's reach. |
| `would_emit_audit` | Whether the live path would have written an audit row. Informational: this run wrote none. |

One field is deliberately absent from a report that a live outcome
carries: the approval request id. The live path files an entry and quotes
its id; a simulation files nothing, and minting an id would hand the
caller a reference to an approval nobody can ever answer.

### Fail-closed

An action whose class cannot be resolved lands on L4 and denies — the
level enum has no permissive zero, so a request whose level was never set
is treated as unclassifiable rather than as a read.

The sensitivity tier a caller asks to simulate under is subject to the
same rule. A tier the model does not recognise, including an unset one, is
unresolvable, and an unresolvable tier is the restricted one. There is no
value of that field, present or absent, that widens the reported reach:
the tier narrows the grant's own class through the same choke point the
live carrier narrows through.
