"""Evidence-bound, multi-section reporting without model-generated statistics.

This module has no model, file, or network operations. Callers verify upstream
file ownership before passing the exact research, comment and Forum contents.
"""
from __future__ import annotations

import copy
import hashlib
import html
import json
import re
from urllib.parse import urlsplit, urlunsplit

SCHEMA = "bettafish.comprehensive-evidence/v1"
REPORT_SCHEMA = "bettafish.comprehensive-report/v1"
CORRECTION_SCHEMA = "bettafish.scoped-report-correction/v1"
REVIEW_INPUT_SCHEMA = "bettafish.evidence-review-input/v3"
RESEARCH_ROLES = ("query", "media", "insight")
EVIDENCE_SEMANTICS = (
    "生成和审核共用以下证据规则。区分来源如何报道、事件是否真实和本报告的方法边界。"
    "来源明确报道某事时，可写所引媒体报道了该事，不能擅自改成媒体疑似报道；"
    "这仍只是有明确归因的报道内容，不表示本报告独立证实了事件。来源自身的可能、疑似等限定必须保留。"
    "本报告未独立核实外部事件属于方法边界，由审核输入scope.original_source_truth_verified=false支持，"
    "不要求引用文章也声明未核实。该边界只说明本报告的核验范围，不能推出事件虚假、媒体未核实、无相关报道或遗漏具体材料。"
    "谨慎措辞不能代替证据。对已报道事实优先使用来源归因，避免无依据加疑似；"
    "对方法局限明确写本报告，而非把尚需核验说成来源的结论。"
    "部分肯定、存在分歧或群体分化不能概括为广泛认可、一致肯定；每句结论的强度和范围都须与实际引用相符。"
    "审核须一次检查全部句子，包括分析和局限，不因为措辞更谨慎而自动批准或拒绝。"
)

SECTION_TITLES = {
    "summary": "摘要",
    "background": "背景资料与来源",
    "research": "多角度研究",
    "controversies": "争议与证据边界",
    "comments": "原始评论中的观点",
    "discussion": "讨论结论与核查建议",
    "limitations": "局限与覆盖范围",
}


def apply_scoped_correction(previous, proposed, issues):
    """Take only reviewer-rejected paragraphs; this does not approve evidence.

    Preserve the original shape and every unselected value. Raw responses and
    this deterministic receipt are kept by the caller for independent replay.
    """
    sections = previous.get("sections") if isinstance(previous, dict) else None
    incoming = proposed.get("sections") if isinstance(proposed, dict) else None
    if (not isinstance(sections, list) or not isinstance(incoming, list) or
            set(proposed) - {"schema", "sections"} or len(sections) != len(incoming) or
            ("schema" in proposed and proposed["schema"] != REPORT_SCHEMA)):
        raise ValueError("scoped correction must preserve the complete report structure")
    paths = set()
    for issue in issues:
        match = re.fullmatch(r"\$\.sections\[(\d+)\]\.paragraphs\[(\d+)\]", issue.get("path", ""))
        if issue.get("code") != "evidence_not_supported" or match is None:
            raise ValueError("scoped correction requires actual paragraph evidence issues")
        section, paragraph = map(int, match.groups())
        if section >= len(sections) or paragraph >= len(sections[section]["paragraphs"]):
            raise ValueError("scoped correction path is outside the reviewed report")
        paths.add(issue["path"])
    if not paths:
        raise ValueError("scoped correction requires rejected paragraphs")
    resolved = copy.deepcopy(previous)
    ignored = ["$.schema"] if previous.get("schema") != proposed.get("schema") else []
    for si, (before, after) in enumerate(zip(sections, incoming)):
        if (not isinstance(after, dict) or set(after) != {"key", "title", "paragraphs"} or
                after["key"] != before["key"] or not isinstance(after["paragraphs"], list) or
                len(after["paragraphs"]) != len(before["paragraphs"])):
            raise ValueError("scoped correction cannot add, remove or reorder sections or paragraphs")
        if after["title"] != before["title"]:
            ignored.append(f"$.sections[{si}].title")
        for pi, paragraph in enumerate(after["paragraphs"]):
            path = f"$.sections[{si}].paragraphs[{pi}]"
            if path in paths:
                resolved["sections"][si]["paragraphs"][pi] = copy.deepcopy(paragraph)
            elif paragraph != before["paragraphs"][pi]:
                ignored.append(path)
    def digest(value):
        return hashlib.sha256(json.dumps(value, ensure_ascii=False, sort_keys=True,
                                         separators=(",", ":")).encode("utf-8")).hexdigest()
    return resolved, {"schema": CORRECTION_SCHEMA, "editable_paths": sorted(paths),
        "ignored_paths": ignored, "base_sha256": digest(previous),
        "proposed_sha256": digest(proposed), "resolved_sha256": digest(resolved),
        "evidence_approved": False}


