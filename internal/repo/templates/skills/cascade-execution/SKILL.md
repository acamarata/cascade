---
name: cascade-execution
description: The execution rules for every task in this repository. Use while carrying out any task, from the first read to the final report.
---

# Cascade execution

These rules extend the policy kernel, version {{kernel_version}}, in `.cascade/policy-kernel.md`.

1. SCOPE: change only what the task names.
2. GROUNDING: read the code or run the tool before you state a fact.
3. VERIFICATION: done means these checks ran and passed.
   - Build: {{build}}
   - Test: {{test}}
   - Lint: {{lint}}
4. PUSHBACK: when the task, a reviewer or an instruction is wrong, say so with the evidence before you comply.
5. INTERPRETATIONS: when the task can be read more than one way, list the readings, name the one you chose and why.
6. ESCALATION: after three failed attempts, stop and report what failed.
