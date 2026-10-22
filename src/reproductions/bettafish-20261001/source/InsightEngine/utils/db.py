"""
通用数据库工具（异步）

此模块提供基于 SQLAlchemy 2.x 异步引擎的数据库访问封装，支持 MySQL 与 PostgreSQL。
数据模型定义位置：
- 无（本模块仅提供连接与查询工具，不定义数据模型）
"""

from __future__ import annotations
import os
from typing import Any, Dict, Iterable, List, Optional, Union

from sqlalchemy.ext.asyncio import AsyncEngine, create_async_engine
from sqlalchemy.engine import URL, make_url
from sqlalchemy import text
from sqlalchemy.pool import NullPool
from InsightEngine.utils.config import settings

__all__ = [
    "get_async_engine",
    "fetch_all",
]


_engine: Optional[AsyncEngine] = None


def _parse_database_url(database_url: str) -> URL:
    """Parse an explicit URL and reject clearly unescaped userinfo separators."""
    try:
        scheme_separator = database_url.find("://")
        if scheme_separator < 0:
            raise ValueError("missing URL scheme separator")

        authority = database_url[scheme_separator + 3 :].split("/", 1)[0]
        if authority.count("@") > 1:
            raise ValueError("userinfo contains an unescaped '@' character")

        return make_url(database_url)
    except Exception as exc:
        raise ValueError(
            "环境变量 DATABASE_URL 不是合法的数据库连接 URL。"
            "如果用户名或密码包含特殊字符，请对 userinfo 部分进行 percent-encode（例如 '@'→'%40'，':'→'%3A'）。"
            "或者不要使用 DATABASE_URL，改用 DB_USER/DB_PASSWORD/DB_HOST/DB_PORT/DB_NAME 等分字段配置。"
        ) from exc


def _build_database_url() -> str:
    dialect: str = (settings.DB_DIALECT or "mysql").lower()
    host: str = settings.DB_HOST or ""
    port_int: int = settings.DB_PORT
    user: str = settings.DB_USER or ""
    password: str = settings.DB_PASSWORD or ""
    db_name: str = settings.DB_NAME or ""

    # 如果外部提供了完整的 DATABASE_URL，优先交给 SQLAlchemy 解析后再渲染。
    # 明显包含多个未转义 @ 的 userinfo 会被拒绝，避免静默解析为错误主机。
    database_url_env = os.getenv("DATABASE_URL")
    if database_url_env:
        url_obj = _parse_database_url(database_url_env)
        return url_obj.render_as_string(hide_password=False)

    # 使用 SQLAlchemy 的 URL.create 来安全构建连接 URL，避免手动拼接导致的转义/编码问题
    if dialect in ("postgresql", "postgres"):
        # PostgreSQL 使用 asyncpg 驱动
        url_obj = URL.create(
            drivername="postgresql+asyncpg",
            username=user or None,
            password=password or None,
            host=host or None,
            port=port_int,
            database=db_name or None,
        )
        try:
            return url_obj.render_as_string(hide_password=False)
        except Exception:
            return str(url_obj)

    # 默认 MySQL 使用 aiomysql 驱动
    url_obj = URL.create(
        drivername="mysql+aiomysql",
        username=user or None,
        password=password or None,
        host=host or None,
        port=port_int,
        database=db_name or None,
    )
    try:
        return url_obj.render_as_string(hide_password=False)
    except Exception:
        return str(url_obj)


def get_async_engine() -> AsyncEngine:
    global _engine
    if _engine is None:
        database_url: str = _build_database_url()
        _engine = create_async_engine(
            database_url,
            # Streamlit runs queries on multiple thread-owned event loops.
            # Async driver connections must never be reused across those loops.
            poolclass=NullPool,
        )
    return _engine


async def fetch_all(query: str, params: Optional[Union[Iterable[Any], Dict[str, Any]]] = None) -> List[Dict[str, Any]]:
    """
    执行只读查询并返回字典列表。
    """
    engine: AsyncEngine = get_async_engine()
    async with engine.connect() as conn:
        result = await conn.execute(text(query), params or {})
        rows = result.mappings().all()
        # 将 RowMapping 转换为普通字典
        return [dict(row) for row in rows]

