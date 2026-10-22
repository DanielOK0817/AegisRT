"""Offline regression checks for research, discussion and feedback scheduling."""

import copy
import importlib.util
import io
import json
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("bettafish_full_runner", ROOT / "runner.py")
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)
spec = importlib.util.spec_from_file_location("bettafish_full_adapter", ROOT / "adapter.py")
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)


def options(directory, mode="live"):
    root = Path(directory)
    return SimpleNamespace(
        workflow_id="full-workflow-test", run_dir=root / "runs", input_dir=root / "inputs",
        source_dir=root / "source", python=Path(sys.executable), aegisrt=root / "unused-runtime",
        env_file=None, mode=mode, flow_variant="comprehensive" if mode == "live" else "minimal",
        topic="测试话题", task_timeout=60, wall_timeout=10, max_paragraphs=5, max_reflections=2,
        insight_max_reflections=3,
        model_request_limits=[40, 40, 64, 1, 2], refinement_request_limits=[20, 20, 32],
        max_content_length=100000, max_output_tokens=4000,
        port=12345, cgroup_root=None, resume_dir=None, fixed_topic_search=False,
        cancel_file=None, custom_template=None,
    )


def task_map(args):
    return {task["id"]: task for task in runner.make_tasks(args, args.topic)["tasks"]}


def topological_order(tasks):
    pending = {key: set(value["dependencies"]) for key, value in tasks.items()}
    order = []
    while pending:
        ready = sorted(key for key, dependencies in pending.items() if dependencies <= set(order))
        if not ready:
            raise AssertionError("Workflow has a cycle or references a missing dependency")
        for key in ready:
            order.append(key)
            del pending[key]
    return order


