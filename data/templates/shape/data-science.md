---
id = "data-science"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = ["data-science"]
description = "Data analysis, ML, or research workflows where reproducibility and experiment tracking matter."
---

# Data Science / Research

Codebases built around exploration, analysis, and model development.
The primary artifacts are insights and trained models, not deployed services.
Reproducibility is the core discipline: results must be re-derivable from the same
inputs and code.

## Project Structure Expectations

Common layout:
- `data/raw/` — immutable source data (never overwrite; never commit large files)
- `data/processed/` — derived datasets (gitignored or tracked by DVC / similar)
- `notebooks/` — exploratory work
- `src/` or `<project>/` — reusable Python/R/Julia modules
- `models/` — saved model artifacts (tracked by DVC or model registry, not git)
- `reports/` — generated figures and output documents

Large data and model files are tracked via DVC, LFS, or a dedicated model registry.
Never commit binary blobs or large CSVs to git.

## Decision Norms

Experiments are documented: what was tried, what the hypothesis was, what
the result was. A notebook that exists without a corresponding experiment log
is incomplete work.

Hyperparameters and random seeds are always logged. "It worked on my machine"
is not a result.

When a modeling choice is non-obvious (loss function, architecture, feature
engineering), leave a note explaining why (not just what).

## Code Review Conventions

Review for: reproducibility (fixed seeds, dependency versions pinned), data
leakage (test set contamination), metric validity, and statistical soundness.

Notebooks are hard to review in diff form. Prefer reviewed, cleaned notebooks
over raw exploratory drafts in the main branch. Use `nbstripout` or equivalent
to strip notebook outputs before commit.

Production-bound model code (inference pipeline, feature engineering) follows
the same code review standards as application code.

## Release Cadence

Research work is released as papers, reports, or model checkpoints on a
milestone cadence, not a continuous one. Model releases include: architecture
description, training data provenance, evaluation metrics, and known limitations.

Deployed inference services follow application release norms (see saas-product
or cli-tool shape as appropriate).

## Documentation Expectations

`README.md`: project goal, how to reproduce the main results, required hardware
(GPU memory, disk), and estimated compute time. A `METHODS.md` or equivalent
for the statistical/algorithmic approach.

Notebooks include: context at the top (what question this answers), conclusion
at the bottom (what was found). Exploratory notebooks are in `notebooks/explore/`;
final notebooks are in `notebooks/reports/` and are cleaned before commit.

## Dependency Philosophy

Pin all dependencies to exact versions in a lockfile (conda `environment.yml`
with exact builds, `requirements.txt` with hashes, or `pyproject.toml` with
poetry lockfile). Results that can't be reproduced because "numpy changed"
are not reproducible results.

Virtual environments are per-project. System-wide installs are not acceptable
for research code that must be reproducible by a collaborator.
