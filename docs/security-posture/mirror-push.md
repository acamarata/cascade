# Mirror push

Cascade can push an accepted branch to one remote you name. It is off by default.

## What enabling needs

1. `[ci].mirror_remote` set to a git URL or `user@host:path` address. A bare remote name is refused, because the egress scope is the remote's host. A password or token in the address is refused.
2. The `ci.mirror` capability granted: `cascade policy grant ci.mirror`.
3. The `ci-mirror-push` egress class ships disabled. The push is admitted only for the host in `[ci].mirror_remote`.

With either of the first two missing, the step is skipped. A skip is not a failure and no git process starts.

## How it pushes

- Runs `git push --porcelain -- <remote> <tip>:refs/heads/<branch>` as the host's git user, through that user's credential helper or SSH agent.
- The child environment is filtered to PATH, HOME, USER, LOGNAME, SSH_AUTH_SOCK and a few locale and config variables. Terminal prompts are off.
- Before pushing, the remote ref is read. If the tip is already there, nothing is pushed. If the remote ref is not an ancestor of the tip, the push is refused.
- An intent effect record is written before the push and a confirm record after. Both name the attestation that authorized the acceptance. A crash between them leaves an open intent; recovery reads the remote ref and confirms without a second push.

## What it may never do

- Use `--mirror`, `--force`, `--delete` or a `+` refspec.
- Push tags or any ref other than the accepted branch tip.
- Write a token into the hook, repo config or child environment.
- Block or reverse an acceptance when the push fails. A failure is recorded, raises one attention item and leaves the job accepted.
- Be enabled by default.

## Watching it

`cascade doctor` reports `ci_mirror_backlog` as WARN while pushes are pending. Only mirror rows count; other integration-site outbox rows are ignored. Git-gate status reports the last push.