class FullWorkflowGraphTests(unittest.TestCase):
    def test_default_report_budget_is_five_with_total_223(self):
        args = options(".")
        args.model_request_limits = list(runner.LIVE_REQUEST_LIMITS)
        args.refinement_request_limits = list(runner.REFINEMENT_REQUEST_LIMITS)
        tasks = task_map(args)
        budgets = {key: json.loads(task["goal"])["max_model_requests"] for key, task in tasks.items()}
        self.assertEqual(budgets[args.workflow_id + "-report"], 5)
        self.assertEqual(sum(budgets.values()), 223)

    def test_cli_accepts_five_report_requests_and_rejects_a_sixth(self):
        base = ["runner.py", "--input-dir", "inputs", "--aegisrt", "runtime", "--source-dir", "source",
                "--run-dir", "runs", "--mode", "live", "--workflow-id", "budget-cli"]
        outcome = {"workflow_id": "budget-cli", "status": "completed", "mode": "live"}
        with patch.object(sys, "argv", base + ["--model-request-limits", "40,40,64,1,5"]), \
                patch.object(runner, "run_workflow", return_value=outcome) as workflow, \
                patch.object(sys, "stdout", io.StringIO()):
            runner.main()
        self.assertEqual(workflow.call_args.args[0].model_request_limits[-1], 5)
        for limits in ("40,40,64,1,6", "40,40,64,2,5", "40,40,64,3,5"):
            with self.subTest(limits=limits), patch.object(sys, "argv", base + ["--model-request-limits", limits]), \
                    patch.object(sys, "stderr", io.StringIO()), patch.object(runner, "run_workflow") as workflow:
                with self.assertRaises(SystemExit) as exit_result:
                    runner.main()
                self.assertEqual(exit_result.exception.code, 2)
                workflow.assert_not_called()

    def test_live_graph_contains_initial_feedback_final_discussion_and_report(self):
        tasks = task_map(options("."))
        workflow = "full-workflow-test"
        expected = {f"{workflow}-{role}" for role in runner.ROLES}
        expected |= {f"{workflow}-{role}-feedback" for role in runner.ROLES[:3]}
        expected.add(f"{workflow}-forum-final")
        self.assertEqual(set(tasks), expected)
        self.assertEqual(len(topological_order(tasks)), 9)
        for role in runner.ROLES[:3]:
            self.assertEqual(tasks[f"{workflow}-{role}"]["dependencies"], [])
            self.assertEqual(set(tasks[f"{workflow}-{role}-feedback"]["dependencies"]),
                             {f"{workflow}-{role}", f"{workflow}-forum"})
        refinement = {f"{workflow}-{role}-feedback" for role in runner.ROLES[:3]}
        self.assertEqual(set(tasks[f"{workflow}-forum"]["dependencies"]),
                         {f"{workflow}-{role}" for role in runner.ROLES[:3]})
        self.assertEqual(set(tasks[f"{workflow}-forum-final"]["dependencies"]), refinement)
        self.assertEqual(set(tasks[f"{workflow}-report"]["dependencies"]),
                         refinement | {f"{workflow}-forum-final"})

    def test_discussion_is_a_real_upstream_input_to_every_refinement(self):
        tasks = task_map(options("."))
        workflow = "full-workflow-test"
        for role in runner.ROLES[:3]:
            initial = tasks[f"{workflow}-{role}"]
            feedback = tasks[f"{workflow}-{role}-feedback"]
            self.assertEqual(initial["agent_id"], feedback["agent_id"])
            self.assertEqual(json.loads(initial["goal"])["research_phase"], "initial")
            self.assertEqual(json.loads(feedback["goal"])["research_phase"], "refinement")
            self.assertIn(f"{workflow}-forum", feedback["dependencies"])
            self.assertEqual(initial["max_attempts"], 1)
            self.assertEqual(feedback["max_attempts"], 1)
            self.assertNotEqual(initial["idempotency_key"], feedback["idempotency_key"])

    def test_full_research_budget_is_not_the_eight_request_demo(self):
        tasks = task_map(options("."))
        budgets = {key: json.loads(task["goal"])["max_model_requests"] for key, task in tasks.items()}
        workflow = "full-workflow-test"
        for role, expected in zip(runner.ROLES[:3], [40, 40, 64]):
            self.assertEqual(budgets[f"{workflow}-{role}"], expected)
        for role, expected in zip(runner.ROLES[:3], [20, 20, 32]):
            self.assertEqual(budgets[f"{workflow}-{role}-feedback"], expected)
        self.assertGreater(sum(budgets.values()), 8)
        for task in tasks.values():
            self.assertEqual(json.loads(task["goal"])["flow_variant"], "comprehensive")
        for role in runner.ROLES[:3]:
            self.assertEqual(json.loads(tasks[f"{workflow}-{role}"]["goal"])["max_paragraphs"], 5)

    def test_replay_remains_a_five_task_zero_request_workflow(self):
        tasks = task_map(options(".", mode="replay"))
        self.assertEqual(len(tasks), 5)
        self.assertEqual(len(topological_order(tasks)), 5)
        self.assertFalse(any(key.endswith(("-feedback", "-final")) for key in tasks))
        self.assertEqual(sum(json.loads(task["goal"])["max_model_requests"] for task in tasks.values()), 0)


class ProcessDouble:
    """Only exercise coordinator state handling; no process is started."""

    def __init__(self):
        self.returncode = None

    def poll(self):
        return self.returncode

    def terminate(self):
        self.returncode = 0

    def wait(self, timeout=None):
        self.returncode = 0
        return 0


class FullWorkflowControl:
    def __init__(self, args, early_failed_task=False):
        self.args = args
        self.rows = []
        self.output_requests = []
        self.patch_received = False
        self.early_failed_task = early_failed_task

    def call(self, path, body=None):
        if path == "/readyz":
            return {"status": "ready"}
        if path == "/v1/task-graphs/patches":
            self.rows = [{"task": copy.deepcopy(task), "status": "SUCCEEDED"} for task in body["tasks"]]
            if self.early_failed_task:
                self.rows[0]["status"] = "FAILED"
            self.patch_received = True
            return {"tasks": copy.deepcopy(self.rows)}
        if path == "/v1/tasks":
            return {"tasks": copy.deepcopy(self.rows)}
        if path.startswith("/v1/events?"):
            return {"events": [], "next_after_id": "0", "stream_id": "fixture-events", "has_more": False}
        if path == "/v1/runtime":
            return {"status": "fixture", "model_calls": 0}
        if path.endswith("/output-summary"):
            task_id = path.split("/")[-2]
            self.output_requests.append(task_id)
            return {"task_id": task_id, "artifacts": []}
        if path.endswith("/cancel"):
            return {"status": "CANCELED"}
        raise AssertionError(f"Unexpected fixture API request: {path}")


