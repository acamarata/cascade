---
id = "django"
version = "1.0.0"
tier = "any"
stacks = ["python", "django"]
project_shapes = []
description = "Django 5.x + DRF service: pytest-django, Black, isort, Alembic-style migrations"
---

# CASCADE Instructions — Django Service

> Stack: Python 3.11+ · Django 5.x · Django REST Framework · pytest-django · Black · isort
> Tier: any (typically PRC or PAC)

Use `{{project_name}}` for the Django project name, `{{app_name}}` for the primary Django app, and `{{secret_key_env}}` for the environment variable holding `SECRET_KEY` (e.g. `DJANGO_SECRET_KEY`).

---

## Module / Package Layout

```
{{project_name}}/
├── {{project_name}}/              # Django project package
│   ├── __init__.py
│   ├── settings/
│   │   ├── base.py                # shared settings
│   │   ├── local.py               # local dev overrides
│   │   └── production.py          # production overrides
│   ├── urls.py
│   ├── wsgi.py
│   └── asgi.py
├── {{app_name}}/                  # primary Django app
│   ├── migrations/
│   │   └── 0001_initial.py
│   ├── models.py
│   ├── serializers.py             # DRF serializers
│   ├── views.py                   # DRF ViewSets / APIViews
│   ├── urls.py
│   ├── admin.py
│   └── apps.py
├── tests/
│   ├── conftest.py
│   └── test_{{app_name}}.py
├── pyproject.toml
├── manage.py
└── .env.example
```

---

## Build & Tooling

```toml
[project]
name = "{{project_name}}"
requires-python = ">=3.11"
dependencies = [
    "django>=5.0",
    "djangorestframework>=3.15",
    "django-environ>=0.11",       # 12-factor env config
    "gunicorn>=22.0",
]

[project.optional-dependencies]
dev = ["pytest", "pytest-django", "factory-boy", "black", "isort", "mypy", "django-stubs"]
```

**Common commands:**

```bash
# Dev server
python manage.py runserver

# Run tests
pytest

# Create migration
python manage.py makemigrations

# Apply migrations
python manage.py migrate

# Format
black .
isort .

# Check format (CI)
black --check .
isort --check-only .
```

---

## Testing Convention

Use `pytest-django` — never `django.test.TestCase` (it is slower and less composable).

```python
# tests/conftest.py
import pytest

@pytest.fixture(autouse=True)
def enable_db_access_for_all_tests(db):
    pass
```

```python
# tests/test_{{app_name}}.py
import pytest
from rest_framework.test import APIClient
from {{app_name}}.models import Item

@pytest.fixture
def api_client():
    return APIClient()

@pytest.mark.django_db
def test_list_items_returns_200(api_client):
    Item.objects.create(name="widget")
    resp = api_client.get("/api/items/")
    assert resp.status_code == 200
    assert len(resp.json()) == 1
```

`pyproject.toml` pytest config:

```toml
[tool.pytest.ini_options]
DJANGO_SETTINGS_MODULE = "{{project_name}}.settings.local"
python_files = ["test_*.py"]
```

Use `factory-boy` for model factories instead of inline `Model.objects.create()` calls in tests.

---

## Lint & Format

```toml
[tool.black]
line-length = 88
target-version = ["py311"]

[tool.isort]
profile = "black"
known_django = ["django", "rest_framework"]
sections = ["FUTURE", "STDLIB", "THIRDPARTY", "DJANGO", "FIRSTPARTY", "LOCALFOLDER"]
```

Run in CI:

```bash
black --check . && isort --check-only .
```

---

## Database Migrations

Django's built-in `makemigrations` / `migrate` is the migration system. Do not use Alembic — it is for SQLAlchemy, not Django ORM.

Key rules:
- Every model change must have a corresponding migration committed in the same PR.
- Never edit a migration that has been applied to a shared environment. Create a new migration instead.
- Squash old migrations periodically: `python manage.py squashmigrations {{app_name}} 0001 0020` — but only after all environments are past the squash point.
- Run `python manage.py migrate --check` in CI to confirm no unapplied migrations are in the codebase.

**Data migrations** (not just schema changes):

```python
# migrations/0002_populate_slugs.py
from django.db import migrations

def populate_slugs(apps, schema_editor):
    Item = apps.get_model("{{app_name}}", "Item")
    for item in Item.objects.all():
        item.slug = item.name.lower().replace(" ", "-")
        item.save(update_fields=["slug"])

class Migration(migrations.Migration):
    dependencies = [("{{app_name}}", "0001_initial")]
    operations = [migrations.RunPython(populate_slugs, migrations.RunPython.noop)]
```

Always provide a reverse function (or `migrations.RunPython.noop`) so rollback works.

---

## Common Pitfalls

- **`SECRET_KEY` in version control.** Never commit `SECRET_KEY`. Use `django-environ` to read `{{secret_key_env}}` from the environment. The `.env.example` file shows the key name but not the value.
- **`DEBUG=True` in production.** Use `settings/production.py` with `DEBUG = False`. The split-settings pattern (`base.py` + environment-specific files) prevents this mistake.
- **N+1 queries.** Use `select_related()` for ForeignKey lookups and `prefetch_related()` for ManyToMany. Use `django-debug-toolbar` in dev to spot N+1s.
- **`makemigrations` without `migrate`.** Running `makemigrations` creates the migration file but does not apply it. Always follow with `migrate` in dev.
- **DRF permissions on every view.** Default DRF permission is `IsAuthenticated` — set it globally in `settings.py` via `DEFAULT_PERMISSION_CLASSES`. Never rely on route-level security alone.
