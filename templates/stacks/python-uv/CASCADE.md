# Stack Template: Python (uv)

**Tier:** APC · **Stack:** Python with uv package manager · **Language:** Python 3.11+

## Idiomatic Layout

```
src/
  {package}/
    __init__.py         # Public API surface (explicit re-exports)
    {module}/
      __init__.py
      {impl}.py
    models/             # Pydantic models or dataclasses
    services/           # Business logic
    utils/              # Pure helper functions
    config.py           # Settings via pydantic-settings
    errors.py           # Unified exception hierarchy
    cli.py              # CLI entry point (typer/click)
tests/                  # Mirrors src/{package}/ structure
  conftest.py
  test_{module}.py
scripts/                # Dev helper scripts
pyproject.toml          # All config (build, lint, test, type-check)
uv.lock                 # Lock file (committed)
.cascade/               # AI working memory (gitignored)
```

## Modular Coding Patterns

- `__init__.py` at package root: explicit `__all__` list; no wildcard imports
- Pydantic v2 for all external data validation; never `dict` for structured data
- Services are plain classes; injected via constructor; no global singletons
- Type hints on all public functions: parameters and return types
- Async: `asyncio` throughout if any I/O is async; no mixing sync/async carelessly

## Key Commands

```bash
uv sync                 # Install from lock file
uv run pytest           # Run tests
uv run ruff check .     # Lint
uv run ruff format .    # Format
uv run mypy src/        # Type check
uv build                # Build wheel + sdist
uv publish              # Publish to PyPI
```

## Engineering Rules

- `pyproject.toml`: `[tool.ruff]`, `[tool.mypy]`, `[tool.pytest.ini_options]` all present
- Mypy: `strict = true`; no untyped functions in `src/`
- File ceiling: ≤400 lines per .py file; split by domain beyond limit
- No mutable module-level state; config via pydantic-settings from env

## Cross-Refs

- `.cascade/rules/engineering-excellence.md`
- `.cascade/rules/unit-header-comment-standard.md`
- `.cascade/rules/credentials-vault.md`