class FullWorkflowCoordinatorTests(unittest.TestCase):
    def exercise(self, mode="live", early_failed_task=False):
        with tempfile.TemporaryDirectory() as directory:
            args = options(directory, mode)
            args.input_dir.mkdir()
            runner.save_json(args.input_dir / "manifest.json", {"topic": args.topic})
            root = args.run_dir / args.workflow_id
            artifact = root / f"{args.workflow_id}-report" / "report.html"
            artifact.parent.mkdir(parents=True)
            artifact.write_text("<p>Deterministic coordinator fixture; no business or model call.</p>", encoding="utf-8")
            runner.save_json(artifact.parent / "result.json", {"files": {
                "report": {"path": str(artifact), "sha256": runner.digest(artifact)}
            }})
            control = FullWorkflowControl(args, early_failed_task)
            process = ProcessDouble()
            output = io.StringIO()
            with patch.object(runner, "Client", return_value=control), \
                    patch.object(runner.subprocess, "Popen", return_value=process), \
                    patch.object(runner.time, "sleep"), patch.object(sys, "stdout", output):
                if early_failed_task:
                    with self.assertRaisesRegex(RuntimeError, "task failed"):
                        runner.run_workflow(args)
                    summary = json.loads((root / "summary.json").read_text(encoding="utf-8"))
                else:
                    summary = runner.run_workflow(args)
            statuses = json.loads((root / "task-snapshot.json").read_text(encoding="utf-8"))
            self.assertTrue(control.patch_received)
            self.assertIsNotNone(process.returncode)
            self.assertEqual(summary["model_usage"]["provider_attempts"], 0)
            expected_ids = set(task_map(args))
            self.assertEqual({row["task"]["id"] for row in statuses["tasks"]}, expected_ids)
            printed = [json.loads(line) for line in output.getvalue().splitlines() if line]
            self.assertEqual(set(printed[0]["tasks"]), expected_ids)
            if early_failed_task:
                self.assertEqual(summary["status"], "error")
                self.assertEqual(control.output_requests, [])
            else:
                self.assertEqual(summary["status"], "completed")
                self.assertEqual(set(control.output_requests), expected_ids)
                self.assertEqual(set(summary["output_summaries"]),
                                 {task_id[len(args.workflow_id) + 1:] for task_id in expected_ids})
                expected_budget = sum(json.loads(task["goal"])["max_model_requests"]
                                      for task in task_map(args).values())
                self.assertEqual(summary["model_request_budget"], expected_budget)
            return summary

    def test_nine_same_agent_id_tasks_remain_visible_until_all_complete(self):
        self.exercise()

    def test_failure_of_initial_research_cannot_be_hidden_by_succeeded_refinement(self):
        self.exercise(early_failed_task=True)

    def test_replay_coordinator_still_handles_five_tasks_and_no_model_budget(self):
        self.exercise(mode="replay")


