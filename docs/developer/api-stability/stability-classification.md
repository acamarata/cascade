# Stable vs experimental classification

Every `pkg/` directory present at the time this document was authored is
classified below. Nothing is left unclassified: an unlabeled `pkg/`
package would be a silent gap, and this document does not leave one.

| Package | Classification | What that means |
|---|---|---|
| `pkg/provider` | stable | Carries the full compat promise (see [compat-promise.md](compat-promise.md)): additions need a doc entry, removals need the one-minor deprecation cycle. |
| `pkg/plugin` | stable | Same compat promise. The `cascade.plugin/v2` manifest schema additionally carries its own stronger promise: stable across the entire v2.x line (see `../versioning/deprecation-and-abi.md`). |
| `pkg/cascade` | stable | The frozen 14-kind error taxonomy plus the exit-code and JSON-RPC code tables. Frozen means stronger than the general compat promise: the kind set does not grow or shrink under the normal addition/removal cycle at all, by its own T0 ruling (R-14.3) - a change here is a taxonomy amendment, not a routine SDK addition. |

## What "stable" means here

A stable package's exported symbols carry the SDK compat promise in full:
adding a symbol needs a documentation entry (this directory plus godoc);
removing or breaking one needs the one-minor deprecation window
`../versioning/deprecation-and-abi.md` defines. A downstream author who
pins to a stable package's current symbols can upgrade across patch and
most minor releases without their code breaking silently.

## What "experimental" means, for when a package is labeled that way

No `pkg/` directory carries this label today - every directory present at
authoring time is stable per the table above. If a future package under
`pkg/` is added as experimental, that label must be explicit in this
table and in that package's own doc comment: experimental means it may
change or be removed without going through the one-minor deprecation
window, and it is never silently unstable. A downstream author sees the
label before depending on it, not after being broken by a change.

## Adding a new pkg/ directory

Any future `pkg/` directory needs a row added to the table above the same
day it lands, classified as stable or experimental. This document does
not tolerate an "unclassified" state for any directory that exists.