_ID = re.compile(r"[A-Za-z0-9_.-]{1,128}\Z")
_NUMBERS = re.compile(
    r"\d|[%％]|百分之|千分之|万分之|"
    r"[零〇一二三四五六七八九十百千万亿两]+(?:个|条|次|项|例|种|人|份|段|章|轮|秒|分钟|小时|天|年|月|日|元|美元|港元|倍|成|亿|万)"
)
# These name a qualitative outline, not a measured sample/platform/person count.
# Keep the exception literal and small; all other numeric expressions stay banned.
_ANALYSIS_CLASSIFICATIONS = re.compile(
    r"(?<![零〇一二三四五六七八九十百千万亿两])(?:两个层面|两个维度|三个方面)"
)
_INDEFINITE_QUALITATIVE = re.compile(
    r"(?:是|属于|构成|可视为|被视为)一个(?:不可忽视的|明显的|潜在的|重要的|值得关注的)?"
    r"(?:缺点|问题|风险|限制|局限|建议|例子)"
)
_LETTER_METAPHOR = re.compile(
    r"(?:视作|视为|看作|比作)[^，,。！？!?；;\n]{0,40}?"
    r"(?<![零〇一二三四五六七八九十百千万亿两])一份情书"
)
_UNVERIFIED_CLAIMS = re.compile(r"事实确凿|确定属实|已被证实|已全面核实|全面事实核验已通过|全面事实核验完成")
_NEGATED_FACT_CONTEXT = re.compile(
    r"(?:"
    r"(?:不能|不可|不得|不应|不宜|无法|难以|不足以)(?:据此|直接|简单|轻易)?"
    r"(?:认为|认定|确认|断言|声称|宣称|判定|推断|证明|证实|说明|表示|视为|当作|称为|判断为|意味着)"
    r"|(?:不|并不|未|尚未|从未|没有)(?:声称|宣称|认定|确认|断言|判定|认为|证明|证实|表示)"
    r"|(?:没有|尚无|缺乏)(?:充分的?)?(?:证据|依据)(?:可以|能够|足以)?(?:表明|证明|支持|说明|认定|确认)"
    r"|(?:不|并非|不是|并不是|不是说|并不是说|不属于|并不属于|未达到|尚未达到|无法达到|不能|无法|难以|尚未|未)"
    r")"
    r"(?:该主张|相关主张|相关说法|该事件|事件|此事|其|它)?"
    r"(?:是|为|属于)?[\"'“‘「『\s]*$"
)
_NEGATION_INVERTER = re.compile(
    r"(?:并非|不是|并不是|不是说|并不是说|不意味着|并不意味着|不能说|不能否认|不否认|否认|否定)[\"'“‘「『\s]*$"
)
_RESEARCH_ATTRIBUTIONS = {
    "query": re.compile(r"(?<![A-Za-z])Query(?![A-Za-z])|查询(?:视角|研究|角色|报告|分析)", re.IGNORECASE),
    "media": re.compile(r"(?<![A-Za-z])Media(?![A-Za-z])|媒体(?:视角|研究|角色)", re.IGNORECASE),
    "insight": re.compile(r"(?<![A-Za-z])Insight(?![A-Za-z])|洞察(?:视角|研究|角色|报告|分析)", re.IGNORECASE),
}
_RESEARCH_ARTIFACT = re.compile(r"基准数据|审计报告|补丁日志|测试报告")
_MATERIAL_GAP = re.compile(
    r"(?:缺乏|缺少|尚无|暂无|没有|未提供|未找到|未检索到|未见|均无|都无|并无)"
    r"[^，,。！？!?；;\n]{0,70}(?:报道|证据|回应|核验|普查|统计|量化数据|量化评价|评估数据|调查数据|基准数据|审计报告|补丁日志|测试报告)"
)
_MATERIAL_CONTEXT = re.compile(r"材料|资料|摘录|样本|检索|研究")
_LOCAL_MATERIAL_SCOPE = re.compile(
    r"(?:本段所引|所引|该|这份|上述)(?:的)?(?:来源|报道|研究报告|评论|摘录)"
    r"|(?:给定|冻结|所引|当前|评论)(?:的)?(?:评论)?样本"
)
_GLOBAL_REPORT_ABSENCE = re.compile(
    r"(?:当前|现有|给定|全部|所有|整体|全量|本次|本轮|已提供|所提供)(?:的)?(?:材料|资料|检索结果)"
    r"(?:(?:中|里)?[，,]\s*)?[^，,。！？!?；;\n]{0,70}(?:均无|都无|没有|未见|并无|尚无|暂无|缺乏)"
    r"(?:任何|相关|直接|对应|可核验|的|与核验事实|\s){0,10}报道"
)
_NEGATED_SCOPE_EXPANSION = re.compile(
    r"(?:不能|不可|不得|不应|不宜|无法)(?:直接|简单|据此)?"
    r"(?:扩大成|扩大为|扩大到|推广为|推广到|推断为|理解为|等同于|视为)"
    r"(?:(?:查询研究|媒体研究|洞察研究|Query|Media|Insight)[、和及与或\s]*)*(?:当前|现有|给定)?$",
    re.IGNORECASE,
)
# A narrow contract for information-truth judgments, not a general entailment
# classifier. In particular, technical image/audio distortion and comparative
# research quality (e.g. "evidence is relatively strong") are outside it.
_INFORMATION_OBJECT = r"(?:信息|消息|报道|新闻|传闻|说法|指控|公告|数据|证据|材料)"
_INFORMATION_JUDGMENT = re.compile(
    r"(?:失真|失实|虚假|不实|伪造|捏造|编造|造假)(?:的)?" + _INFORMATION_OBJECT
    + r"|" + _INFORMATION_OBJECT
    + r"(?:(?:的|内容|是|为|系|纯属|确属|属于|有|存在|出现|已经|已|被|遭|明显|严重|确实|"
      r"疑似|可能|或许|也许|并非|并不|不是|不|尚未|未)){0,5}"
      r"(?:失真|失实|虚假|不实|伪造|捏造|编造|造假|属实)"
)
_INFORMATION_CAUTION = re.compile(
    r"(?:"
    r"(?:疑似|可能|或许|也许|涉嫌|怀疑)(?:的|存在|出现|含有|涉及|属于|是|为|已经|已)*"
    r"|(?:尚待|有待|待|未经|尚未)(?:独立)?(?:核实|核验|证实)(?:的)?"
    r"|是否(?:属于|是|为|存在|含有)?"
    r"|(?:尚不能|不能|不可|不得|不应|不宜|不足以|无法|尚无法)(?:据此|直接|轻易)?"
    r"(?:认定|确认|断言|判定|证明|证实|称为|认为|视为|当作)"
    r"(?:其|它|这|相关|上述|这些|该|该消息|该报道)?(?:是|为)?(?:确定的?|肯定的?)?"
    r"|(?:不能|不可|不得|不应|不宜|无法)(?:把|将)[^，,。！？!?；;\n]{1,60}"
    r"(?:认定为|判定为|定性为|当作|视为)(?:确定的?|肯定的?)?"
    r"|(?:如果|假如|若)(?:存在|出现|含有)?"
    r")[\"'“‘「『\s]*$"
)
_RELIABILITY_ATTRIBUTION = re.compile(
    r"^(?P<label>Query(?:研究|报告)?|查询(?:研究|报告|分析)|Media(?:研究|报告)?|媒体研究|"
    r"Insight(?:研究|报告)?|洞察(?:研究|报告|分析)|Forum(?:讨论)?|主持讨论|"
    r"(?:本段)?所引(?:来源|报道|摘录)(?:原文)?)"
    r"(?:指出|记载|写道|提到|表述|认为|表示|描述)[：:，,]\s*(?P<body>.+)$", re.IGNORECASE | re.DOTALL)
_SOURCE_SCOPE = re.compile(r"以下|下列|如下|下面|下述|下文|上述|本段|本节|本文|本研究|本报告|该段|此处|这些|全文|全部|所有|均")
_SOURCE_UNCERTAINTY = re.compile(
    r"疑似|疑点|可能|推测|猜测|据称|不确定|无法确认|不能确定|不能断言|"
    r"(?:未|未经|尚未|待|尚待|有待)(?:核实|核验|证实|确认|验证)")


class ReportValidationError(ValueError):
    """Actionable validation issues without modifying model or evidence content."""

    def __init__(self, message=None, *, issues=None):
        self.issues = copy.deepcopy(issues) if issues is not None else [
            {"code": "evidence_validation", "path": "$", "message": str(message)}]
        self.code = self.issues[0]["code"]
        self.path = self.issues[0]["path"]
        super().__init__("; ".join(
            row["path"] + " [" + row["code"] + "]: " + row["message"] for row in self.issues))

    def as_dict(self):
        return {"error": "report_validation", "issues": copy.deepcopy(self.issues)}


def _canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def _hash(value):
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def _identifier(prefix, value):
    return prefix + _hash(_canonical(value))[:20]


def _url(value):
    if not isinstance(value, str) or re.search(r"[\x00-\x20\x7f]", value):
        return False
    try:
        parsed = urlsplit(value)
        return parsed.scheme in ("http", "https") and bool(parsed.hostname) and not parsed.username and not parsed.password
    except ValueError:
        return False


def _text(value, label, allow_empty=False):
    if not isinstance(value, str) or (not allow_empty and not value.strip()):
        raise ReportValidationError("invalid " + label)
    return value


def _generated_text(value):
    """Normalize generated Chinese prose only; never alter evidence quotations."""
    return re.sub(r"(?<=[\u4e00-\u9fff])[ \t\u00a0]+(?=[A-Za-z])|(?<=[A-Za-z])[ \t\u00a0]+(?=[\u4e00-\u9fff])", "", value)


def _has_unsupported_numbers(value):
    # Only the numeric guard sees this copy. Rendered/archived prose is unchanged.
    # "同一条件" is not a count of 条. Only the metaphor's indefinite article
    # is ignored; numbers in its qualifying text must still be checked.
    qualitative = value.replace("同一条件", "相同条件")
    qualitative = _LETTER_METAPHOR.sub(lambda match: match.group(0)[:-4] + "情书", qualitative)
    return bool(_NUMBERS.search(_INDEFINITE_QUALITATIVE.sub("", _ANALYSIS_CLASSIFICATIONS.sub("", qualitative))))


