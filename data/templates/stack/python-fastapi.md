---
id = "python-fastapi"
version = "1.0.0"
tier = "any"
stacks = ["python", "fastapi"]
project_shapes = []
description = "FastAPI service: Pydantic v2, Alembic, pytest-asyncio, uvicorn"
---

# CASCADE Instructions — Python FastAPI Service

> Stack: Python 3.11+ · FastAPI · Pydantic v2 · Alembic · pytest-asyncio · uvicorn
> Tier: any (typically PRC or PAC)

Use `{{project_name}}` for the app/package name, `{{db_url_env}}` for the database URL env var name (e.g. `DATABASE_URL`), and `{{min_python}}` for the minimum Python version (e.g. `3.11`).

---

## Module / Package Layout

```
{{project_name}}/
├── src/
│   └── {{project_name}}/
│       ├── __init__.py
│       ├── main.py              # FastAPI app factory + lifespan
│       ├── config.py            # pydantic-settings Settings model
│       ├── database.py          # SQLAlchemy async engine + session factory
│       ├── models/              # SQLAlchemy ORM models
│       │   └── user.py
│       ├── schemas/             # Pydantic v2 request/response models
│       │   └── user.py
│       ├── routers/             # APIRouter modules (one per domain)
│       │   └── users.py
│       └── deps.py              # FastAPI dependency injection helpers
├── alembic/
│   ├── env.py
│   └── versions/
├── tests/
│   ├── conftest.py              # app fixture, async test client
│   └── test_users.py
├── alembic.ini
├── pyproject.toml
└── .env.example
```

---

## Build & Tooling

```toml
[project]
name = "{{project_name}}"
requires-python = ">={{min_python}}"
dependencies = [
    "fastapi>=0.111",
    "uvicorn[standard]>=0.29",
    "pydantic>=2.7",
    "pydantic-settings>=2.3",
    "sqlalchemy[asyncio]>=2.0",
    "alembic>=1.13",
    "asyncpg>=0.29",           # or aiosqlite for SQLite
]

[project.optional-dependencies]
dev = ["pytest", "pytest-asyncio", "httpx", "ruff", "mypy"]
```

**Common commands:**

```bash
# Start dev server (reload on change)
uvicorn {{project_name}}.main:app --reload

# Run tests
pytest -x

# Lint
ruff check . && ruff format --check .

# Generate a new migration
alembic revision --autogenerate -m "add users table"

# Apply migrations
alembic upgrade head
```

---

## Testing Convention

Use `pytest-asyncio` with `asyncio_mode = "auto"` and `httpx.AsyncClient` for route tests.

```python
# tests/conftest.py
import pytest
from httpx import AsyncClient, ASGITransport
from {{project_name}}.main import app

@pytest.fixture
async def client():
    async with AsyncClient(
        transport=ASGITransport(app=app), base_url="http://test"
    ) as ac:
        yield ac
```

```python
# tests/test_users.py
async def test_create_user(client):
    resp = await client.post("/users/", json={"email": "a@b.com"})
    assert resp.status_code == 201
```

`pyproject.toml` asyncio config:

```toml
[tool.pytest.ini_options]
asyncio_mode = "auto"
```

---

## Lint & Format

Same `ruff` configuration as `python-lib`. Add `mypy` with FastAPI stubs:

```toml
[tool.mypy]
strict = true
plugins = ["pydantic.mypy"]
```

---

## Database Migrations

`alembic init alembic` creates the scaffold. Edit `alembic/env.py` to use the async engine and import all ORM models so autogenerate detects them.

**`alembic/env.py` async pattern:**

```python
from logging.config import fileConfig
from sqlalchemy.ext.asyncio import create_async_engine
from alembic import context
from {{project_name}}.models import Base  # import all models here
from {{project_name}}.config import settings

config = context.config
fileConfig(config.config_file_name)
target_metadata = Base.metadata

def run_migrations_online():
    connectable = create_async_engine(settings.database_url)

    async def run():
        async with connectable.connect() as connection:
            await connection.run_sync(
                lambda conn: context.configure(
                    connection=conn, target_metadata=target_metadata
                )
            )
            async with connection.begin():
                await connection.run_sync(
                    lambda _: context.run_migrations()
                )

    import asyncio
    asyncio.run(run())

run_migrations_online()
```

Key rules:
- Every schema change gets a migration. Never alter the DB schema manually.
- Migration files are committed alongside the code change that requires them.
- `alembic downgrade -1` must work cleanly for every migration.

---

## Common Pitfalls

- **Pydantic v2 breaking changes.** `model.dict()` is gone; use `model.model_dump()`. `validator` is gone; use `@field_validator`. Check the v2 migration guide before porting v1 code.
- **Lifespan over `startup`/`shutdown` events.** Use the `lifespan` context manager in `main.py`; the old event hooks are deprecated.
- **Never block the event loop.** Any synchronous I/O (file reads, subprocess calls) inside a route handler must be run via `asyncio.to_thread(...)`.
- **`{{db_url_env}}` in `.env`, not in code.** The `pydantic-settings` `Settings` model reads it from the environment. Commit `.env.example` with placeholder values; never commit `.env`.
- **Dependency injection for DB sessions.** Use `Depends(get_session)` to get an async session per request; never create sessions inside route bodies.
