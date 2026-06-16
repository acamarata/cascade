---
id = "standard-python"
version = "1.0.0"
tier = "any"
stacks = ["python"]
project_shapes = []
description = "Python coding standard: type hints, ruff/black, venv isolation, pytest, no bare except."
---

# Python Coding Standard

## Type Hints

- All function signatures carry type hints: parameters and return type.
- Use `from __future__ import annotations` at the top of every module to enable
  deferred evaluation.
- Run `mypy --strict` (or `pyright` in strict mode) in CI; zero errors on the
  main branch.
- Prefer built-in generics (`list[str]`, `dict[str, int]`) over `typing.List` /
  `typing.Dict` (Python 3.9+).

## Formatting and Linting

- Format with `black` (or `ruff format`); line length 88.
- Lint with `ruff` (replaces flake8 + isort + pyupgrade). Config in
  `pyproject.toml` under `[tool.ruff]`.
- Both checks run on save in the editor and as a pre-commit hook.
- No `# noqa` suppressions without an inline comment explaining why.

## Environment Isolation

- Every project uses a virtual environment (`python -m venv .venv` or `uv venv`).
  Never install packages into the system Python.
- Pin direct dependencies with exact versions in `pyproject.toml`
  (`[project.dependencies]`). Lock transitive deps with a lockfile
  (`uv lock` / `pip-compile`).
- `.venv/` and `*.egg-info/` are always in `.gitignore`.

## Error Handling

- No bare `except:` clauses. Catch specific exception types.
- Re-raise with context: `raise NewError("…") from original_exc`.
- Use custom exception classes for domain errors; inherit from a project base
  exception so callers can catch broadly when needed.

## Tests

- Framework: `pytest`. Test files in `tests/` mirroring the `src/` layout.
- Coverage target: ≥80% lines (`pytest-cov`). Run `pytest --cov` in CI.
- Use `pytest.mark.parametrize` for data-driven cases instead of loop-inside-test.
- No skipped tests on main without a linked issue in the skip reason.

## Code Structure

- Files: ≤300 lines. Functions: ≤50 lines. One module = one concern.
- Docstrings on every public function, class, and module (Google or NumPy style,
  consistently across the project).