def _asserts_unverified_fact(text):
    """Require a local, direct negation for every restricted factual phrase.

    Negations in previous clauses, unrelated negative words and double negatives
    cannot turn an affirmative claim into acceptable cautious wording.
    """
    for claim in _UNVERIFIED_CLAIMS.finditer(text):
        left = re.split(r"[，,。.!！?？;；:\n]", text[:claim.start()])[-1]
        negation = _NEGATED_FACT_CONTEXT.search(left)
        if negation is None or _NEGATION_INVERTER.search(left[:negation.start()]):
            return True
    return False


def _material_scope_issues(paragraph, evidence_bundle, path):
    """Catch missing research attribution and unjustified global absence claims.

    This is a narrow prose contract, not a semantic truth checker. Each sentence
    making a gap assessment must identify its research role or cited subset;
    adding citations never repairs an expanded assertion by itself.
    """
    text = paragraph.get("text")
    if not isinstance(text, str):
        return []
    supplied = set(evidence_bundle["included_research_ids"])
    references = paragraph.get("research_ids")
    if not isinstance(references, list):
        return []
    reports = [evidence_bundle["research"][key] for key in references
               if isinstance(key, str) and key in supplied
               and evidence_bundle["research"][key]["kind"] == "report"]
    roles = {row["role"] for row in reports}
    issues = []
    for sentence in re.split(r"[。！？!?；;\n]", text):
        attributed = {role for role, pattern in _RESEARCH_ATTRIBUTIONS.items() if pattern.search(sentence)}
        missing = attributed - roles
        if missing:
            issues.append({"code": "research_attribution_missing", "path": path + ".research_ids",
                "message": "sentence names research roles without their supplied report citations: " + ", ".join(sorted(missing))})
        def locally_negated(prefix):
            clause = re.split(r"[，,。.!！?？;；:\n]", prefix)[-1]
            negation = _NEGATED_FACT_CONTEXT.search(clause) or _NEGATED_SCOPE_EXPANSION.search(clause)
            return negation is not None and not _NEGATION_INVERTER.search(clause[:negation.start()])

        def compared_gap(gap):
            return (re.search(r"(?:与|和)\s*$", sentence[:gap.start()]) is not None and
                    re.match(r"(?:是|属于)(?:不同|两种不同)(?:的)?(?:判断|概念)", sentence[gap.end():]) is not None)

        global_claims = list(_GLOBAL_REPORT_ABSENCE.finditer(sentence))
        gaps = [gap for gap in _MATERIAL_GAP.finditer(sentence)
                if not locally_negated(sentence[:gap.start()])
                and not compared_gap(gap)
                and not any(claim.start() <= gap.start() < claim.end()
                            and locally_negated(sentence[:claim.start()]) for claim in global_claims)]
        # Specific research artifacts imply a coverage assessment even when
        # the sentence omits generic words such as materials or research.
        if not (gaps and (_MATERIAL_CONTEXT.search(sentence) or
                          any(_RESEARCH_ARTIFACT.search(gap.group()) for gap in gaps))):
            continue
        if any(claim.start() <= gap.start() < claim.end() for claim in global_claims for gap in gaps):
            issues.append({"code": "global_report_absence", "path": path + ".text",
                "message": "a role or sample finding must not assert that all supplied materials contain no reporting; "
                           "distinguish reported allegations from independent verification and name the cited subset"})
        local = _LOCAL_MATERIAL_SCOPE.search(sentence)
        if not attributed and not local:
            issues.append({"code": "evidence_scope_required", "path": path + ".text",
                "message": "attribute this material-gap assessment to a cited Query, Media or Insight report, "
                           "or explicitly limit it to this paragraph's cited excerpts or frozen comment sample"})
        if not reports and not local:
            issues.append({"code": "material_assessment_requires_research", "path": path + ".research_ids",
                "message": "this paragraph assesses research coverage without citing a supplied research report"})
    return issues


def _quote_body(text):
    # An optional declarative full stop may surround a quotation. Question
    # marks are semantic: removing one would turn a source question into a fact.
    # No internal punctuation or words may be removed.
    text = text.strip().rstrip("。.").strip()
    for opening, closing in (("“", "”"), ("‘", "’"), ('"', '"'), ("「", "」"), ("『", "』")):
        if text.startswith(opening) and text.endswith(closing):
            text = text[1:-1].strip().rstrip("。.").strip()
            break
    return text


def _complete_attributed_judgment(sentence, paragraph, bundle):
    """Allow an attributed complete source paragraph, never a substring.

    The lookup uses the *supplied* pool, not the longer retained originals.
    Named research roles may only quote that role's cited report. An unrelated
    citation cannot change what a named role actually said.
    """
    attribution = _RELIABILITY_ATTRIBUTION.fullmatch(sentence.strip())
    if attribution is None:
        return False
    label = attribution["label"]
    role = next((role for role, pattern in _RESEARCH_ATTRIBUTIONS.items() if pattern.search(label)), None)
    if label.lower().startswith("forum") or label == "主持讨论":
        role = "forum"
    body = _quote_body(attribution["body"])
    field, kind, text_key = ("research_ids", "research", "text") if role else ("source_ids", "sources", "excerpt")
    cited = paragraph.get(field, [])
    if not isinstance(cited, list):
        return False
    for record in bundle["model_input"][kind]:
        if record["id"] not in cited:
            continue
        if role and (record["role"] != role or (role != "forum" and record["kind"] != "report")):
            continue
        original = record[text_key]
        # A semicolon, comma or full stop can separate a scope qualifier from
        # its claim. Retain the entire paragraph, and preceding explicit global
        # uncertainty headings. Matching a bare later sentence is insufficient.
        units = [part for part in re.split(r"\n\s*\n", original) if part.strip()]
        if record.get("excerpt_complete", True) and body == _quote_body(original):
            return True
        if not record.get("excerpt_complete", True):
            units = units[:-1]
        scoped_uncertainty = False
        for unit in units:
            if body == _quote_body(unit) and not scoped_uncertainty:
                return True
            if _SOURCE_SCOPE.search(unit) and _SOURCE_UNCERTAINTY.search(unit):
                scoped_uncertainty = True
    return False


def _information_judgment_issues(paragraph, bundle, path):
    """Prevent bare truth/falsity labels from overruling source uncertainty.

    Qualifiers belong to each local judgment. An unrelated hypothesis or a
    trailing request to verify does not undo an already asserted noun label.
    Arbitrary paraphrases still require the separate semantic review.
    """
    text = paragraph.get("text")
    if not isinstance(text, str):
        return []
    issues = []
    for sentence in _review_sentences(text):
        original = sentence["text"]
        for judgment in _INFORMATION_JUDGMENT.finditer(original):
            # Include internal modals such as "信息可能失真" in the local check.
            prefix = re.split(r"[，,。.!！?？;；:\n]", original[:judgment.start()])[-1]
            internal = re.search(r"疑似|可能|或许|也许", judgment.group())
            local = prefix + (judgment.group()[:internal.end()] if internal else "")
            caution = _INFORMATION_CAUTION.search(local)
            if caution is not None and not _NEGATION_INVERTER.search(local[:caution.start()]):
                continue
            # "信息失真的可能性" names a possibility; "失真信息需核验"
            # instead labels the information first and is deliberately rejected.
            if (re.match(r"(?:的)?(?:可能性|疑点|风险|嫌疑|迹象)", original[judgment.end():])
                    and not _NEGATION_INVERTER.search(prefix)):
                continue
            if _complete_attributed_judgment(text, paragraph, bundle):
                continue
            issues.append({"code": "unqualified_information_judgment", "path": path + ".text",
                           "sentence_index": sentence["index"], "sentence_text": original,
                           "judgment": judgment.group(),
                           "message": "qualify this information-truth judgment locally, or explicitly attribute the complete "
                                      "original paragraph from this paragraph's supplied cited evidence without dropping qualifiers; "
                                      "a trailing request to verify, a different clause's uncertainty, or a review true cannot authorize it"})
    return issues


