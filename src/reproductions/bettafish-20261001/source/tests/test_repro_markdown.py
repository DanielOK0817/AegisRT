import importlib
import pytest

@pytest.mark.parametrize('engine',['InsightEngine','MediaEngine','QueryEngine'])
def test_markdown_citations_do_not_remove_report_prefix(engine):
 cls=importlib.import_module(engine+'.nodes.formatting_node').ReportFormattingNode
 report='# 标题\n\n## 第一节\n正文。\n\n## 第二节\n证据[3]。'
 assert cls.__new__(cls).process_output(report)==report

@pytest.mark.parametrize('engine',['InsightEngine','MediaEngine','QueryEngine'])
def test_explicit_think_block_is_removed_without_losing_markdown(engine):
 cls=importlib.import_module(engine+'.nodes.formatting_node').ReportFormattingNode
 assert cls.__new__(cls).process_output('<think>internal</think>\n# 标题\n正文[1]')=='# 标题\n正文[1]'
