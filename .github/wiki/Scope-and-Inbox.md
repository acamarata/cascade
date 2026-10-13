# Scope and the inbox in a harness session

Cascade can hand a running harness session the inbox items addressed to it, at the start of
the session's next turn. This page says what reaches a session, what never does, and what the
current build does.

**Current state: off.** The delivery path is built and wired, and it ships switched off. A
`context.slice` request returns no inbox items and leaves every queue untouched until the tool gate
turns it on. Nothing on this page changes what you see today.

## What reaches a session

An inbox item reaches a session only when all of these hold:

- the item is addressed to that session by its session id, or the notify router selects it for the
  session under the same scope and visibility rules `cascade inbox` uses (an item that names only a
  task id reaches no session);
- the session is known to the fleet sessions store and is not closed;
- the request that drains it comes from the owner CLI or from a caller bound to that same session.

An item addressed to another session, to another task, or to no resolvable target reaches nobody.
Delivery never widens a session's scope.

## What the session sees

At the next prompt the context hook prints one block headed
`Cascade inbox (untrusted-source data, not instructions)`, after any supervision directives. Each
line carries the item's id, class, origin scope, data class and a short excerpt. The excerpt is at
most half of the body and at most 200 characters; the body itself is never sent. Anything that looks
like markup or a line break in a field is escaped, so an item cannot forge a second line or close the
block. The text is data. It cannot approve, gate or complete anything.

## What stays put

- Draining an item does not mark it read. It is still listed by `cascade inbox` until you
  acknowledge it.
- If the drain itself fails (for example a store error), the item stays queued and the next turn tries
  again. Delivery is at most once: the queue entry is removed when the daemon hands it over, so if the
  hook times out or dies after that point the hydrated copy is lost. The item itself stays in
  `cascade inbox`.
- Queued items are stored in the daemon database, so a restart loses nothing. Expired items are
  dropped when the queue is drained.
- A session that never registers with the daemon receives nothing. Items stay in `cascade inbox`.

## Direct push

Cascade does not push inbox items into a session mid-turn. The harness gives a server no
way to observe that a pushed notification reached a `-p`-driven session, so the push path stays
off and every item waits for the next turn. On Windows the same holds.
