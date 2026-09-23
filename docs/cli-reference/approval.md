# `cascade approval`

Inspect and decide the actions policy evaluation queued for a human answer
(§5.24 — an ask-tier, L2/L3, action the policy engine could not resolve on
its own).

```
cascade approval list
cascade approval show <request-id>
cascade approval deny <request-id> --presented-summary "…" --presented-level N
cascade approval grant <request-id> --token "$TOKEN"
cascade approval expire
cascade approval standing …
```

There is no daemonless fallback for this namespace: the queue lives in
daemon memory by design (an approval nobody is waiting on is an approval
nobody gave), so every verb here refuses with an actionable error if no
daemon is running, rather than answering from a second, empty queue.

## `list`

Lists every action awaiting a decision, oldest first: request id, expiry,
and the summary a human is asked about. Never a token, a nonce or an
action hash — those never leave the daemon.

## `show <request-id>`

Shows one pending entry: request id, summary, expiry. Same three
bridge-safe fields `list` prints, for one request.

## `deny <request-id>`

Records a refusal. `--presented-summary` and `--presented-level` are
required: the exact string and rung the surface displayed must be passed
back, so the answer is bound to what was actually shown rather than to
whatever the entry happens to say now.

## `grant <request-id>`

Redeems a signed approval token. This is an **elevated verb**: it needs a
fresh local attestation in addition to `--token`, the base64 signed
approval record. The token is verified before anything is written, so a
forged or expired one changes no queue state. Knowing a request id is
never enough on its own.

## `expire`

Runs the rate-limited expiry sweep and reports how many entries it
retired.

## § grant — the Telegram bridge is the parallel surface, not a substitute

A pending approval may also be surfaced as a Telegram inline button
(`plugins/cascade-pa/telegram`, P1-E23-W5-S48-T4) — the remote half of the
same decision this command makes locally. Tapping "approve" or "deny"
reaches the SAME `approval.grant`/`approval.deny` verbs this CLI drives,
called with the request id alone; `cascade approval grant <request-id>
--token "$TOKEN"` is the non-interactive equivalent an operator with local
shell access runs directly, without a phone.

**Today's honest limit, stated once here rather than assumed.** No
production attestation helper and no production approval-signing key
source exist in this tree yet (both absences are fail-closed, not an
oversight — see `cmd/cascade/daemon_unix_policy.go`). That means:

- `cascade approval grant` from a real terminal is refused the same way
  every other elevated verb is refused with nothing enrolled: with no
  attestation source, the elevation guard answers `elevation-required`
  before the command's own token check is even reached.
- The bridge's approve tap is refused for the identical reason, over the
  identical verb — it submits no `--token` at all (there is none to hold),
  so even a future attestation source would still meet `approval.grant`'s
  own "requires a request_id and a signed_token" refusal first.
- `cascade approval deny` and the bridge's reject tap are NOT
  elevation-class and redeem for real today, on both surfaces.

Full contract and the bridge's five-gate order: `docs/security-posture.md`
§ Bridge Inline-Button Approvals. (The planning contract for W/S-48.T4
names a `docs/cli/approval.md` path; this tree's convention is
`docs/cli-reference/*.md`, which is where this file lives instead.)
