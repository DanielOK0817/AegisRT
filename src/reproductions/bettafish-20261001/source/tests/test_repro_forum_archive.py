from ForumEngine.monitor import LogMonitor

def test_forum_reset_preserves_previous_log(tmp_path):
 monitor=LogMonitor(str(tmp_path))
 monitor.forum_log_file.write_text('[HOST] previous discussion\n',encoding='utf-8')
 monitor.clear_forum_log()
 archives=list((tmp_path/'archive').glob('forum-*.log'))
 assert len(archives)==1
 assert archives[0].read_text(encoding='utf-8')=='[HOST] previous discussion\n'
 assert 'previous discussion' not in monitor.forum_log_file.read_text(encoding='utf-8')
