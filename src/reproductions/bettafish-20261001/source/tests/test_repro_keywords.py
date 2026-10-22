from unittest.mock import patch
from InsightEngine.tools.keyword_optimizer import KeywordOptimizer

def test_fenced_keyword_json_preserves_names():
 o=KeywordOptimizer.__new__(KeywordOptimizer)
 content='```json\n{"keywords": ["景甜", "孙宇晨", "["], "reasoning": "test"}\n```'
 with patch.object(o,'_call_qwen_api',return_value={'success':True,'content':content}):
  result=o.optimize_keywords('景甜 孙宇晨')
 assert result.optimized_keywords==['景甜','孙宇晨']

def test_keyword_json_array():
 o=KeywordOptimizer.__new__(KeywordOptimizer)
 assert o._parse_keyword_json('Here are the keywords:\n["品牌", "产品"]')[0]==['品牌','产品']

def test_empty_keyword_response_uses_original_query():
 o=KeywordOptimizer.__new__(KeywordOptimizer)
 with patch.object(o,'_call_qwen_api',return_value={'success':True,'content':'{"keywords": ["[", "...", ""]}'}):
  assert o.optimize_keywords('景甜 孙宇晨').optimized_keywords==['景甜','孙宇晨']
