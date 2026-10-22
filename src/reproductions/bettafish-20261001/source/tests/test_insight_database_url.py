import importlib.util
import sys
import types
from pathlib import Path

import pytest
from sqlalchemy.engine import make_url


PROJECT_ROOT = Path(__file__).resolve().parent.parent


@pytest.fixture
def db_module(monkeypatch):
    insight_package = types.ModuleType("InsightEngine")
    insight_package.__path__ = [str(PROJECT_ROOT / "InsightEngine")]
    monkeypatch.setitem(sys.modules, "InsightEngine", insight_package)

    utils_package = types.ModuleType("InsightEngine.utils")
    utils_package.__path__ = [str(PROJECT_ROOT / "InsightEngine" / "utils")]
    monkeypatch.setitem(sys.modules, "InsightEngine.utils", utils_package)

    config_path = PROJECT_ROOT / "InsightEngine" / "utils" / "config.py"
    config_spec = importlib.util.spec_from_file_location("InsightEngine.utils.config", config_path)
    config_module = importlib.util.module_from_spec(config_spec)
    monkeypatch.setitem(sys.modules, "InsightEngine.utils.config", config_module)
    config_spec.loader.exec_module(config_module)

    db_path = PROJECT_ROOT / "InsightEngine" / "utils" / "db.py"
    db_spec = importlib.util.spec_from_file_location("InsightEngine.utils.db", db_path)
    module = importlib.util.module_from_spec(db_spec)
    monkeypatch.setitem(sys.modules, "InsightEngine.utils.db", module)
    db_spec.loader.exec_module(module)
    return module


@pytest.mark.parametrize(
    ("dialect", "driver"),
    [("postgresql", "postgresql+asyncpg"), ("mysql", "mysql+aiomysql")],
)
def test_split_fields_round_trip_special_characters(db_module, monkeypatch, dialect, driver):
    monkeypatch.delenv("DATABASE_URL", raising=False)
    monkeypatch.setattr(db_module.settings, "DB_DIALECT", dialect)
    monkeypatch.setattr(db_module.settings, "DB_HOST", "localhost")
    monkeypatch.setattr(db_module.settings, "DB_PORT", 5432)
    monkeypatch.setattr(db_module.settings, "DB_USER", "user@example.com")
    monkeypatch.setattr(db_module.settings, "DB_PASSWORD", "p@ss:/?#[]")
    monkeypatch.setattr(db_module.settings, "DB_NAME", "betta/fish")

    parsed = make_url(db_module._build_database_url())

    assert parsed.drivername == driver
    assert parsed.username == "user@example.com"
    assert parsed.password == "p@ss:/?#[]"
    assert parsed.database == "betta/fish"


def test_explicit_url_accepts_percent_encoded_password(db_module, monkeypatch):
    monkeypatch.setenv(
        "DATABASE_URL",
        "postgresql+asyncpg://user:p%40ss@localhost:5432/bettafish",
    )

    parsed = make_url(db_module._build_database_url())

    assert parsed.password == "p@ss"
    assert parsed.host == "localhost"


def test_explicit_url_rejects_unescaped_at_in_password(db_module, monkeypatch):
    monkeypatch.setenv(
        "DATABASE_URL",
        "postgresql+asyncpg://user:p@ss@localhost:5432/bettafish",
    )

    with pytest.raises(ValueError, match="percent-encode"):
        db_module._build_database_url()


def test_compose_keeps_legacy_postgres_variable_fallbacks():
    compose = (PROJECT_ROOT / "docker-compose.yml").read_text(encoding="utf-8")

    assert "POSTGRES_USER: ${DB_USER:-${POSTGRES_USER:-bettafish}}" in compose
    assert "POSTGRES_PASSWORD: ${DB_PASSWORD:-${POSTGRES_PASSWORD:-bettafish}}" in compose
    assert "POSTGRES_DB: ${DB_NAME:-${POSTGRES_DB:-bettafish}}" in compose