class FullResearchFeedbackTests(unittest.TestCase):
    def test_dependency_validation_rejects_the_wrong_stage_of_the_same_role(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            workflow = "stage-check"
            manifests = []
            for role, suffix in (("media", "media"), ("forum", "forum")):
                folder = root / workflow / f"{workflow}-{suffix}"
                folder.mkdir(parents=True)
                report = folder / "report.md"
                report.write_text("validated stage input", encoding="utf-8")
                state = folder / "state.json"
                adapter.atomic_json(state, {"workflow_id": workflow, "task_id": f"{workflow}-{suffix}",
                                           "role": role, "mode": "live", "status": "completed"})
                manifests.append({"schema": adapter.SCHEMA, "workflow_id": workflow, "topic": "测试话题",
                                  "task_id": f"{workflow}-{suffix}", "role": role, "mode": "live",
                                  "files": {"report": adapter.file_record(report), "state": adapter.file_record(state)}})
            request = {"workflow_id": workflow, "task_id": workflow + "-media-feedback", "goal": json.dumps({
                "topic": "测试话题", "research_phase": "refinement",
                "dependency_tasks": {"media": workflow + "-media", "forum": workflow + "-forum"}}),
                "context": "\n".join(adapter.canonical(item) for item in manifests)}
            args = SimpleNamespace(role="media", mode="live", run_dir=str(root))
            payload = adapter.validate_request(request)
            accepted = adapter.dependency_manifests(request, payload, args)
            self.assertEqual(accepted["media"]["task_id"], workflow + "-media")
            payload["dependency_tasks"]["forum"] = workflow + "-forum-final"
            with self.assertRaisesRegex(adapter.AdapterError, "wrong research or discussion stage"):
                adapter.dependency_manifests(request, payload, args)

    def research_fixture(self, directory, append_evidence=True, update_summary=True):
        topic = "测试话题"
        initial = {"query": topic, "is_completed": True, "report_title": "Initial evidence",
                   "paragraphs": [{"title": title, "content": "核验材料",
                                   "research": {"is_completed": True, "latest_summary": "初轮分析" + title,
                                                "search_history": [{"url": "https://example.com/initial", "query": title}]}}
                                  for title in ("争议来源", "独立依据")]}
        observations = {"decisions": [], "summaries": [], "searches": []}

        class State:
            def __init__(self, value):
                self.query = value["query"]
                self.is_completed = value["is_completed"]
                self.report_title = value["report_title"]
                self.final_report = value.get("final_report", "")
                self.paragraphs = []
                for row in value["paragraphs"]:
                    research = SimpleNamespace(**copy.deepcopy(row["research"]))
                    research.mark_completed = lambda research=research: setattr(research, "is_completed", True)
                    self.paragraphs.append(SimpleNamespace(title=row["title"], content=row["content"], research=research))

            @classmethod
            def from_dict(cls, value):
                return cls(value)

            def mark_completed(self):
                self.is_completed = True

            def to_dict(self):
                return {"query": self.query, "is_completed": self.is_completed, "report_title": self.report_title,
                        "final_report": self.final_report, "paragraphs": [
                            {"title": p.title, "content": p.content, "research": {
                                key: copy.deepcopy(value) for key, value in vars(p.research).items() if not callable(value)
                            }} for p in self.paragraphs]}

        def forbidden_initial(*args, **kwargs):
            raise AssertionError("Refinement repeated initial planning or first research instead of using validated state")

        def decide(data, **kwargs):
            observations["decisions"].append(copy.deepcopy(data))
            return {"search_query": data["title"] + "独立核验", "search_tool": "comprehensive_search", "reasoning": "检查论坛疑点"}

        def summarize(data, **kwargs):
            observations["summaries"].append(copy.deepcopy(data))
            return ("根据独立来源补充后的分析" + data["title"]) if update_summary else data["paragraph_latest_state"]

        def search(tool, query):
            observations["searches"].append((tool, query))
            return SimpleNamespace(webpages=[SimpleNamespace(name="Independent source", url="https://example.com/refined",
                                                             snippet="可用于核查疑点的真实格式材料")])

        class Agent:
            def __init__(self, config):
                self.config = config
                self.first_search_node = SimpleNamespace(run=forbidden_initial)
                self.reflection_node = SimpleNamespace(run=decide, process_output=None)
                self.first_summary_node = SimpleNamespace(run=forbidden_initial)
                self.reflection_summary_node = SimpleNamespace(run=summarize)
                self.report_formatting_node = SimpleNamespace(format_report_manually=lambda rows, title: "\n".join(row["paragraph_latest_state"] for row in rows))
                self.execute_search_tool = search
                self._generate_report_structure = forbidden_initial
                self._initial_search_and_summary = forbidden_initial

            def _reflection_loop(self, index):
                paragraph = self.state.paragraphs[index]
                data = {"title": paragraph.title, "content": paragraph.content,
                        "paragraph_latest_state": paragraph.research.latest_summary}
                decision = self.reflection_node.run(data)
                response = self.execute_search_tool(decision["search_tool"], decision["search_query"])
                if append_evidence:
                    paragraph.research.search_history.append({"query": decision["search_query"], "url": response.webpages[0].url})
                paragraph.research.latest_summary = self.reflection_summary_node.run({**data, "search_query": decision["search_query"]})

            def research(self, query, save_report=True):
                self._generate_report_structure(query)
                self._process_paragraphs()
                return self._generate_final_report()

        imports = {
            "MediaEngine": SimpleNamespace(Settings=lambda **kwargs: SimpleNamespace(SEARCH_TOOL_TYPE="AnspireAPI", **kwargs),
                                            DeepSearchAgent=Agent, AnspireSearchAgent=Agent),
            "MediaEngine.nodes.report_structure_node": SimpleNamespace(SYSTEM_PROMPT_REPORT_STRUCTURE="", ReportStructureNode=type("Structure", (), {})),
            "MediaEngine.nodes.summary_node": SimpleNamespace(SYSTEM_PROMPT_FIRST_SUMMARY="", SYSTEM_PROMPT_REFLECTION_SUMMARY="", FORUM_READER_AVAILABLE=True),
            "MediaEngine.nodes.search_node": SimpleNamespace(SYSTEM_PROMPT_FIRST_SEARCH="", SYSTEM_PROMPT_REFLECTION=""),
            "MediaEngine.state.state": SimpleNamespace(State=State),
        }
        args = SimpleNamespace(role="media", source_dir=str(directory), run_dir=str(directory), input_dir=None,
                               forum_feedback="核对最初来源，并检验两项报道的分歧。", initial_research_state=initial,
                               initial_research_sha256="frozen-initial-state-digest", resume_seed=None)
        request = {"workflow_id": "feedback-case", "task_id": "feedback-case-media-feedback", "goal": json.dumps({
            "topic": topic, "max_paragraphs": 5, "max_reflections": 2, "research_phase": "refinement"})}
        return args, request, observations, imports

    def execute_fixture(self, append_evidence=True, update_summary=True):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args, request, observations, imports = self.research_fixture(root, append_evidence, update_summary)
            with patch("socket.socket", side_effect=AssertionError("Offline feedback test attempted network")), \
                    patch.object(adapter, "bound_search_response", side_effect=lambda value, role: value), \
                    patch.object(adapter.importlib, "import_module", side_effect=lambda name: imports[name]):
                state, report = adapter.research_live(args, request, adapter.validate_request(request), root / "feedback-output")
            return state, report, observations, args.forum_feedback

    def test_refinement_passes_validated_discussion_to_search_and_summary_of_each_paragraph(self):
        state, report, observed, feedback = self.execute_fixture()
        self.assertTrue(state["is_completed"])
        self.assertEqual(len(state["paragraphs"]), 2)
        self.assertEqual(len(observed["searches"]), 2)
        self.assertEqual(len(observed["decisions"]), 2)
        self.assertEqual(len(observed["summaries"]), 2)
        for data in observed["decisions"] + observed["summaries"]:
            self.assertEqual(data["forum_feedback"], feedback)
        for data in observed["summaries"]:
            self.assertEqual(data["search_results"][0]["url"], "https://example.com/refined")
        for paragraph in state["research_audit"]["paragraphs"]:
            self.assertTrue(paragraph["feedback_consumed"])
            self.assertEqual(paragraph["reflections_executed"], 1)
            self.assertEqual(paragraph["search_calls"], 1)
            self.assertGreater(paragraph["source_records_after"], paragraph["source_records_before"])
            self.assertNotEqual(paragraph["summary_sha256_before"], paragraph["summary_sha256_after"])
        self.assertIn("补充后的分析", report)

    def test_refinement_cannot_succeed_without_new_search_evidence(self):
        with self.assertRaisesRegex(adapter.AdapterError, "did not perform an evidence search"):
            self.execute_fixture(append_evidence=False)

    def test_refinement_cannot_succeed_with_unchanged_initial_analysis(self):
        with self.assertRaisesRegex(adapter.AdapterError, "did not update"):
            self.execute_fixture(update_summary=False)


class ComprehensiveReportIntegrationTests(unittest.TestCase):
    @staticmethod
    def candidate(model_input):
        sources = {role: next(row["id"] for row in model_input["sources"] if role in row["roles"])
                   for role in ("query", "media")}
        research = {role: next(row["id"] for row in model_input["research"] if row["role"] == role)
                    for role in (*runner.ROLES[:3], "forum")}
        comments = [row["id"] for row in model_input["comments"]]
        sections = []
        for key, title, source_ids, comment_ids, research_ids in [
            ("summary", "摘要", [sources["query"]], [], [research["query"]]),
            ("background", "背景资料与来源", [sources["query"]], [], []),
            ("research", "多角度研究", [], [], [research[role] for role in runner.ROLES[:3]]),
            ("controversies", "争议与证据边界", [sources["media"]], [], []),
            ("comments", "原始评论中的观点", [], comments[:1], []),
            ("discussion", "讨论结论与核查建议", [], [], [research["forum"]]),
            ("limitations", "局限与覆盖范围", [], [], [research["forum"]]),
        ]:
            sections.append({"key": key, "title": title, "paragraphs": [{
                "text": "材料存在不确定性，相关分析应结合来源与研究范围理解。",
                "source_ids": source_ids, "comment_ids": comment_ids, "research_ids": research_ids,
            }]})
        return {"sections": sections}

    def exercise_report(self, invalid_first=False, resumed=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deps = {}
            for role in runner.ROLES[:4]:
                folder = root / role
                folder.mkdir()
                report_path = folder / "report.md"
                report_path.write_text(f"{role}完整研究及讨论材料。", encoding="utf-8")
                state_path = folder / "state.json"
                state = {"research_state": {"paragraphs": [{"title": "研究依据", "research": {
                    "latest_summary": role + "基于检索与原始材料进行的分析。",
                    "search_history": [{"url": "https://example.com/" + role, "title": role + "外部来源",
                                        "content": role + "用于核验争议的来源原始正文。"}],
                }}]}}
                adapter.atomic_json(state_path, state)
                files = {"report": adapter.file_record(report_path), "state": adapter.file_record(state_path)}
                if role == "insight":
                    comments_path = folder / "comments.json"
                    adapter.atomic_json(comments_path, [{"comment_id": "original-a", "content": "原始观点甲。",
                        "source_url": "https://example.com/comment-a", "nickname": "甲", "like_count": 3},
                        {"comment_id": "original-b", "content": "原始观点乙。", "source_url": "https://example.com/comment-b"}])
                    files["comments"] = adapter.file_record(comments_path)
                deps[role] = {"files": files}
            observed = []
            def response_stub(usage, role, system, supplied):
                self.assertEqual(role, "report")
                model_input = json.loads(supplied)
                observed.append(copy.deepcopy(model_input))
                if model_input.get("task") == "paragraph-evidence-review":
                    return json.dumps({"checks": [{"path": row["path"], "supported": True,
                        "reason": "所引材料支持测试段落。",
                        "evidence_ids": (row["source_ids"] + row["comment_ids"] + row["research_ids"])[:1],
                        "sentence_checks": [{"index": sentence["index"], "supported": True,
                            "reason": "所引材料支持测试句。",
                            "evidence_ids": (row["source_ids"] + row["comment_ids"] + row["research_ids"])[:1]}
                            for sentence in row["sentences"]]}
                        for row in model_input["paragraphs"]]}, ensure_ascii=False)
                value = self.candidate(model_input)
                if invalid_first and len(observed) == 1:
                    value["sections"][0]["paragraphs"][0]["source_ids"] = ["invented-source"]
                return json.dumps(value, ensure_ascii=False)
            args = SimpleNamespace(role="report", mode="live", resume_dir=root / "frozen-seed" if resumed else None)
            request = {"workflow_id": "report-integration", "task_id": "report-integration-report"}
            payload = {"topic": "测试综合话题", "max_content_length": 12000, "max_model_requests": 3}
            target = root / "result"
            target.mkdir()
            with patch.object(adapter, "complete_text", side_effect=response_stub), \
                    patch("socket.socket", side_effect=AssertionError("Report test attempted a network call")):
                result = adapter.downstream_role(args, request, payload, deps, target, None)
            audit = adapter.read_json(Path(result["files"]["audit"]["path"]))
            content = adapter.read_json(Path(result["files"]["content"]["path"]))
            page = Path(result["files"]["report"]["path"]).read_text(encoding="utf-8")
            self.assertEqual(len(content["sections"]), 7)
            generated_inputs = [row for row in observed if row.get("task") != "paragraph-evidence-review"]
            self.assertEqual({row["role"] for row in generated_inputs[-1]["research"]}, {"query", "media", "insight", "forum"})
            self.assertEqual({role for row in generated_inputs[-1]["sources"] for role in row["roles"]}, {"query", "media"})
            self.assertEqual({row["content"] for row in generated_inputs[-1]["comments"]}, {"原始观点甲。", "原始观点乙。"})
            self.assertTrue(audit["validation"]["external_sources_consumed"])
            self.assertTrue(audit["validation"]["forum_consumed"])
            self.assertFalse(audit["validation"]["model_fact_check_performed"])
            self.assertTrue(audit["validation"]["model_evidence_review_performed"])
            self.assertEqual(audit["execution_scope"], {"resume_used": resumed, "fresh_from_zero": not resumed})
            self.assertEqual("这不是从零运行的验证。" in page, resumed)
            if resumed:
                self.assertIn("本轮初始研究接续了明确冻结的检查点", page)
            self.assertIn("外部来源", page)
            self.assertIn("原始观点甲。", page)
            self.assertIn("完整研究及讨论材料。", page)
            for record in result["files"].values():
                self.assertEqual(adapter.digest_file(Path(record["path"])), record["sha256"])
            if invalid_first:
                self.assertEqual(len(observed), 3)
                self.assertIn("correction", observed[1])
                self.assertEqual([row["status"] for row in audit["validation_attempts"]], ["rejected", "candidate_validated", "passed"])
            else:
                self.assertEqual(len(observed), 2)
            self.assertEqual(observed[-1]["task"], "paragraph-evidence-review")

    def test_production_report_route_consumes_all_research_sources_forum_and_comments(self):
        self.exercise_report()

    def test_report_validation_retry_corrects_unknown_evidence_without_replacing_originals(self):
        self.exercise_report(invalid_first=True)

    def test_resumed_report_discloses_checkpoint_continuation_in_page_and_audit(self):
        self.exercise_report(resumed=True)

    def test_background_and_controversies_require_their_own_external_citations(self):
        spec = importlib.util.spec_from_file_location("full_report_contract", ROOT / "comprehensive_report.py")
        reporting = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(reporting)
        states = {role: {"paragraphs": [{"title": role, "research": {
            "latest_summary": role + "分析", "search_history": [{"url": "https://example.com/" + role, "title": role, "content": "外部原文"}]
        }}]} for role in runner.ROLES[:3]}
        reports = {role: role + "研究报告" for role in runner.ROLES[:3]}
        comments = {key: {"comment_id": key, "content": "原始观点" + key, "source_url": "https://example.com/" + key}
                    for key in ("C001", "C002")}
        bundle = reporting.build_evidence(states, reports, comments, "主持讨论", 12000)
        for key in ("background", "controversies"):
            with self.subTest(section=key):
                candidate = self.candidate(bundle["model_input"])
                section = next(row for row in candidate["sections"] if row["key"] == key)
                section["paragraphs"][0]["source_ids"] = []
                section["paragraphs"][0]["research_ids"] = [bundle["forum_id"]]
                # Keep both provider roles cited elsewhere; otherwise only a global
                # coverage check would pass for the wrong reason.
                candidate["sections"][0]["paragraphs"][0]["source_ids"] = bundle["included_source_ids"]
                with self.assertRaises(ValueError):
                    reporting.validate_report(candidate, bundle)


if __name__ == "__main__":
    unittest.main()