def model_evidence_budget(max_content_length):
    """Reserve space for complete request metadata, prose and correction."""
    if type(max_content_length) is not int or max_content_length < 2000:
        raise ReportValidationError("complete report input budget must be at least 2000 characters")
    return max_content_length - min(24000, max_content_length // 2)


def build_evidence(research_states: dict, research_reports: dict, comments: dict,
                   forum: str, max_content_length: int = 12000) -> dict:
    """Keep full originals and create an explicitly bounded model-input subset.

    Source/research IDs are content hashes, stable across role dict reordering.
    Comment IDs are supplied by the caller and retain their original records.
    max_content_length bounds canonical JSON for model_input, not the appendices.
    """
    if type(max_content_length) is not int or max_content_length < 1000:
        raise ReportValidationError("model input budget must be at least 1000 characters")
    if not isinstance(research_states, dict) or not isinstance(research_reports, dict):
        raise ReportValidationError("research inputs must be objects")
    if not isinstance(comments, dict) or len(comments) < 2:
        raise ReportValidationError("at least two original comments are required")
    forum = _text(forum, "Forum discussion")
    sources, research, copied_comments = {}, {}, copy.deepcopy(comments)
    for role in RESEARCH_ROLES:
        state = research_states.get(role)
        if isinstance(state, dict) and "research_state" in state:
            state = state["research_state"]
        report = _text(research_reports.get(role), role + " research report")
        if not isinstance(state, dict) or not isinstance(state.get("paragraphs"), list) or not state["paragraphs"]:
            raise ReportValidationError("invalid " + role + " research state")
        role_sources = []
        paragraph_rows = []
        for paragraph in state["paragraphs"]:
            if not isinstance(paragraph, dict) or not isinstance(paragraph.get("research", {}), dict):
                raise ReportValidationError("invalid research paragraph")
            detail = paragraph.get("research", {})
            history = detail.get("search_history", [])
            if not isinstance(history, list):
                raise ReportValidationError("invalid search history")
            paragraph_sources = []
            if role in ("query", "media"):
                for item in history:
                    if not isinstance(item, dict):
                        raise ReportValidationError("invalid search record")
                    if not _url(item.get("url")):
                        continue
                    title = _text(item.get("title", ""), "source title", allow_empty=True)
                    content = _text(item.get("content", item.get("snippet", "")), "source content", allow_empty=True)
                    identity = {"url": item["url"], "title": title, "content": content}
                    source_id = _identifier("S", identity)
                    if source_id not in sources:
                        sources[source_id] = {"id": source_id, **identity, "roles": [],
                                              "content_sha256": _hash(content), "independently_verified": False}
                    if role not in sources[source_id]["roles"]:
                        sources[source_id]["roles"].append(role)
                    if source_id not in role_sources:
                        role_sources.append(source_id)
                    if source_id not in paragraph_sources:
                        paragraph_sources.append(source_id)
            summary = detail.get("latest_summary", "")
            if summary:
                _text(summary, "research summary")
                title = _text(paragraph.get("title", role), "research title")
                row = {"role": role, "kind": "paragraph", "title": title, "content": summary,
                       "source_ids": paragraph_sources}
                row["id"] = _identifier("R", {"role": role, "title": title, "content": summary})
                row["content_sha256"] = _hash(summary)
                paragraph_rows.append(row)
        if role in ("query", "media") and not role_sources:
            raise ReportValidationError(role + " has no usable external source records")
        row = {"role": role, "kind": "report", "title": role + "研究报告", "content": report,
               "source_ids": role_sources, "content_sha256": _hash(report)}
        row["id"] = _identifier("R", {"role": role, "kind": "report", "content": report})
        research[row["id"]] = row
        for paragraph_row in paragraph_rows:
            research[paragraph_row["id"]] = paragraph_row
    forum_id = _identifier("R", {"role": "forum", "content": forum})
    research[forum_id] = {"id": forum_id, "role": "forum", "kind": "discussion", "title": "主持讨论",
                          "content": forum, "source_ids": [], "content_sha256": _hash(forum)}
    original_ids = set()
    for key, row in copied_comments.items():
        if not isinstance(key, str) or not _ID.fullmatch(key) or not isinstance(row, dict):
            raise ReportValidationError("invalid original comment identity")
        _text(row.get("content"), "original comment content")
        if not _url(row.get("source_url")):
            raise ReportValidationError("invalid original comment source")
        original_id = str(row.get("comment_id", key))
        if not original_id or original_id in original_ids:
            raise ReportValidationError("duplicate original comment identity")
        original_ids.add(original_id)

    model_input = {"sources": [], "research": [], "comments": [], "coverage": {
        "sources_available": len(sources), "research_available": len(research),
        "comments_available": len(copied_comments), "source_records_are_unverified": True,
    }}
    included = {"sources": set(), "research": set(), "comments": set()}

    def entry(kind, key, excerpt_length=80):
        row = {"sources": sources, "research": research, "comments": copied_comments}[kind][key]
        if kind == "sources":
            return {"id": key, "roles": row["roles"], "title": row["title"], "url": row["url"],
                    "excerpt": row["content"][:excerpt_length], "excerpt_complete": len(row["content"]) <= excerpt_length}
        if kind == "research":
            return {"id": key, "role": row["role"], "kind": row["kind"], "title": row["title"],
                    "text": row["content"][:excerpt_length], "excerpt_complete": len(row["content"]) <= excerpt_length}
        return {"id": key, "content": row["content"]}

    def append(kind, key, limit, required=False, excerpt_length=80):
        if key in included[kind]:
            return True
        model_input[kind].append(entry(kind, key, excerpt_length))
        if len(_canonical(model_input)) > limit:
            model_input[kind].pop()
            if required:
                raise ReportValidationError("model input budget cannot cover required research and sources")
            return False
        included[kind].add(key)
        return True

    # Reserve coverage fields and enough material for every role, not just comments.
    working_limit = max_content_length - 220
    for role in ("query", "media"):
        role_keys = [key for key, row in sources.items() if role in row["roles"]]
        key = next((key for key in role_keys if sources[key]["content"].strip()), role_keys[0])
        append("sources", key, working_limit, required=True)
    for role in RESEARCH_ROLES:
        key = next(key for key, row in research.items() if row["role"] == role and row["kind"] == "report")
        append("research", key, working_limit, required=True)
    append("research", forum_id, working_limit, required=True)
    for key in copied_comments:
        if append("comments", key, working_limit) and len(included["comments"]) == 2:
            break
    if len(included["comments"]) < 2:
        raise ReportValidationError("model input budget cannot cover two complete original comments")

    # Exact prefixes are later rendered by the program; no model-supplied quotes.
    def grow(expandable, limit):
        while True:
            progress = False
            for kind, row in expandable:
                field = "excerpt" if kind == "sources" else "text"
                full = (sources if kind == "sources" else research)[row["id"]]["content"]
                if len(row[field]) == len(full):
                    continue
                old, old_complete = row[field], row["excerpt_complete"]
                row[field] = full[:len(old) + 256]
                row["excerpt_complete"] = len(row[field]) == len(full)
                if len(_canonical(model_input)) > limit:
                    row[field], row["excerpt_complete"] = old, old_complete
                else:
                    progress = True
            if not progress:
                break

    # Protect Forum conclusions and substantive role reports before allowing a
    # large collection of source titles/URLs to consume the available budget.
    complete_core = copy.deepcopy(model_input)
    for row in complete_core["research"]:
        if row["kind"] == "report" or row["role"] == "forum":
            row["text"] = research[row["id"]]["content"]
            row["excerpt_complete"] = True
    research_limit = max(len(_canonical(model_input)), int(working_limit * .45),
                         min(working_limit, len(_canonical(complete_core))))
    grow([("research", row) for row in model_input["research"] if row["role"] == "forum"], research_limit)
    grow([("research", row) for row in model_input["research"] if row["kind"] == "report"], research_limit)
    for key in research:
        append("research", key, research_limit)
    grow([("research", row) for row in model_input["research"] if row["kind"] == "paragraph"], research_limit)

    comment_limit = max(len(_canonical(model_input)), int(working_limit * .65))
    for key in copied_comments:
        append("comments", key, comment_limit)

    # Prefer readable extracts over many near-empty citation records. Required
    # sources retain their identities and are expanded before optional sources.
    for row in model_input["sources"]:
        full = sources[row["id"]]["content"]
        old, old_complete = row["excerpt"], row["excerpt_complete"]
        row["excerpt"] = full[:768]
        row["excerpt_complete"] = len(row["excerpt"]) == len(full)
        if len(_canonical(model_input)) > working_limit:
            row["excerpt"], row["excerpt_complete"] = old, old_complete
    source_queues = {role: [key for key, row in sources.items() if role in row["roles"]
                            and key not in included["sources"]]
                     for role in ("query", "media")}
    for keys in source_queues.values():
        keys.sort(key=lambda key: not bool(sources[key]["content"].strip()))
    for index in range(max(len(keys) for keys in source_queues.values())):
        for keys in source_queues.values():
            if index < len(keys):
                append("sources", keys[index], working_limit, excerpt_length=768)
    grow([("sources", row) for row in model_input["sources"]], working_limit)
    grow([("research", row) for row in model_input["research"]], working_limit)
    coverage = {"source_count": len(sources), "research_record_count": len(research), "raw_comment_count": len(copied_comments),
                "included_source_count": len(included["sources"]), "included_research_count": len(included["research"]),
                "model_comment_count": len(included["comments"]), "omitted_source_count": len(sources) - len(included["sources"]),
                "omitted_research_count": len(research) - len(included["research"]),
                "omitted_comment_count": len(copied_comments) - len(included["comments"]),
                "truncated_source_count": sum(not row["excerpt_complete"] for row in model_input["sources"]),
                "truncated_research_count": sum(not row["excerpt_complete"] for row in model_input["research"]),
                "research_roles_supplied": list(RESEARCH_ROLES), "forum_supplied": True,
                "complete_originals_retained": True, "model_fact_check_performed": False}
    model_input["coverage"].update({"sources_supplied": coverage["included_source_count"],
                                    "research_supplied": coverage["included_research_count"],
                                    "comments_supplied": coverage["model_comment_count"], "truncated_excerpts":
                                    coverage["truncated_source_count"] + coverage["truncated_research_count"]})
    if len(_canonical(model_input)) > max_content_length:
        raise ReportValidationError("model input coverage exceeds budget")
    coverage["model_input_characters"] = len(_canonical(model_input))
    coverage["max_content_length"] = max_content_length
    return {"schema": SCHEMA, "sources": sources, "research": research, "comments": copied_comments,
            "forum": forum, "forum_id": forum_id, "model_input": model_input, "coverage": coverage,
            "included_source_ids": [row["id"] for row in model_input["sources"]],
            "included_research_ids": [row["id"] for row in model_input["research"]],
            "included_comment_ids": [row["id"] for row in model_input["comments"]]}


def _check_bundle(bundle):
    if not isinstance(bundle, dict) or bundle.get("schema") != SCHEMA:
        raise ReportValidationError("invalid evidence bundle schema")
    for kind, included_name, text_key in (("sources", "included_source_ids", "excerpt"),
                                         ("research", "included_research_ids", "text"),
                                         ("comments", "included_comment_ids", "content")):
        mapping, supplied = bundle.get(kind), bundle.get("model_input", {}).get(kind)
        if not isinstance(mapping, dict) or not isinstance(supplied, list):
            raise ReportValidationError("invalid evidence mapping")
        ids = [row.get("id") for row in supplied]
        if ids != bundle.get(included_name) or len(set(ids)) != len(ids):
            raise ReportValidationError("model input identities differ from evidence")
        for row in supplied:
            key = row["id"]
            if key not in mapping:
                raise ReportValidationError("model input contains unknown evidence")
            original = mapping[key]["content"]
            supplied_text = row.get(text_key)
            if not isinstance(supplied_text, str) or (kind == "comments" and supplied_text != original) or not original.startswith(supplied_text):
                raise ReportValidationError("model excerpt differs from original evidence")
            if kind != "comments" and mapping[key].get("content_sha256") != _hash(original):
                raise ReportValidationError("original evidence content checksum mismatch")
            if kind == "sources" and (row.get("title"), row.get("url"), row.get("roles")) != (
                    mapping[key]["title"], mapping[key]["url"], mapping[key]["roles"]):
                raise ReportValidationError("source identity differs from model input")
            if kind == "research" and (row.get("role"), row.get("kind"), row.get("title")) != (
                    mapping[key]["role"], mapping[key]["kind"], mapping[key]["title"]):
                raise ReportValidationError("research identity differs from model input")


def validate_report(candidate: dict, evidence_bundle: dict) -> dict:
    """Validate all paragraphs and return precise issues for bounded correction."""
    _check_bundle(evidence_bundle)
    issues = []

    def issue(code, path, message):
        issues.append({"code": code, "path": path, "message": message})

    def fail(code, path, message):
        issue(code, path, message)
        raise ReportValidationError(issues=issues)

    if not isinstance(candidate, dict) or set(candidate) - {"schema", "sections"}:
        fail("invalid_report", "$", "report must contain only schema and sections")
    if "schema" in candidate and candidate["schema"] != REPORT_SCHEMA:
        issue("invalid_schema", "$.schema", "invalid report schema")
    sections = candidate.get("sections")
    if not isinstance(sections, list) or len(sections) != len(SECTION_TITLES):
        fail("invalid_sections", "$.sections", "report requires all seven sections")
    if [section.get("key") if isinstance(section, dict) else None for section in sections] != list(SECTION_TITLES):
        fail("invalid_section_order", "$.sections", "report section keys or order are invalid")
    cited_sources, cited_research = set(), set()
    for section_index, section in enumerate(sections):
        section_path = "$.sections[" + str(section_index) + "]"
        if set(section) != {"key", "title", "paragraphs"}:
            issue("invalid_section_fields", section_path, "invalid section fields")
            continue
        title = section["title"]
        if not isinstance(title, str) or not title.strip():
            issue("invalid_title", section_path + ".title", "invalid section title")
        else:
            if len(title) > 100:
                issue("title_too_long", section_path + ".title", "section title exceeds qualitative text limit")
            if _has_unsupported_numbers(title):
                issue("numeric_title", section_path + ".title", "model titles must not introduce numbers")
            if _asserts_unverified_fact(title):
                issue("unverified_claim", section_path + ".title", "report asserts unverified facts")
            for judgment_issue in _information_judgment_issues({"text": title}, evidence_bundle, section_path):
                judgment_issue["path"] = section_path + ".title"
                issues.append(judgment_issue)
        paragraphs = section["paragraphs"]
        if not isinstance(paragraphs, list) or not 1 <= len(paragraphs) <= 8:
            issue("invalid_paragraphs", section_path + ".paragraphs", "each section requires bounded paragraphs")
            continue
        section_refs = {"source_ids": set(), "comment_ids": set(), "research_ids": set()}
        for paragraph_index, paragraph in enumerate(paragraphs):
            paragraph_path = section_path + ".paragraphs[" + str(paragraph_index) + "]"
            if not isinstance(paragraph, dict) or set(paragraph) != {"text", "source_ids", "comment_ids", "research_ids"}:
                issue("invalid_paragraph_fields", paragraph_path, "invalid report paragraph fields")
                continue
            text = paragraph["text"]
            if not isinstance(text, str) or not text.strip():
                issue("invalid_text", paragraph_path + ".text", "invalid paragraph text")
            else:
                if len(text) > 1800:
                    issue("text_too_long", paragraph_path + ".text", "report paragraph exceeds qualitative text limit")
                if _has_unsupported_numbers(text):
                    issue("numeric_narrative", paragraph_path + ".text", "model narrative must not introduce numeric statistics or dates")
                if _asserts_unverified_fact(text):
                    issue("unverified_claim", paragraph_path + ".text", "report asserts unverified facts")
            ref_count = 0
            for field, mapping, included in (("source_ids", "sources", "included_source_ids"),
                                             ("comment_ids", "comments", "included_comment_ids"),
                                             ("research_ids", "research", "included_research_ids")):
                values = paragraph[field]
                citation_path = paragraph_path + "." + field
                if not isinstance(values, list) or len(values) > 12 or any(not isinstance(key, str) for key in values):
                    issue("invalid_citations", citation_path, "invalid paragraph citations")
                    continue
                if len(set(values)) != len(values):
                    issue("duplicate_citations", citation_path, "repeated paragraph citations")
                for citation_index, key in enumerate(values):
                    identity_path = citation_path + "[" + str(citation_index) + "]"
                    if key not in evidence_bundle[mapping]:
                        issue("unknown_citation", identity_path, "unknown " + field + " citation: " + key[:128])
                    elif key not in evidence_bundle[included]:
                        issue("unsupplied_citation", identity_path, "citation was not supplied to the model: " + key[:128])
                    else:
                        section_refs[field].add(key)
                ref_count += len(values)
            if not ref_count:
                issue("missing_citations", paragraph_path, "each paragraph requires original evidence citations")
            issues.extend(_material_scope_issues(paragraph, evidence_bundle, paragraph_path))
            issues.extend(_information_judgment_issues(paragraph, evidence_bundle, paragraph_path))
        cited_sources.update(section_refs["source_ids"])
        cited_research.update(section_refs["research_ids"])
        if section["key"] in ("background", "controversies") and not section_refs["source_ids"]:
            issue("external_sources_required", section_path, "background and controversies sections require external sources")
        if section["key"] == "comments" and not section_refs["comment_ids"]:
            issue("comments_required", section_path, "comment section requires original comments")
        if section["key"] == "discussion" and evidence_bundle["forum_id"] not in section_refs["research_ids"]:
            issue("forum_required", section_path, "discussion section must cite Forum evidence")
        if section["key"] == "research":
            roles = {evidence_bundle["research"][key]["role"] for key in section_refs["research_ids"]}
            if not set(RESEARCH_ROLES).issubset(roles):
                issue("research_roles_required", section_path, "research section must cover Query, Media and Insight")
    source_roles = {role for key in cited_sources for role in evidence_bundle["sources"][key]["roles"]}
    if not {"query", "media"}.issubset(source_roles):
        issue("source_roles_required", "$.sections", "report must cite Query and Media external sources")
    if issues:
        raise ReportValidationError(issues=issues)
    return copy.deepcopy(candidate)


def _review_paragraphs(candidate):
    return [
        {"path": f"$.sections[{si}].paragraphs[{pi}]", "section_key": section["key"],
         "section_title": section["title"], **copy.deepcopy(paragraph),
         "sentences": _review_sentences(paragraph["text"])}
        for si, section in enumerate(candidate["sections"])
        for pi, paragraph in enumerate(section["paragraphs"])]


def _review_sentences(text):
    """Partition original prose without rewriting punctuation or whitespace.

    English stops followed by whitespace, a closing quote or end of text are
    boundaries; dots inside decimal values, abbreviations or URLs stay intact.
    Closing quotes and following whitespace belong to the preceding sentence.
    """
    boundary = re.compile(r'''(?:[。！？!?；;]+|\.(?=\s|$|[”’"'）)\]】」』]))[”’"'）)\]】」』]*''')
    pieces, start = [], 0
    for match in boundary.finditer(text):
        end = match.end()
        while end < len(text) and text[end].isspace():
            end += 1
        if end > start:
            pieces.append(text[start:end])
            start = end
    if start < len(text):
        pieces.append(text[start:])
    return [{"index": index, "text": original} for index, original in enumerate(pieces)]


def build_review_input(candidate: dict, evidence_bundle: dict,
                       max_content_length: int = 60000, *, sample_scope=None) -> dict:
    """Provide the frozen supplied pool and exact sentences for support review.

    Uncited evidence can reveal contradictions or missing citations but cannot
    support a paragraph. Each paragraph's own citations remain its boundary.
    Complete supplied excerpts, prose and metadata are never shortened to fit.
    This evaluates support within saved material, not real-world source truth.
    """
    candidate = validate_report(candidate, evidence_bundle)
    paragraphs = _review_paragraphs(candidate)
    supplied = {kind: copy.deepcopy(evidence_bundle["model_input"][kind])
                for kind in ("sources", "research", "comments")}
    if sample_scope is not None and not isinstance(sample_scope, dict):
        raise ValueError("sample scope must be verified metadata or null")
    for paragraph in paragraphs:
        paragraph["citation_index"] = []
        for field, kind in (("source_ids", "sources"), ("comment_ids", "comments"), ("research_ids", "research")):
            records = {row["id"]: row for row in supplied[kind]}
            for identity in paragraph[field]:
                paragraph["citation_index"].append({"id": identity, "kind": kind,
                    "role": records[identity].get("role")})
    review_input = {"schema": REVIEW_INPUT_SCHEMA, "scope": {
        "original_source_truth_verified": False, "cross_paragraph_evidence_allowed": False,
        "source_titles_are_clues_only": True, "research_and_forum_are_summaries_or_advice": True,
        "complete_paragraph_coverage_required": True, "sentence_checks_required": True,
        "uncited_evidence_for_contradiction_only": True,
        "sample_scope_for_sampling_limits_only": True,
        "citation_presence_is_not_semantic_support": True,
    }, "paragraphs": paragraphs, "evidence": supplied, "sample_scope": copy.deepcopy(sample_scope)}
    characters = len(_canonical(review_input))
    if type(max_content_length) is not int or max_content_length < 1 or characters > max_content_length:
        raise ReportValidationError(issues=[{
            "code": "review_input_budget", "path": "$",
            "message": f"complete review input requires {characters} characters including prose and metadata; "
                       "frozen supplied excerpts cannot be truncated to fit the configured budget"}])
    return review_input


def contradictory_missing_citations(reason, allowed):
    """Detect explicit missing-ID claims, not whether a cited source supports prose.

    Narrow literal check: ambiguous natural-language reasons still require the
    model review. A detected contradiction requests a new review, never approval.
    """
    if not isinstance(reason, str):
        return []
    marker = re.compile(r"(?:并未引用|未引用|没有引用|漏引|缺少引用|not cited|not referenced|missing citation)", re.I)
    identity_pattern = re.compile(r"(?<![A-Za-z0-9_.-])[SRC][A-Za-z0-9_.-]{1,128}(?![A-Za-z0-9_.-])")
    list_connector = re.compile(r"[\s）)\]】」』]*(?:、|和|及|与|以及|and)[\s（(\[【「『]*", re.I)
    # A statement about uncited *content* or material outside a cited record
    # does not assert that the record's identity is absent. Such a rejection
    # still goes through evidence correction and full review, never approval.
    content_suffix = re.compile(
        r"[\s）)\]】」』]*(?:以外|之外|中(?:的|提到|所)|所述|所载|所提|"
        r"的(?:具体|内容|结论|建议|观点|细节)|['’]s\b)", re.I)
    found = set()
    for match in marker.finditer(reason):
        if re.search(r"(?:并非|不是|并不是|不是说|并不是说|不意味着|并不意味着)\s*$", reason[:match.start()]):
            continue
        clause = re.split(r"[，,。；;\n]", reason[match.end():], maxsplit=1)[0][:180]
        previous = None
        for identity in identity_pattern.finditer(clause):
            if previous is not None and not list_connector.fullmatch(clause[previous.end():identity.start()]):
                break
            previous = identity
            if content_suffix.match(clause[identity.end():]):
                continue
            value = identity[0] if identity[0] in allowed else identity[0].rstrip(".")
            if value in allowed:
                found.add(value)
    return sorted(found)


def validate_evidence_review(response: dict, candidate: dict,
                             evidence_bundle: dict) -> tuple[dict, list[dict]]:
    """Validate exhaustive paragraph and sentence checks for bounded correction.

    Malformed, incomplete or inconsistent checks raise ReportValidationError.
    Every unsupported sentence returns an issue at the original paragraph path
    with its exact sentence index and text. Uncited evidence is never support.
    The caller must require an empty issues list before publishing the report.
    Neither successful checks nor this validator verify original-source truth.
    """
    candidate = validate_report(candidate, evidence_bundle)
    expected = {row["path"]: row for row in _review_paragraphs(candidate)}
    structural, unsupported, seen = [], [], set()

    def issue(code, path, message):
        structural.append({"code": code, "path": path, "message": message})

    def check_support(row, row_path, allowed):
        if type(row["supported"]) is not bool:
            issue("invalid_support_boolean", row_path + ".supported", "supported must be a strict JSON boolean")
        reason = row["reason"]
        if not isinstance(reason, str) or not reason.strip() or len(reason) > 2000:
            issue("invalid_review_reason", row_path + ".reason", "check requires a nonempty specific reason within the review text limit")
        contradictions = contradictory_missing_citations(reason, allowed)
        if contradictions:
            issue("contradictory_missing_citation", row_path + ".reason",
                  "These IDs are already cited by this paragraph: " + ", ".join(contradictions) +
                  ". Recheck their full supplied content; citation presence alone does not establish semantic support.")
        ids = row["evidence_ids"]
        valid_ids = isinstance(ids, list) and 1 <= len(ids) <= len(allowed) and all(isinstance(key, str) for key in ids)
        if not valid_ids:
            issue("invalid_review_evidence_ids", row_path + ".evidence_ids", "check requires at least one original citation from this paragraph")
        elif len(set(ids)) != len(ids):
            issue("duplicate_review_evidence_ids", row_path + ".evidence_ids", "check repeats evidence identities")
        else:
            for citation_index, key in enumerate(ids):
                if key not in allowed:
                    issue("foreign_review_evidence", row_path + f".evidence_ids[{citation_index}]",
                          "evidence was not supplied and cited by this paragraph: " + key[:128])

    if not isinstance(response, dict) or set(response) != {"checks"} or not isinstance(response.get("checks"), list):
        raise ReportValidationError(issues=[{
            "code": "invalid_evidence_review", "path": "$", "message": "review must contain only a checks array"}])
    for index, row in enumerate(response["checks"]):
        row_path = f"$.checks[{index}]"
        if not isinstance(row, dict) or set(row) != {"path", "supported", "reason", "evidence_ids", "sentence_checks"}:
            issue("invalid_review_fields", row_path, "each check requires only path, supported, reason, evidence_ids and sentence_checks")
            continue
        paragraph_path = row["path"]
        if not isinstance(paragraph_path, str) or paragraph_path not in expected:
            issue("unknown_review_path", row_path + ".path", "check does not identify an actual report paragraph")
            continue
        if paragraph_path in seen:
            issue("duplicate_review_path", row_path + ".path", "paragraph was checked more than once: " + paragraph_path)
        seen.add(paragraph_path)
        paragraph = expected[paragraph_path]
        allowed = set(paragraph["source_ids"] + paragraph["comment_ids"] + paragraph["research_ids"])
        check_support(row, row_path, allowed)
        sentence_checks = row["sentence_checks"]
        if not isinstance(sentence_checks, list):
            issue("invalid_sentence_checks", row_path + ".sentence_checks", "every paragraph requires a complete sentence_checks array")
            continue
        expected_sentences = {sentence["index"]: sentence for sentence in paragraph["sentences"]}
        sentence_seen, sentence_values = set(), []
        for sentence_position, sentence_check in enumerate(sentence_checks):
            sentence_path = row_path + f".sentence_checks[{sentence_position}]"
            if not isinstance(sentence_check, dict) or set(sentence_check) != {"index", "supported", "reason", "evidence_ids"}:
                issue("invalid_sentence_review_fields", sentence_path, "each sentence check requires only index, supported, reason and evidence_ids")
                continue
            sentence_index = sentence_check["index"]
            if type(sentence_index) is not int or sentence_index not in expected_sentences:
                issue("unknown_review_sentence", sentence_path + ".index", "check does not identify an actual sentence in this paragraph")
                continue
            if sentence_index in sentence_seen:
                issue("duplicate_review_sentence", sentence_path + ".index", "sentence was checked more than once")
            sentence_seen.add(sentence_index)
            check_support(sentence_check, sentence_path, allowed)
            sentence_values.append(sentence_check["supported"])
            if sentence_check["supported"] is False:
                unsupported.append({"code": "evidence_not_supported", "path": paragraph_path,
                                    "sentence_index": sentence_index,
                                    "sentence_text": expected_sentences[sentence_index]["text"],
                                    "message": sentence_check["reason"],
                                    "evidence_ids": copy.deepcopy(sentence_check["evidence_ids"])})
        for sentence_index in expected_sentences:
            if sentence_index not in sentence_seen:
                issue("missing_review_sentence", paragraph_path + f".sentences[{sentence_index}]",
                      "every original sentence requires its own complete support check")
        if sentence_seen == set(expected_sentences) and all(type(value) is bool for value in sentence_values):
            if type(row["supported"]) is bool and row["supported"] != all(sentence_values):
                issue("inconsistent_review_support", row_path + ".supported",
                      "paragraph support must equal the conjunction of all original sentence checks")
    for paragraph_path in expected:
        if paragraph_path not in seen:
            issue("missing_review_check", paragraph_path, "every report paragraph requires its own complete support check")
    if structural:
        raise ReportValidationError(issues=structural)
    return copy.deepcopy(response), unsupported


def render_report(topic: str, candidate: dict, evidence_bundle: dict, run_id: str,
                  mode: str, sample_scope=None) -> str:
    """Render model prose as text and bind quotations/counts from exact records."""
    candidate = validate_report(candidate, evidence_bundle)
    _text(topic, "topic")
    _text(run_id, "run identity")
    if mode not in ("live", "replay"):
        raise ReportValidationError("invalid report mode")
    if sample_scope is not None and not isinstance(sample_scope, dict):
        raise ReportValidationError("sample scope must be an object")
    esc = lambda value: html.escape(str(value), quote=True)

    def link(url, title):
        if not _url(url):
            return esc(title)
        return f'<a href="{esc(url)}" target="_blank" rel="noopener noreferrer" referrerpolicy="no-referrer">{esc(title)}</a>'

    def comment_quote(key):
        row = evidence_bundle["comments"][key]
        metadata = key + " · " + str(row.get("nickname", "匿名"))
        if type(row.get("like_count")) is int and row["like_count"] >= 0:
            metadata += " · 采集时点赞" + str(row["like_count"])
        return (f'<blockquote data-comment-id="{esc(key)}"><p>{esc(row["content"])}</p>'
                f'<footer>{esc(metadata)} · {link(row["source_url"], "原始来源")}</footer></blockquote>')

    supplied_sources = {row["id"]: row for row in evidence_bundle["model_input"]["sources"]}
    supplied_research = {row["id"]: row for row in evidence_bundle["model_input"]["research"]}

    def source_quote(key):
        row, supplied = evidence_bundle["sources"][key], supplied_sources[key]
        quote = '<p>' + esc(supplied["excerpt"]) + '</p>' if supplied["excerpt"] else '<p>该检索记录没有保存正文，标题只作来源线索。</p>'
        cut = '' if supplied["excerpt_complete"] else '<small>以上为本轮送入模型的原始摘录，完整记录见附录。</small>'
        return (f'<blockquote data-source-id="{esc(key)}">{quote}{cut}<footer>{esc(key)} · '
                f'{link(row["url"], row["title"] or row["url"])} · 检索记录，未独立核实</footer></blockquote>')

    def research_quote(key):
        row, supplied = evidence_bundle["research"][key], supplied_research[key]
        cut = '' if supplied["excerpt_complete"] else '<small>以上为本轮送入模型的摘录，完整研究正文见附录。</small>'
        return (f'<blockquote data-research-id="{esc(key)}"><p>{esc(supplied["text"])}</p>{cut}'
                f'<footer>{esc(key)} · {esc(row["title"])} · Agent研究材料，非独立事实核验</footer></blockquote>')

    rendered_sections = []
    for section in candidate["sections"]:
        paragraphs = []
        for row in section["paragraphs"]:
            quotes = ''.join(source_quote(key) for key in row["source_ids"])
            quotes += ''.join(comment_quote(key) for key in row["comment_ids"])
            quotes += ''.join(research_quote(key) for key in row["research_ids"])
            label = "模型综合归纳：" if mode == "live" else "离线材料说明："
            paragraphs.append('<p class="analysis">' + label + esc(_generated_text(row["text"])) + '</p>' + quotes)
        rendered_sections.append(f'<section id="{esc(section["key"])}"><h2>{esc(_generated_text(section["title"]))}</h2>{"".join(paragraphs)}</section>')
    source_appendix = ''.join(
        f'<article id="source-{esc(key)}"><h3>{esc(key)} · {link(row["url"], row["title"] or row["url"])}</h3>'
        f'<p class="original">{esc(row["content"])}</p><small>{esc("、".join(row["roles"]))}检索原始记录；原文中的数字和主张未独立核实。</small></article>'
        for key, row in evidence_bundle["sources"].items())
    research_appendix = ''.join(
        f'<article id="research-{esc(key)}"><h3>{esc(row["title"])} · {esc(key)}</h3>'
        f'<p class="original">{esc(row["content"])}</p></article>' for key, row in evidence_bundle["research"].items())
    video_counts = {}
    for row in evidence_bundle["comments"].values():
        parsed = urlsplit(row["source_url"])
        source = urlunsplit((parsed.scheme, parsed.netloc, parsed.path, "", ""))
        video_counts[source] = video_counts.get(source, 0) + 1
    source_rows = ''.join(f'<tr><td>{link(url, url)}</td><td>{count}</td></tr>' for url, count in video_counts.items())
    c = evidence_bundle["coverage"]
    stats = (f'<p>冻结原始评论{len(evidence_bundle["comments"])}条，来自{len(video_counts)}个来源页面；'
             f'本轮送入模型的评论{len(evidence_bundle["included_comment_ids"])}条。检索原始记录{len(evidence_bundle["sources"])}项，'
             f'本轮送入{len(evidence_bundle["included_source_ids"])}项；研究与讨论记录{len(evidence_bundle["research"])}项，'
             f'本轮送入{len(evidence_bundle["included_research_ids"])}项。</p>'
             f'<p>本轮存在截断的检索摘录{c["truncated_source_count"]}项、研究摘录{c["truncated_research_count"]}项。'
             '完整原始记录保留在附录；未送入的材料不作为模型段落的引用依据。以上计数由程序计算，主题和段落可以重叠，不推算总体占比。</p>')
    if mode == "replay":
        stats = stats.replace("本轮送入模型的评论", "本轮离线验证允许引用的评论")
    scope = '' if sample_scope is None else '<section><h2>本轮样本口径</h2><pre>' + esc(json.dumps(sample_scope, ensure_ascii=False, indent=2)) + '</pre></section>'
    mode_note = '本轮调用真实模型进行材料综合。' if mode == "live" else '本轮复用冻结研究文件，未调用模型；以下离线说明不构成新的研究结论。'
    navigation = ' · '.join(f'<a href="#{key}">{title}</a>' for key, title in SECTION_TITLES.items())
    return ('<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">'
            '<meta name="viewport" content="width=device-width, initial-scale=1">'
            '<meta http-equiv="Content-Security-Policy" content="default-src \'none\'; style-src \'unsafe-inline\'; base-uri \'none\'">'
            f'<title>{esc(_generated_text(topic))}：综合研究报告</title><style>'
            'body{font:16px/1.8 system-ui,"Microsoft YaHei",sans-serif;background:#f4f6fa;color:#203044;margin:0}'
            'main{max-width:1050px;margin:28px auto;background:white;padding:34px;border-radius:12px}'
            'h1{font-size:29px}h2{font-size:23px;margin-top:32px}h3{font-size:18px}p{overflow-wrap:anywhere}'
            '.notice{padding:16px;background:#fff4dc;border-left:4px solid #ba8218}'
            'blockquote{margin:14px 0;padding:12px 20px;background:#f7f9fc;border-left:3px solid #b6c9d8}'
            'blockquote p,.original,pre{white-space:pre-wrap;overflow-wrap:anywhere}footer,small{font-size:13px;color:#536779}'
            'a{color:#126884;overflow-wrap:anywhere}table{border-collapse:collapse;width:100%}'
            'td,th{border:1px solid #d7e0e8;text-align:left;padding:10px}details{margin-top:20px}'
            '@media(max-width:700px){main{margin:0;padding:20px}}'
            '</style></head><body><main><p>微舆 · 可追溯综合研究</p>'
            f'<h1>{esc(_generated_text(topic))}综合研究报告</h1><p class="notice">'
            '综合研究内容来自Query、Media、Insight和Forum材料，评论观点与检索主张均须区分来源及不确定性。'
            '检索命中、标题、Agent总结和模型段落不能自动证明事件属实。本报告不宣称全面事实核验。'
            f'模式为{esc(mode)}，{mode_note}</p><nav>{navigation}</nav>'
            '<section><h2>程序统计与实际覆盖</h2>' + stats + '<table><tr><th>评论来源页面</th><th>冻结评论记录数</th></tr>'
            + source_rows + '</table></section>' + ''.join(rendered_sections) + scope
            + '<section><h2>原始材料与引用附录</h2><p>附录保留原文，包含未经证实的来源主张与观点。原文数字由程序按原记录展示，不代表核实结论。</p>'
            + '<details><summary>全部检索原始记录</summary>' + source_appendix + '</details>'
            + '<details><summary>全部研究正文与主持讨论</summary>' + research_appendix + '</details>'
            + f'<details><summary>全部{len(evidence_bundle["comments"])}条原始评论</summary>'
            + ''.join(comment_quote(key) for key in evidence_bundle["comments"]) + '</details></section>'
            + f'<p>运行记录：{esc(run_id)}</p></main></body></html>')
