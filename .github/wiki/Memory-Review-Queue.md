# Memory Review Queue

Cascade decides on its own what is worth remembering, and the rule is
deliberately dumb: a candidate becomes a durable memory once it has been
referenced at least three times across at least two separate sessions. No
model is asked, nobody is prompted, and the same evidence always produces
the same answer.

The review queue is the other half of that. It is where you see what the
automatic lane did, and where you overrule it.

## Two lanes, one story

| | Automatic lane | Review queue |
|---|---|---|
| Decides | Promotes at the threshold | Nothing. It presents. |
| Who acts | The machine, mechanically | You, one candidate at a time |
| Covers | Candidates at or above the threshold | Candidates below it, and promotions you may want back |

A candidate that has crossed the threshold never appears as something
awaiting your review. It has already been promoted, or is about to be, by a
rule that does not consult anyone. Asking you about it would be theatre.

## What the queue shows

```
cascade memory review
```

**Pending**: candidates below the threshold. They are on the list because
their counts are low, which is a fact, not a hint that you should promote
them. The command prints the threshold beside the counts so you can check
that for yourself.

**Promoted**: promotions that are still standing. These are already real
memories. They are listed because reverting one is only possible if you know
which it was.

Three more things are reported below the tables, because a list that looks
empty when it is not would be a lie: how many candidates you deferred and
have not yet come back to, which candidates the automatic lane is about to
take, and any candidate file that could not be read.

Looking at the queue never changes it. Neither does the weekly digest.

## What you can do

Each action names one candidate by its `<kind>/<name>` address. There is no
"approve everything" and there will not be one.

| You want to | Command |
|---|---|
| Promote something early | `cascade memory review project/x --auto-approve` |
| Leave it alone | `cascade memory review project/x --auto-skip` |
| Stop seeing it for a while | `cascade memory review project/x --defer-days 14` |
| Take back a promotion | `cascade memory review user/y --revert` |

**Defer** hides a candidate; it does not silence the evidence. The counts
keep climbing, and if it crosses the threshold it is promoted like anything
else. Deferring is about your attention, not about the memory.

**Revert** un-promotes a candidate and makes it start over: the next
reference counts as the first, so a belief you rejected has to earn its way
back. It does not delete the record that was written. That is
`cascade memory forget`, and the output tells you so.

**Skip** is recorded even though it changes nothing, so the audit can answer
"did anyone actually look at this".

## The weekly digest

Once a week the daemon builds a digest and puts it on the event bus. It
tells you what is waiting below the threshold and what was promoted during
that week, with the window it covers printed in the payload, so you never
have to guess which stretch of time a digest is talking about.

It carries addresses and counts only. The text of your memories is never in
it. The digest is published locally and nothing else: no email, no webhook,
no outbound anything.

A quiet week still produces a digest. An event that only showed up when
there was news would be indistinguishable from a job that had died.

Set the cadence with `memory.review_cadence_days` in `config.toml`
(default 7).

## Automation

Nothing in this surface prompts, so it works unattended.
`CASCADE_MEMORY_REVIEW_ACTION=approve` picks the action when you cannot pass
a flag, and `--json` gives you the same data the table shows.
