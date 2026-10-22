"""Read-only regression checks against the reproduction database."""
import asyncio
import json
from concurrent.futures import ThreadPoolExecutor

from InsightEngine.utils.db import fetch_all
from InsightEngine.tools.search import MediaCrawlerDB, DatabaseQueryError


def main():
    outcomes = []
    def check(name, fn):
        try:
            fn()
            outcomes.append({'check':name, 'passed':True})
        except Exception as exc:
            root=exc.__cause__ or exc
            # Only type and first diagnostic line; never connection URLs or parameters.
            outcomes.append({'check':name, 'passed':False, 'error_type':type(root).__name__,
                             'diagnostic':str(root).splitlines()[0][:220]})

    def probe(_=None):
        assert asyncio.run(fetch_all('SELECT 1 AS probe')) == [{'probe':1}]
    check('sequential_event_loops', lambda: [probe() for _ in range(5)])
    def concurrent():
        with ThreadPoolExecutor(max_workers=4) as pool:
            list(pool.map(probe, range(12)))
    check('concurrent_event_loops', concurrent)
    db=MediaCrawlerDB()
    def invalid():
        try:
            db._execute_query('SELECT nonexistent_repro_column')
        except DatabaseQueryError:
            return
        raise AssertionError('Query failure silently returned no data')
    check('query_error_is_not_empty',invalid)
    check('global_search',lambda:db.search_topic_globally('__repro_no_match__',1))
    check('date_search',lambda:db.search_topic_by_date('__repro_no_match__','2026-01-01','2026-10-01',1))
    check('comments',lambda:db.get_comments_for_topic('__repro_no_match__',2))
    check('hot_content',lambda:db.search_hot_content('week',2))
    for platform in ('bilibili','weibo','douyin','kuaishou','xhs','zhihu','tieba'):
        check('platform_'+platform,lambda p=platform:db.search_topic_on_platform(p,'__repro_no_match__','2026-01-01','2026-10-01',1))
    print(json.dumps(outcomes,ensure_ascii=False))
    return all(x['passed'] for x in outcomes)


if __name__=='__main__':
    raise SystemExit(0 if main() else 1)
