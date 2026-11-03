# Codex Driver

`providers/agents/codex` runs one agent job as one `codex app-server` child and speaks its stdio JSON-RPC. Fleet reaches it through the `codex` harness binding (`agent:codex`, plugin `cascade-codex`). The host shim is the only caller.

## Launch

| Item | Value |
|------|-------|
| argv | `[codex, "app-server"]`. Nothing else. `codex mcp-server` is refused with `ErrForbiddenLaunchPath`. |
| Environment | Exactly `SpawnEnv.Driver`. `HOME` must equal `SpawnEnv.Home`. `CODEX_HOME` is the profile auth dir. Any variable outside the driver allowlist refuses the spawn. |
| Working dir | `WorktreeRoot`. |
| Process group | Own group (`pkg/procgroup`). Its id is returned in `SpawnResult`. |
| Windows | Refused with `ErrPlatform`. |

## Session

1. `initialize` with `clientInfo {name "cascade", version}`. The response must report codex `0.144.2` (else `ErrHarnessIncompatible`) and a `codexHome` equal to `CODEX_HOME` (else `ErrCodexHomeMismatch`).
2. The `initialized` notification.
3. `thread/start {cwd, model?, sandbox, approvalPolicy "on-request", approvalsReviewer "user"}`.
4. `turn/start {threadId, input [{type text, text}], effort?}`.
5. Notifications until `turn/completed`.

| ToolPolicy | sandbox |
|------------|---------|
| none, readonly | `read-only` |
| all | `workspace-write` |

No flag, default or parameter can select `danger-full-access`, `never` or an automatic reviewer. `New` refuses a flag-shaped `DriverDefaults` value.

## Events and journal

- Every notification becomes an `AgentEvent` whose `Native` holds the method and the filtered raw frame. That includes `account/rateLimits/updated`, `thread/tokenUsage/updated` and methods the driver does not know. Nothing is dropped.
- `Collect().SessionID` is the thread id. `Journal()` returns `thread_id`, `user_agent` and `schema_revision` (`1/<codex version>`).
- Events, approval requests and the collect result all carry the job tier, `JoinSensitivity(DataClass, Envelope.Sensitivity)`.

## Server requests

Each request the schema names gets exactly one answer.

| Request | Answer |
|---------|--------|
| `item/commandExecution/requestApproval`, `execCommandApproval` | Surfaced as an `ApprovalRequest` with kind `command` |
| `item/fileChange/requestApproval`, `applyPatchApproval` | Surfaced as an `ApprovalRequest` with kind `file_change` |
| `item/permissions/requestApproval` | Surfaced as kind `permissions` with `Grant {Paths, Hosts}`. Answer: `{permissions, scope "turn"}`; a refusal is `{}`. |
| `item/tool/requestUserInput`, `mcpServer/elicitation/request`, `item/tool/call` | JSON-RPC error |
| `account/chatgptAuthTokens/refresh`, `attestation/generate` | JSON-RPC error. Never a credential or an attestation. |
| Any other method | JSON-RPC error, plus a NativeEvent |

The driver never answers an approval on its own. `ResolveApproval(id, token)` with a token answers `accept`. An empty token, the approval timeout (`DriverDefaults.ApprovalTimeout`) or `Cancel` answers `decline` or `cancel`. It then sends `turn/interrupt`, closes stdin and cancels the job. The legacy methods use `approved`, `denied` and `abort`. Session-scoped decisions and policy amendments are never written.

## Output filter and egress

Every stdout line passes the job's `OutputFilter` before it is stored or surfaced. A filter error fails the job with `ErrEgressSubstitutionFailed` and withholds its output.

The codex child opens its own TLS connections to its vendor. This driver does not see, inspect or decrypt that traffic. It sees only the stdio frames.

## Cancel

`Cancel` answers pending approvals with `cancel`, sends `turn/interrupt` and closes stdin. After `CancelGrace` it sends TERM to the group, and after another grace KILL. It returns once the child has exited, or `ErrCancelUnconfirmed`.

## Test data

`providers/agents/codex/testdata` holds the revision-1 schema, the replayed session and `capture.sh`. See its README for provenance. Both fixtures still need to be replaced with a capture from an installed codex CLI.
