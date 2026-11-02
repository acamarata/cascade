# Driver credential custody

How the daemon hands a credential to an external agent driver, and what that
does not protect. One host shim (`internal/providers/agents`, `spawn.go`) is
the only production caller of a driver's `Spawn`. Read the limits first.

## Limits

| Control | What it is | What it is not |
|---|---|---|
| Key separation | The key and the principal token reach the child as environment values the daemon built for that one spawn. Under ADR-0006 section 3 the caller principal protects model-facing surfaces against cross-session leakage and prompt-driven misuse. Key separation binds principal-bearing callers only, not same-uid shell code. | A boundary against code running as the same uid. Such code can read the child's environment, the profile directory and the process table. |
| Output filter | A value scrub of every injected secret, then the process egress engine, on the child's stdout and stderr. | A filter on the vendor CLI's own network traffic. |
| Vendor TLS egress | Not inspected (R-21.177). The harness opens its own TLS connections to its vendor. The per-spawn proxy ([driver-egress-proxy.md](driver-egress-proxy.md)) is advisory routing. | Inspected, filtered or firewalled. Do not describe it so. |
| Tool isolation | The tool environment omits the key and the config directory. | A sandbox for tools the harness runs. Deferred as `DEF-P2-driver-tool-isolation`. |

## Spawn order

`Shim.Spawn` runs these steps in order. Any refusal before the intent record
starts no child, leaves no profile directory, no proxy and no effect record.

1. Resolve the lane (`agent:<harness>`) to its binding; an absent or disabled owning plugin is unavailable.
2. Check the lease marker (`NewLeaseMarker` value), the principal token and the credential choice for the harness.
3. `Envelope.Check` for the worktree root at the injected clock's time.
4. Tool policy: the spec's policy must equal the one the envelope allows (`agent.tools.all`, `agent.tools.readonly`, else none). A wider policy refuses.
5. `AssertMinimalWorktree`: the tree holds only the declared prefixes, their parent directories, `.git` and `.claude`. A symlink must resolve inside the root onto an allowed path; an escaping, dangling or out-of-scope link refuses.
6. `RunPreSpawnScan` over the process detector (`ProcessDetectorMatch`). A hit is `ErrPreSpawnSecretFound`; a scan failure or an unavailable detector refuses too.
7. `ProvisionRuntimeProfile`, then the relay hook pack (`RenderAgentHookPack`).
8. Build the driver from its factory. Its `Executable()` must equal the configured absolute vendor path (or that path with symlinks resolved) and carry a basename the binding lists. A driver that cannot report its executable refuses.
9. Start the per-spawn proxy, then `BuildDriverEnv` for the exact driver and tool environments.
10. Bind the output filter at the join of the envelope's tier and the job's data class.
11. Mint a job id if empty. Write the effect intent (`agent.spawn:<job id>`, actor `agent-driver:<harness>`), call the driver, then confirm with `pgid=<n>`, or fail the intent.

`Release` cancels a live job, sweeps the lease marker, closes the proxy and removes the profile. It is idempotent and returns the first error.

## Credential table

A harness binding names its credential classes and the one child-side variable for each. Configuration chooses a class and a source; it never names a child variable.

| Harness | Class | Child variable | Source |
|---|---|---|---|
| claude | `oauth_token` | `CLAUDE_CODE_OAUTH_TOKEN` | daemon environment variable named by `key_env` |
| claude | `api_key` | `ANTHROPIC_API_KEY` | daemon environment variable named by `key_env` |
| any | `subscription` | none | the vendor CLI's own auth file, linked in place |

The daemon reads a key class only from its own environment. An unset `key_env` variable is a typed error naming it. The vault is never read for driver credentials.

## Environments

The child never inherits the daemon environment. `os.Environ` is banned under `providers/agents` (`TestAgentEnvGate`).

| Environment | Names |
|---|---|
| Driver | `PATH`, `HOME`, `TMPDIR`, `LANG`, `CASCADE_JOB_ID`, `CASCADE_SOCKET`, `CASCADE_PRINCIPAL_TOKEN`, `CASCADE_LEASE_ID`, `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`, the three lowercase proxy names, the vendor config directory variable and, for a key class, its key variable |
| Tool | the driver list minus the key variable and the config directory variable |

`HOME` and `TMPDIR` name the per-spawn profile. The proxy variables come from the per-spawn proxy alone; `NO_PROXY` stays empty. The principal token enters through `principal.BindEnv`, never argv.

## Runtime profile

One profile per spawn under `<profile base>/<harness>/<job id>`: `home`, `home/config` (the vendor config directory) and `tmp`. Every directory is mode 0700 and owned by the daemon user. The job directory must not exist before the spawn, and the home is never the daemon's own `HOME` or config directory. Profiles are unix only. The shim removes the profile on every refusal and on `Release`.

## In-place auth link

For a subscription class the daemon links the vendor auth file into the profile's config directory under its own basename. It never opens, reads or copies the file. Before linking it requires a regular file owned by the daemon user, not group or world writable, in a directory only the daemon user can write. After linking it confirms the file is still the one it checked. A vendor CLI that refuses a symlinked auth file is an escalation, not a reason to copy.

## Output filter

`WrapDriverOutput` replaces every occurrence of each injected secret (the key, the principal token and the proxy credential) with `[REDACTED:agent-driver-secret]` first, in raw, base64 (standard and URL alphabets, at each alignment) and hex spellings. Then each chunk goes through the egress engine's substitution pass for class `agent-driver`, which admits internal and public content and refuses restricted. An engine error withholds the chunk and returns `ErrEgressSubstitutionFailed`; the failure callback receives a reason with no content. A secret split across two chunks is not seen by the value scrub; drivers hand over whole lines.

## Deferred

- `DEF-P2-driver-tool-isolation`: isolating the tools a harness runs from the driver's environment and files.
- `DEF-P2-driver-netns`: an OS-enforced network namespace for the harness's own egress.
