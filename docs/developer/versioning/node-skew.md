# Node version skew policy

How a fleet controller and its enrolled nodes stay compatible as
versions drift apart over time.

## The same-minor window

A controller and an enrolled node are expected to run within the same
minor release of each other. A controller on `v2.4.x` and a node on
`v2.4.y` (any patch) are in-window; a controller on `v2.4.x` and a node
still on `v2.3.x` has fallen out of the same-minor window.

Version negotiation happens at two points:

- **Enroll time**: the controller either provisions the node's binary
  directly (a signed release artifact copied over ssh) or verifies a
  preinstalled binary's version before accepting enrollment.
- **Heartbeat**: every controller-node RPC carries version information,
  so drift introduced after enrollment (a node falling behind because it
  was not upgraded) is detected on the next heartbeat, not only at
  enroll time.

## Out-of-window warning

When the controller detects an enrolled node has fallen outside the
same-minor window, it warns rather than silently continuing or silently
cutting the node off. The warning names the node and the version gap, so
an operator can act before the gap widens into an actual incompatibility.

## Remediation path

The documented remediation is `cascade node upgrade [--all]`: a
controller-coordinated rollout that pushes the current release to one
node or every enrolled node, through the same node-managed install
channel enroll-time provisioning uses. This keeps the fleet's node
binaries under controller control rather than requiring an operator to
log into each node individually.

## Boundaries

This page documents the skew *policy*: the window, the negotiation
points, and the remediation path's name. The negotiation and upgrade
*mechanics* (the RPC fields, the rollout state machine, the ssh
provisioning flow) are implemented and owned elsewhere; this page is not
where those are specified. Signature verification during node transfers
follows the same signing-key rules as every other release artifact - see
[signing-key-rotation.md](signing-key-rotation.md).
