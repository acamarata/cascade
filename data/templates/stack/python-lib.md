---
id = "python-lib"
version = "1.0.0"
tier = "any"
stacks = ["python"]
project_shapes = ["lib"]
description = "Python library: src layout, pyproject.toml, pytest, ruff, mypy strict"
---

# CASCADE Instructions — Python Library

> Stack: Python 3.11+ · src layout · pyproject.toml · pytest · ruff · mypy strict
> Tier: any (typically PRC or PAC)

Use `{{package_name}}` for the importable package name (e.g. `my_lib`), `{{project_name}}` for the distribution name (e.g. `my-lib`), and `{{min_python}}` for the minimum Python version (e.g. `3.11`).

---

## Module / Package Layout

Always use `src/` layout to prevent accidentally importing an uninstalled package.

```
{{project_name}}/
├── src/
│   └── {{package_name}}/
│       ├── __init__.py          # exports the public API
│       ├── _internal.py         # underscore prefix = private
│       └── py.typed             # PEP 561 marker — enables type checking by consumers
├── tests/
│   ├── conftest.py
│   └── test_core.py
├── pyproject.toml
├── .python-version              # pinned via pyenv (e.g. "3.11.9")
└── README.md
```

---

## Build & Tooling

All configuration lives in `pyproject.toml`. Do not use `setup.py` or `setup.cfg`.

```toml
[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"

[project]
name = "{{project_name}}"
version = "0.1.0"
requires-python = ">={{min_python}}"
dependencies = []

[project.optional-dependencies]
dev = ["pytest", "pytest-cov", "ruff", "mypy"]

[tool.hatch.build.targets.wheel]
packages = ["src/{{package_name}}"]
```

**Common commands:**

```bash
# Install in editable mode with dev extras
pip install -e ".[dev]"

# Build a wheel
python -m build

# Run tests
pytest

# Lint + format
ruff check .
ruff format .

# Type check
mypy src/
```

---

## Testing Convention

```python
# tests/test_core.py
import pytest
from {{package_name}} import some_function

def test_some_function_returns_expected():
    assert some_function("input") == "expected"

def test_some_function_raises_on_bad_input():
    with pytest.raises(ValueError, match="must not be empty"):
        some_function("")
```

- One test file per module (`tests/test_<module>.py`).
- `conftest.py` for shared fixtures only.
- Coverage target: ≥80% statements (tracked via `pytest-cov`).
- No `unittest.TestCase`; use plain `def test_*` functions.

---

## Lint & Format

`ruff` handles both lint and format. Minimum `pyproject.toml` configuration:

```toml
[tool.ruff]
line-length = 88
target-version = "py{{min_python | replace('.', '')}}"

[tool.ruff.lint]
select = ["E", "W", "F", "I", "UP", "B", "SIM"]
ignore = []

[tool.mypy]
strict = true
python_version = "{{min_python}}"
```

Run `ruff check --fix .` to auto-fix safe issues. Never disable `mypy --strict` without a documented reason in the code.

---

## Common Pitfalls

- **`src/` layout is mandatory.** Without it, `import {{package_name}}` resolves from the repo root, masking packaging bugs.
- **`py.typed` marker.** Required for downstream users to get mypy coverage of your types. It is an empty file.
- **Avoid mutable default arguments.** `def f(items=[])` is a classic bug; use `items: list | None = None` and assign `items = items or []` in the body.
- **`pyproject.toml` only.** Any `setup.py` found in the repo is a sign something was auto-generated incorrectly.
- **mypy strict.** `--strict` enables `--disallow-untyped-defs`, `--disallow-any-generics`, `--warn-return-any`, and more. Address each failure; do not silence with `# type: ignore` without a comment explaining why.
