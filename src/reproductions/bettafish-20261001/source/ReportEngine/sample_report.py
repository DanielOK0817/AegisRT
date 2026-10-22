"""Evidence-linked sample reporting, with deterministic counts and quotations."""
import html
import re
from urllib.parse import urlsplit, urlunsplit


def validate_themes(result, evidence):
    themes = result.get('themes', [])
    if not 2 <= len(themes) <= 8:
        raise ValueError('Expected 2-8 supported themes')
    for theme in themes:
        if not isinstance(theme.get('title'), str) or not isinstance(theme.get('summary'), str):
            raise ValueError('Missing theme text')
        if not theme['title'].strip() or not theme['summary'].strip():
            raise ValueError('Empty theme text')
        ids = theme.get('comment_ids', [])
        if not isinstance(ids, list) or not 1 <= len(ids) <= 4:
            raise ValueError('Each theme needs 1-4 source comments')
        if len(set(ids)) != len(ids) or any(key not in evidence for key in ids):
            raise ValueError('Unknown or repeated source comment')
        if re.search(r'\d|[%％]', theme['title'] + theme['summary']):
            raise ValueError('Model narrative must not introduce numeric statistics')
    return themes


def render_sample_report(topic, themes, evidence, sources, run_id):
    esc = lambda x: html.escape(str(x))
    def source_link(url, text):
        if not str(url).startswith(('https://','http://')):
            return esc(text)
        return f'<a href="{esc(url)}" target="_blank" rel="noopener noreferrer">{esc(text)}</a>'
    def quote(key):
        row = evidence[key]
        return f'<blockquote><p>{esc(row["content"])}</p><footer>{esc(key)} · {esc(row["nickname"])} · 点赞{esc(row["like_count"])} · {source_link(row["source_url"],"原视频")}</footer></blockquote>'
    video_counts = {}
    for row in evidence.values():
        parsed = urlsplit(row['source_url'])
        video_url = urlunsplit((parsed.scheme,parsed.netloc,parsed.path,'',''))
        video_counts[video_url] = video_counts.get(video_url,0)+1
    source_rows = ''.join(f'<tr><td>{source_link(url,url.rstrip("/").split("/")[-1])}</td><td>{count}</td></tr>' for url,count in video_counts.items())
    sections = ''.join(f'<section><h2>{index}. {esc(t["title"])}</h2><p class="interpretation">模型对样本观点的归纳：{esc(t["summary"])}</p>{"".join(quote(key) for key in t["comment_ids"])}</section>' for index,t in enumerate(themes,1))
    external = ''.join(f'<li>{source_link(row["url"], row.get("title") or row["url"])} <small>（{esc(row["agent"])}检索记录，内容未独立核实）</small></li>' for row in sources)
    appendix = ''.join(quote(key) for key in evidence)
    return f'''<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{esc(topic)}：评论样本报告</title>
<style>body{{font:16px/1.75 system-ui,"Microsoft YaHei",sans-serif;background:#f4f6fa;color:#203044;margin:0}}main{{max-width:1050px;margin:32px auto;background:white;padding:36px;border-radius:12px}}h1{{font-size:29px;margin-top:0}}h2{{font-size:22px;margin-top:32px}}.badge{{color:#196071;font-size:14px}}.notice{{padding:16px 20px;background:#fff4dc;border-left:4px solid #ba8218}}.stats{{display:flex;gap:18px;flex-wrap:wrap;margin:22px 0}}.stat{{background:#eef5f9;padding:14px 24px;flex:1;min-width:150px}}.stat strong{{font-size:30px;display:block}}blockquote{{margin:14px 0;padding:12px 20px;background:#f7f9fc;border-left:3px solid #b6c9d8}}blockquote p{{white-space:pre-wrap;margin:0}}footer,small{{font-size:13px;color:#536779}}a{{color:#126884;overflow-wrap:anywhere}}table{{border-collapse:collapse;width:100%}}td,th{{border:1px solid #d7e0e8;text-align:left;padding:10px}}details{{margin-top:20px}}li{{margin:10px 0}}.interpretation{{padding-bottom:8px}}@media(max-width:700px){{main{{margin:0;padding:22px}}}}</style></head><body><main>
<p class="badge">微舆复现 · 原始评论可追溯版</p><h1>{esc(topic)}话题的评论样本分析</h1>
<p class="notice">本报告分析评论中表达的观点，不判定网络传闻、财务争议或人物动机是否属实。模型只归纳主题；样本数量、点赞数及引文均由程序从采集记录填入。主题可能重叠，不计算观点占比。</p>
<div class="stats"><div class="stat"><strong>{len(video_counts)}</strong>个B站视频</div><div class="stat"><strong>{len(evidence)}</strong>条去重一级评论</div><div class="stat"><strong>{len(themes)}</strong>组有原文支持的主题</div></div>
<h2>样本与采集口径</h2><p>2026年10月1日采集，每个选定视频最多获取50条一级评论，采用平台默认顺序。本样本不代表全平台或全网，也不属于随机抽样。点赞数是采集时快照，不表示赞同某一事实主张的人数。</p>
<table><tr><th>视频来源</th><th>实际采集评论数</th></tr>{source_rows}</table>
{sections}
<h2>其他分析Agent提供的检索线索</h2><p>以下链接来自已完成的Query、Media检索记录，仅用于进一步核查。检索命中、标题或摘要不能代替原文核实，也不能自动证明传闻属实。</p><ol>{external}</ol>
<h2>局限与运行说明</h2><p>仅覆盖单平台的选定视频和有限评论，不能推算热搜排名、全网声量、群体人口属性或总体情感比例。当前主题归纳仍需人工复核，所列原文便于逐项检查。</p><p>本轮运行经历接口失败、日志恢复与人工校正。原版综合报告生成出现无来源比例等问题，未通过质量检查；本报告采用受约束的样本归纳流程。它不代表多轮无干预稳定性验收通过，也尚未接入AegisRT调度验证。</p>
<details><summary>展开全部{len(evidence)}条原始评论</summary>{appendix}</details><p class="badge">运行记录：{esc(run_id)}</p></main></body></html>'''
