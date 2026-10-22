import pytest
from ReportEngine.sample_report import validate_themes,render_sample_report

def fixture():
    return {'C001':{'content':'<script>bad</script>sample','nickname':'author','like_count':2,'source_url':'https://example/video'}}, {'themes':[{'title':'求证','summary':'评论者要求提供证据。','comment_ids':['C001']},{'title':'表达','summary':'评论表达了疑问。','comment_ids':['C001']}]}

def test_unknown_comment_is_rejected():
    evidence,result=fixture(); result['themes'][0]['comment_ids']=['missing']
    with pytest.raises(ValueError):validate_themes(result,evidence)

def test_unsupported_percentage_is_rejected():
    evidence,result=fixture(); result['themes'][0]['summary']='52%的用户支持。'
    with pytest.raises(ValueError):validate_themes(result,evidence)

def test_quotes_and_counts_come_from_records_and_are_escaped():
    evidence,result=fixture(); themes=validate_themes(result,evidence)
    page=render_sample_report('Topic',themes,evidence,[],'run')
    assert '<script>bad</script>' not in page
    assert '&lt;script&gt;bad&lt;/script&gt;sample' in page
    assert '点赞2' in page and '<td>1</td>' in page

def test_comment_fragments_do_not_inflate_video_count():
    evidence,result=fixture()
    evidence['C001']['source_url']='https://example/video/#reply1'
    evidence['C002']={**evidence['C001'],'source_url':'https://example/video/#reply2'}
    page=render_sample_report('Topic',validate_themes(result,evidence),evidence,[],'run')
    assert '<strong>1</strong>个B站视频' in page
    assert '<td>2</td>' in page
