import json
from InsightEngine.tools.search import MediaCrawlerDB
b=MediaCrawlerDB()
r=b.get_comments_for_topic('景甜和孙宇晨',500)
assert r.results_count==127, r.results_count
assert all(x.url and x.source_keyword for x in r.results)
assert len({x.url for x in r.results})==3
assert b.get_comments_for_topic('景甜和孙宇晨',5).results_count==5
assert b.get_comments_for_topic('__unrelated_regression__',500).results_count==0
r2=b.search_topic_globally('景甜和孙宇晨',500)
assert sum(x.content_type=='comment' for x in r2.results)==127
print(json.dumps({'passed':True,'comments':r.results_count,'sources':3,'global_rows':r2.results_count}))
