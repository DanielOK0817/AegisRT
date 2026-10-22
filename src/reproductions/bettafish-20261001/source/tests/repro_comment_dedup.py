from InsightEngine.tools.search import MediaCrawlerDB
from InsightEngine.agent import DeepSearchAgent
r=MediaCrawlerDB().get_comments_for_topic('景甜和孙宇晨',500)
u=DeepSearchAgent._deduplicate_results(None,r.results)
print({'retrieved':len(r.results),'after_agent_deduplication':len(u)},flush=True)
assert len(u)==127, 'Different comments share the parent URL and are incorrectly deduplicated'
