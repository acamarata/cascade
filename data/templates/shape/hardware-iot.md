---
id = "hardware-iot"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = ["hardware-iot"]
description = "Software for embedded hardware or IoT devices with real-world physical constraints."
---

# Hardware / IoT

Software that runs on or directly controls physical hardware: microcontrollers,
single-board computers, sensors, actuators, or networked devices. Physical
constraints (memory, power, connectivity, latency) shape every design choice.

## Project Structure Expectations

Separate directories for firmware, host-side tooling, and shared protocol
definitions. Common layout:
- `firmware/` — code that runs on the device
- `host/` — companion app, cloud connector, or configuration tool
- `proto/` — shared data formats (Protobuf, MessagePack schemas)
- `hardware/` — schematics, PCB files, BOM (if open hardware)

Firmware and host code often live in the same repo if they are tightly coupled
(e.g. a custom bootloader + its provisioning tool). Separate repos make sense
when the host side evolves faster than the firmware.

## Decision Norms

Hardware constraints are non-negotiable. RAM limits, flash size, and power budgets
are documented in `constraints.md` and treated as hard limits, not guidelines.
When a feature would exceed a constraint, the feature changes, not the constraint.

Over-the-air (OTA) update strategy is decided early and never retrofitted. A
firmware that ships without OTA support is a firmware that can't be patched in
the field.

## Code Review Conventions

Firmware PRs include: flash size delta, RAM usage before and after, and power
consumption impact (if measurable). CI runs a build for every target platform
in the supported hardware matrix.

Code that manipulates hardware registers or interrupts requires review from
someone familiar with the target MCU. Don't approve embedded code you can't
test on hardware or at least a simulator.

## Release Cadence

Firmware releases are conservative. A bug in a shipped firmware may require
physical device access to recover. Test on representative hardware before tagging.
Pre-release on a small group of devices before wide deployment.

Host-side software follows a faster cadence. Firmware and host versions are
tracked in a compatibility matrix.

## Documentation Expectations

`README.md`: required hardware, flashing/provisioning instructions, pinout,
and first-boot walkthrough. If the device has a cloud component, document
the API and authentication flow.

Hardware revisions are tracked with board version numbers. Document what changed
between revisions in the changelog.

Safety-critical behavior (high voltages, motors, locks) requires explicit warnings
in the documentation and in the code (comments that explain worst-case failure modes).

## Dependency Philosophy

Firmware has no runtime package manager. Dependencies are vendored or included
as git submodules at a pinned commit. The build must be fully reproducible from
the repo without network access.

Host-side tooling follows normal package manager practices (pnpm, cargo, etc.)
but still avoids large transitive trees where practical.

No GPLv3 code in firmware if the device ships as a closed product (check your
legal situation; LGPL and MIT are typically safe).
