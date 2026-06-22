---
id = "rule-delegation-model-discipline"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = []
description = "Top tier plans and reviews; route bulk work to cheaper tiers; max free quotas for adversarial QA; parallel independent units."
---

# Delegation and Model Discipline

These rules govern how work is routed across agent tiers and model classes.

## Top Tier Plans, Reviews, and Gates

The top-tier interactive session plans, synthesizes, reviews outputs, and
decides. It does not do bulk execution, code generation, or long-running
drafting. If work can be delegated, delegate it.

## Route Bulk Work to Cheaper Tiers

Code generation, documentation drafting, test writing, and multi-file edits
go to secondary or tertiary agents. The primary model's quota is a scarce
resource. Use it for decisions, not for work a cheaper agent can do equally well.

## Maximize Free and Cheap Quota Pools for Adversarial Work

Adversarial code review, QA sweeps, and research tasks are exactly the work
that benefits from being run in volume at low cost. Cheap and free quota
pools exist for this purpose. Use them. Do not burn the primary model on
tasks that cheaper models handle at the same quality.

## Cross-Family Models for Code Review and QA

When reviewing code or running QA after generation, prefer a model from a
different family than the one that wrote the code. A second family finds
different classes of bugs. One model writing and reviewing its own output
catches fewer errors than two different models.

## Always Set the Model Explicitly on Sub-Agents

When spawning a sub-agent, always set the model field explicitly. Never
allow the sub-agent to inherit the current model implicitly. Implicit
inheritance burns the primary quota unintentionally and defeats the purpose
of tiered routing.

## Parallel Independent Units

N independent tasks equal N parallel agents. Do not serialize work that
can fan out. Sequential execution of independent tasks wastes wall-clock time
and delays the overall result.

## Verify Cheaply

Verification tasks (build, tests, endpoint checks) are cheap to run and
should be run, not skipped. Do not route a verification task to an expensive
model when running `cargo test` or `curl` returns the same answer for free.
Cheap verification beats expensive reasoning about whether something probably works.
