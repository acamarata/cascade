# Stack Template: Python (Poetry)

**Tier:** APC · **Stack:** Python with Poetry · **Language:** Python 3.11+

## Idiomatic Layout

```
src/
  {package}/
    __init__.py         # Public API, explicit __all__
    {module}.py
    models/             # Pydantic v2 models
    services/           # Business logic, no I/O side effects
    utils/              # Pure functions
    config.py           # pydantic-settings config
    errors.py           # Custom exception hierarchy
tests/
  conftest.py
  test_{module}.py
docs/                   # Sphinx or MkDocs source
pyproject.toml          # Poetry metadata + all tool configs
poetry.lock             # Committed lock file
.cascade/               # AI working memory (gitignored)
```

## Modular Coding Patterns

- Poetry groups: `[tool.poetry.group.dev.dependencies]` for dev/test tools
- Virtual env managed by Poetry; never activate manually in CI (use `poetry run`)
- Plugins/extras declared under `[tool.poetry.extras]`; document each extra's purpose
- Type hints strict throughout; all public symbols exported from `__init__.py`

## Key Commands

```bash
poetry install          # Install all deps
poetry install --only main  # Production deps only
poetry run pytest       # Tests
poetry run ruff check . # Lint
poetry run ruff format .# Format
poetry run mypy src/    # Type check
poetry build            # Build wheel + sdist
poetry publish          # Publish to PyPI
poetry version patch    # Bump patch version
```

## Engineering Rules

- `pyproject.toml`: all tool configs in `[tool.*]` sections; no separate config files
- Mypy strict mode; `python_version` matches Poetry's `python` constraint
- File ceiling: ≤400 lines per .py file
- Dev group includes: `pytest`, `ruff`, `mypy`, `pytest-cov`

## Cross-Refs

- `.cascade/rules/engineering-excellence.md`
- `.cascade/rules/dependency-management.md`
- `.cascade/rules/version-release-lock.md`
