import json
import pytest
from InsightEngine.nodes.search_node import FirstSearchNode,ReflectionNode

@pytest.mark.parametrize('cls',[FirstSearchNode,ReflectionNode])
def test_multiple_search_objects_keep_first_valid_plan(cls):
 first={'search_query':'目标评论','search_tool':'get_comments_for_topic','reasoning':'test','enable_sentiment':False}
 second={'search_query':'另一查询','search_tool':'search_topic_globally'}
 result=cls(None).process_output(json.dumps(first,ensure_ascii=False)+'\n'+json.dumps(second,ensure_ascii=False))
 assert result['search_query']==first['search_query']
 assert result['search_tool']==first['search_tool']
 assert result['enable_sentiment'] is False

@pytest.mark.parametrize('cls',[FirstSearchNode,ReflectionNode])
def test_search_parameters_are_not_discarded(cls):
 plan={'search_query':'目标','search_tool':'search_topic_by_date','start_date':'2026-09-01','end_date':'2026-10-01','platform':'bilibili','unexpected':'ignore'}
 result=cls(None).process_output(json.dumps(plan,ensure_ascii=False))
 for key in ('search_tool','start_date','end_date','platform'): assert result[key]==plan[key]
 assert 'unexpected' not in result
