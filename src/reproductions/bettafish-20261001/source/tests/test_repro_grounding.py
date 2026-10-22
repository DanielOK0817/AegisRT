import json
from unittest.mock import Mock, patch
from InsightEngine.utils.text_processing import format_search_results_for_prompt
from InsightEngine.nodes.summary_node import FirstSummaryNode, ReflectionSummaryNode


def test_comment_evidence_keeps_source_and_engagement():
    result = {'content':'sample comment', 'platform':'bilibili', 'url':'https://example/video',
              'content_type':'comment', 'author':'sample author', 'engagement':{'like_count':7},
              'published_date':'2026-10-01', 'source_id':'123'}
    evidence = json.loads(format_search_results_for_prompt([result])[0])
    for key, value in result.items():
        assert evidence[key] == value


def test_empty_reflection_preserves_summary_without_paid_generation():
    client = Mock()
    data = dict(title='Title',content='Plan',search_query='Query',search_results=[],paragraph_latest_state='Existing supported text')
    with patch('InsightEngine.nodes.summary_node.FORUM_READER_AVAILABLE',False):
        assert ReflectionSummaryNode(client).run(data) == data['paragraph_latest_state']
    client.stream_invoke_to_string.assert_not_called()


def test_empty_initial_search_reports_missing_evidence_without_generation():
    client = Mock()
    data = dict(title='Title',content='Plan',search_query='Query',search_results=[])
    with patch('InsightEngine.nodes.summary_node.FORUM_READER_AVAILABLE',False):
        text = FirstSummaryNode(client).run(data)
    assert '未检索到' in text
    client.stream_invoke_to_string.assert_not_called()
