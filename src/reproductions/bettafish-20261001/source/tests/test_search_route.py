import atexit
import importlib.util
import sys
import threading
from pathlib import Path
from types import ModuleType
from unittest.mock import Mock, patch

import pytest
from flask import Blueprint


class DummyThread:
    def __init__(self, *args, **kwargs):
        pass

    def start(self):
        pass


class DummySocketIO:
    def __init__(self, *args, **kwargs):
        pass

    def emit(self, *args, **kwargs):
        pass

    def on(self, _event):
        return lambda function: function

    def run(self, *args, **kwargs):
        pass

    def stop(self):
        pass


def _remove_modules(prefixes):
    saved = {
        name: module
        for name, module in list(sys.modules.items())
        if any(name == prefix or name.startswith(f"{prefix}.") for prefix in prefixes)
    }
    for name in saved:
        sys.modules.pop(name, None)
    return saved


@pytest.fixture
def root_app(monkeypatch, tmp_path):
    prefixes = ("bettafish_app_under_test", "MindSpider", "ReportEngine", "flask_socketio")
    saved_modules = _remove_modules(prefixes)

    mindspider_package = ModuleType("MindSpider")
    mindspider_package.__path__ = []
    mindspider_main = ModuleType("MindSpider.main")
    mindspider_main.MindSpider = type("MindSpider", (), {})

    report_package = ModuleType("ReportEngine")
    report_package.__path__ = []
    report_interface = ModuleType("ReportEngine.flask_interface")
    report_interface.report_bp = Blueprint("stub_report", __name__)
    report_interface.initialize_report_engine = lambda: True

    socketio_module = ModuleType("flask_socketio")
    socketio_module.SocketIO = DummySocketIO
    socketio_module.emit = lambda *args, **kwargs: None

    monkeypatch.setitem(sys.modules, "MindSpider", mindspider_package)
    monkeypatch.setitem(sys.modules, "MindSpider.main", mindspider_main)
    monkeypatch.setitem(sys.modules, "ReportEngine", report_package)
    monkeypatch.setitem(sys.modules, "ReportEngine.flask_interface", report_interface)
    monkeypatch.setitem(sys.modules, "flask_socketio", socketio_module)
    monkeypatch.chdir(tmp_path)

    source_path = Path(__file__).resolve().parents[1] / "app.py"
    spec = importlib.util.spec_from_file_location("bettafish_app_under_test", source_path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module

    with patch.object(threading, "Thread", DummyThread), patch.object(
        atexit, "register", lambda *args, **kwargs: None
    ):
        spec.loader.exec_module(module)

    try:
        yield module, module.app.test_client()
    finally:
        _remove_modules(prefixes)
        sys.modules.update(saved_modules)


def test_search_skips_forum_and_calls_searchable_engine(root_app, monkeypatch):
    module, client = root_app
    module.processes = {
        "insight": {"status": "running"},
        "forum": {"status": "running"},
    }
    monkeypatch.setattr(module, "check_app_status", lambda: None)
    engine_response = Mock(status_code=200)
    engine_response.json.return_value = {"success": True, "items": ["result"]}
    post = Mock(return_value=engine_response)
    monkeypatch.setattr(module.requests, "post", post)

    response = client.post("/api/search", json={"query": "BettaFish"})

    assert response.status_code == 200
    assert response.get_json() == {
        "success": True,
        "query": "BettaFish",
        "results": {"insight": {"success": True, "items": ["result"]}},
    }
    post.assert_called_once_with(
        "http://localhost:8501/api/search",
        json={"query": "BettaFish"},
        timeout=10,
    )


def test_search_with_only_forum_returns_empty_engine_results(root_app, monkeypatch):
    module, client = root_app
    module.processes = {"forum": {"status": "running"}}
    monkeypatch.setattr(module, "check_app_status", lambda: None)
    post = Mock()
    monkeypatch.setattr(module.requests, "post", post)

    response = client.post("/api/search", json={"query": "BettaFish"})

    assert response.status_code == 200
    assert response.get_json() == {
        "success": True,
        "query": "BettaFish",
        "results": {},
    }
    post.assert_not_called()
