"""Reuse a saved report candidate as correction input, never as an approved report."""
import hashlib
import json
from pathlib import Path
import re


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def read(path):
    return json.loads(Path(path).read_text(encoding='utf-8'))


def load_seed(root, source_task, topic, new_task):
    root = Path(root).resolve()
    if (not re.fullmatch(re.escape(root.name)+r'-report(?:-recovery-[1-9][0-9]?)?', source_task)
            or source_task == new_task):
        raise ValueError('Correction seed must identify a different report from this workflow')
    source = (root/source_task).resolve(strict=True)
    if source.parent != root:
        raise ValueError('Correction seed escapes original workflow')
    state = read(source/'state.json')
    if tuple(state.get(k) for k in ('workflow_id','task_id','role','mode','topic')) != (root.name,source_task,'report','live',topic):
        raise ValueError('Correction seed identity differs')
    if state.get('status') == 'running':
        interruption = read(root/('.'+source_task.removeprefix(root.name+'-'))/'interruption.json')
        if interruption.get('task_id') != source_task or interruption.get('reason') != 'execution_node_interrupted' or interruption.get('automatic_restart') is not False:
            raise ValueError('Running report cannot supply a recovery seed')
    elif state.get('status') != 'failed':
        raise ValueError('Only an unsuccessful report can supply a correction seed')
    budget = read(source/'model-budget.json')
    attempts = read(source/'report-validation-attempts.json')
    if not isinstance(attempts, list) or not 1 <= budget['attempts'] <= budget['limit'] <= 5:
        raise ValueError('Correction source request history is invalid')
    # Only a fully saved rejected generation can be used here. Approved candidates
    # and partial in-flight output need a different review-state continuation.
    last = attempts[-1] if attempts else {}
    if last.get('phase') != 'generation' or last.get('status') != 'rejected' or not 1 <= last.get('attempt',0) <= budget['attempts']:
        raise ValueError('Correction seed requires the latest rejected complete generation')
    names = ['state.json','model-budget.json','report-validation-attempts.json','report-evidence.json',
             f"model-report-response-{last['attempt']}.txt"]
    records = {name: {'path':str(source/name),'sha256':digest(source/name)} for name in names}
    return {'schema':'bettafish.report-correction-seed/v1','workflow_id':root.name,'source_task_id':source_task,
            'source_requests':budget['attempts'],'response_attempt':last['attempt'],
            'source_files':records,'evidence_approved':False,'requires_new_generation_and_review':True}


def correction(seed, bundle, reporting, decode):
    if seed.get('schema') != 'bettafish.report-correction-seed/v1' or seed.get('evidence_approved') is not False:
        raise ValueError('Invalid correction seed schema')
    for record in seed['source_files'].values():
        if digest(record['path']) != record['sha256']:
            raise ValueError('Correction seed bytes changed')
    if read(seed['source_files']['report-evidence.json']['path']) != bundle:
        raise ValueError('Correction seed was generated from different evidence')
    name = f"model-report-response-{seed['response_attempt']}.txt"
    candidate = decode(Path(seed['source_files'][name]['path']).read_text(encoding='utf-8'))
    if len(json.dumps(candidate,ensure_ascii=False,separators=(',',':'))) > 20000:
        raise ValueError('Correction seed exceeds complete-candidate input limit')
    try:
        reporting.validate_report(candidate,bundle)
    except ValueError as error:
        issues = getattr(error,'issues',[{'code':'invalid_report','path':'$','message':str(error)}])
        reason = str(error)
    else:
        issues, reason = [], 'Saved generation must undergo a new complete evidence review.'
    return {'validation_error':reason,'issues':issues,'previous_candidate':candidate,
            'instruction':'这是上次中断前完整保存但未通过审核的候选。按给定问题修正并完整返回七节JSON；各段提到哪个研究角色，就在同段引用该角色的报告ID。不得自动补造引用或删去必要范围限定。此次新候选仍须完整逐段逐句审核。'}
