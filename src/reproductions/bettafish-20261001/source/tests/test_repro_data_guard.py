"""An empty database must stop Insight before any paid model call."""
from unittest.mock import patch

import pytest

from InsightEngine.tools.search import MediaCrawlerDB, DBResponse, NoSourceDataError, DatabaseQueryError


def test_empty_database_is_explicit():
    db = MediaCrawlerDB()
    with patch.object(db, 'search_topic_globally', return_value=DBResponse('search', {})):
        with pytest.raises(NoSourceDataError):
            db.require_source_data()


def test_nonempty_database_passes_preflight():
    db = MediaCrawlerDB()
    with patch.object(db, 'search_topic_globally', return_value=DBResponse('search', {}, results_count=1)):
        db.require_source_data()


def test_connection_failure_is_not_empty():
    db = MediaCrawlerDB()
    with patch.object(db, 'search_topic_globally', side_effect=DatabaseQueryError('unavailable')):
        with pytest.raises(DatabaseQueryError):
            db.require_source_data()


def test_distinct_comments_with_shared_video_url_survive_deduplication():
    from InsightEngine.agent import DeepSearchAgent
    from InsightEngine.tools.search import QueryResult
    rows = [QueryResult('bilibili','comment','same text',url='https://example/video',source_table='bilibili_video_comment',source_id=str(i)) for i in range(3)]
    assert len(DeepSearchAgent._deduplicate_results(None,rows+rows)) == 3


def test_legacy_comments_without_ids_do_not_collapse_by_video_url():
    from InsightEngine.agent import DeepSearchAgent
    from InsightEngine.tools.search import QueryResult
    rows = [QueryResult('bilibili','comment',text,url='https://example/video') for text in ('first','second')]
    assert len(DeepSearchAgent._deduplicate_results(None,rows+rows)) == 2
