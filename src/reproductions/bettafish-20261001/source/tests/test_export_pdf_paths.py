import json
import sys
from pathlib import Path
from types import ModuleType

import export_pdf


def test_project_root_is_derived_from_script_location():
    assert export_pdf.PROJECT_ROOT == Path(export_pdf.__file__).resolve().parent


def test_export_pdf_writes_to_project_reports_directory(monkeypatch, tmp_path):
    ir_path = tmp_path / "report.json"
    ir_path.write_text(json.dumps({"metadata": {"topic": "compatibility"}}), encoding="utf-8")

    class FakeRenderer:
        def render_to_bytes(self, document_ir, optimize_layout):
            assert document_ir["metadata"]["topic"] == "compatibility"
            assert optimize_layout is True
            return b"%PDF-test"

    report_engine = ModuleType("ReportEngine")
    report_engine.__path__ = []
    renderers = ModuleType("ReportEngine.renderers")
    renderers.__path__ = []
    pdf_renderer = ModuleType("ReportEngine.renderers.pdf_renderer")
    pdf_renderer.PDFRenderer = FakeRenderer
    monkeypatch.setitem(sys.modules, "ReportEngine", report_engine)
    monkeypatch.setitem(sys.modules, "ReportEngine.renderers", renderers)
    monkeypatch.setitem(sys.modules, "ReportEngine.renderers.pdf_renderer", pdf_renderer)
    monkeypatch.setattr(export_pdf, "PROJECT_ROOT", tmp_path)

    result = export_pdf.export_pdf(ir_path)

    output_path = Path(result)
    assert output_path.parent == tmp_path / "final_reports" / "pdf"
    assert output_path.read_bytes() == b"%PDF-test"
