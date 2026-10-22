import { useEffect, useRef, useState } from "react";
import {
  Activity,
  ArrowRight,
  Boxes,
  ChartNoAxesCombined,
  Check,
  ChevronRight,
  Clock3,
  Cpu,
  FileText,
  Focus,
  GitBranch,
  House,
  Layers3,
  List,
  LockKeyhole,
  MemoryStick,
  Plus,
  RefreshCw,
  Wifi,
  WifiOff,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Progress } from "@/components/ui/progress";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import MetricChart, { type Sample } from "./MetricChart";

type Task = { role: string; name: string; status: string };
type Workflow = {
  workflow_id: string;
  query: string;
  status: string;
  sample_count: number;
  tasks: Task[];
  model_requests: { attempts: number; limit: number };
  report_url: string | null;
  message?: string;
  readonly?: boolean;
  recovery?: { status: string; model_requests: number; limit: number; reused_tasks: number } | null;
};
type History = Pick<
  Workflow,
  "workflow_id" | "query" | "status" | "sample_count"
> & { task_count?: number };
type Config = {
  ready: boolean;
  readonly?: boolean;
  active_workflow_id: string | null;
  default_workflow_id?: string | null;
  task_count: number;
  max_model_requests: number;
  samples: {
    id: string;
    topic: string;
    label: string;
    comment_count: number;
  }[];
};
type Block = {
  id: string;
  stage: string;
  role: string;
  title: string;
  text: string;
  truncated: boolean;
  status: string;
};
type Failure = { stage: string; name: string; message: string };
type Resources = {
  cpu_milli?: number;
  memory_mib?: number;
  llm_slots?: number;
  token_budget?: number;
};
type TaskMetric = {
  id: string;
  stage: string;
  role: string;
  agent_id?: string;
  status: string;
  dependencies: string[];
  resources: Resources;
  attempt_count: number | null;
  max_attempts: number | null;
  measurement: {
    available: boolean;
    cpu_percent: number | null;
    memory_rss_bytes: number | null;
    process_count: number | null;
  };
  context: {
    available: boolean;
    page_count: number | null;
    logical_tokens: number | null;
    pinned_tokens: number | null;
    shared_page_count: number | null;
    private_page_count: number | null;
    revision: number | null;
    source?: string;
  };
  output: { artifact_count: number | null; content_bytes: number | null };
  timing?: {
    started_at?: string;
    finished_at?: string;
    queue_ms?: number;
    run_ms?: number;
  };
};
type Monitor = {
  workflow_id: string;
  sampled_at: string;
  elapsed_seconds: number | null;
  host: {
    available: boolean;
    cpu_percent: number | null;
    memory_used_bytes: number | null;
    memory_total_bytes: number | null;
  };
  runtime: {
    live: boolean;
    policy?: string;
    capacity?: Resources;
    allocated?: Resources;
  };
  context: {
    available: boolean;
    logical_tokens: number | null;
    page_count: number | null;
    counter_identity?: string;
  };
  tasks: TaskMetric[];
};
type Content = {
  workflow_id: string;
  blocks: Block[];
  task_failures?: Failure[];
  unavailable_stages?: string[];
};
const labels: Record<string, string> = {
  WAITING: "等待依赖",
  WAITING_DEPENDENCY: "等待依赖",
  READY: "等待调度",
  ADMITTED: "已准入",
  DISPATCHING: "启动中",
  QUEUED: "排队中",
  RUNNING: "运行中",
  SUCCEEDED: "已完成",
  FAILED: "失败",
  CANCELED: "已取消",
  CANCELING: "取消中",
  INTERRUPTED: "已中断",
  SKIPPED: "已跳过",
  PENDING: "待提交",
  RETRY_WAIT: "等待重试",
};
const engines: Record<string, string> = {
  query: "Query",
  media: "Media",
  insight: "Insight",
  forum: "Forum",
  report: "Report",
};
const names: Record<string, string> = {
  query: "信息检索",
  media: "媒体分析",
  insight: "评论分析",
  forum: "汇总讨论",
  "query-feedback": "检索补充",
  "media-feedback": "媒体补充",
  "insight-feedback": "评论补充",
  "forum-final": "最终讨论",
  report: "生成报告",
};
const activeStates = new Set(["QUEUED", "RUNNING", "CANCELING"]);
const runningStates = new Set(["RUNNING", "ADMITTED", "DISPATCHING"]);
const waitingStates = new Set([
  "WAITING",
  "WAITING_DEPENDENCY",
  "READY",
  "QUEUED",
  "RETRY_WAIT",
]);
const navItems = [
  { id: "overview", name: "总览", icon: House },
  { id: "tasks", name: "任务", icon: List },
  { id: "resources", name: "资源", icon: ChartNoAxesCombined },
  { id: "context", name: "上下文", icon: Layers3 },
];
const fmt = (n: number | null | undefined) =>
  n == null ? "—" : n.toLocaleString("zh-CN");
const percent = (n: number | null | undefined) =>
  n == null ? "—" : `${n.toFixed(1)}%`;
const memory = (n: number | null | undefined) =>
  n == null
    ? "—"
    : n >= 1024 ** 3
      ? `${(n / 1024 ** 3).toFixed(1)} GB`
      : `${(n / 1024 ** 2).toFixed(1)} MB`;
const duration = (n: number | null | undefined) =>
  n == null
    ? "—"
    : `${Math.floor(n / 3600) ? `${Math.floor(n / 3600)}:` : ""}${String(Math.floor(n / 60) % 60).padStart(2, "0")}:${String(Math.floor(n) % 60).padStart(2, "0")}`;
const stateClass = (s: string) =>
  s === "SUCCEEDED"
    ? "complete"
    : ["FAILED", "INTERRUPTED"].includes(s)
      ? "failed"
      : runningStates.has(s)
        ? "running"
        : ["CANCELED", "SKIPPED", "CANCELING"].includes(s)
          ? "canceled"
          : "waiting";
const effectiveStatus = (task: Task, state?: string) =>
  state && !activeStates.has(state) && runningStates.has(task.status)
    ? "INTERRUPTED"
    : state && !activeStates.has(state) && waitingStates.has(task.status)
      ? "SKIPPED"
      : task.status;
async function api<T>(
  path: string,
  signal?: AbortSignal,
  options: RequestInit = {},
): Promise<T> {
  const response = await fetch(path, {
    ...options,
    signal: signal
      ? AbortSignal.any([signal, AbortSignal.timeout(15000)])
      : AbortSignal.timeout(45000),
    cache: "no-store",
    headers: { "Content-Type": "application/json", ...options.headers },
  });
  const body = await response.json().catch(() => {
    throw Error("后端未返回有效数据");
  });
  if (!response.ok || body.success === false)
    throw Error(body.message || `请求失败（${response.status}）`);
  return body;
}
function Status({ state }: { state: string }) {
  return (
    <span className={`task-status ${stateClass(state)}`}>
      <i />
      {labels[state] || state}
    </span>
  );
}
function Dag({
  tasks,
  metrics,
  selected,
  onSelect,
  large = false,
}: {
  tasks: Task[];
  metrics: TaskMetric[];
  selected: string;
  onSelect: (id: string) => void;
  large?: boolean;
}) {
  const nine = tasks.some((t) => t.role.endsWith("feedback"));
  const positions: Record<string, [number, number]> = nine
    ? {
        query: [90, 30],
        media: [300, 30],
        insight: [510, 30],
        forum: [300, 105],
        "query-feedback": [90, 180],
        "media-feedback": [300, 180],
        "insight-feedback": [510, 180],
        "forum-final": [300, 255],
        report: [300, 330],
      }
    : {
        query: [90, 30],
        media: [300, 30],
        insight: [510, 30],
        forum: [300, 145],
        report: [300, 270],
      };
  const fallback: Record<string, string[]> = nine
    ? {
        forum: ["query", "media", "insight"],
        "query-feedback": ["query", "forum"],
        "media-feedback": ["media", "forum"],
        "insight-feedback": ["insight", "forum"],
        "forum-final": ["query-feedback", "media-feedback", "insight-feedback"],
        report: [
          "query-feedback",
          "media-feedback",
          "insight-feedback",
          "forum-final",
        ],
      }
    : {
        forum: ["query", "media", "insight"],
        report: ["query", "media", "insight", "forum"],
      };
  const edges = tasks
    .flatMap((t) =>
      (
        metrics.find((m) => m.stage === t.role)?.dependencies ??
        fallback[t.role] ??
        []
      ).map((from) => ({ from, to: t.role })),
    )
    .filter((e) => positions[e.from] && positions[e.to]);
  const h = nine ? 365 : 310;
  return (
    <div
      className={`dag ${large ? "large" : ""}`}
      style={{ aspectRatio: `600 / ${h}` }}
      aria-label="实际任务依赖图"
    >
      <svg
        viewBox={`0 0 600 ${h}`}
        preserveAspectRatio="none"
        aria-hidden="true"
      >
        <defs>
          <marker
            id={large ? "dag-arrow-large" : "dag-arrow"}
            markerWidth="6"
            markerHeight="6"
            refX="5"
            refY="3"
            orient="auto"
          >
            <path d="M0 0L6 3L0 6" fill="#96a9b5" />
          </marker>
        </defs>
        {edges.map((e) => {
          const a = positions[e.from],
            b = positions[e.to];
          const skip = b[1] - a[1] > 90;
          const lane = a[0] === 90 ? 15 : a[0] === 510 ? 585 : 405;
          return (
            <path
              key={`${e.from}-${e.to}`}
              data-edge={`${e.from}:${e.to}`}
              d={
                skip
                  ? `M${a[0]} ${a[1] + 22} H${lane} V${b[1] - 22} H${b[0]}`
                  : `M${a[0]} ${a[1] + 22}V${(a[1] + b[1]) / 2}H${b[0]}V${b[1] - 22}`
              }
              className={skip ? "additional-edge" : ""}
              fill="none"
              markerEnd={`url(#${large ? "dag-arrow-large" : "dag-arrow"})`}
            />
          );
        })}
      </svg>
      {tasks.map((t) => {
        const p = positions[t.role];
        return p ? (
          <button
            key={t.role}
            data-node-stage={t.role}
            className={`dag-node ${stateClass(t.status)} ${selected === t.role ? "selected" : ""}`}
            style={{ left: `${p[0] / 6}%`, top: `${(p[1] / h) * 100}%` }}
            onClick={() => onSelect(t.role)}
            aria-label={`${names[t.role] || t.name}，${labels[t.status] || t.status}`}
          >
            <span className="node-icon">
              {t.status === "SUCCEEDED" ? (
                <Check />
              ) : runningStates.has(t.status) ? (
                <span className="node-spinner" />
              ) : t.status === "FAILED" ? (
                <span>!</span>
              ) : (
                <Clock3 />
              )}
            </span>
            <span className="node-copy">
              <strong>{names[t.role] || t.name}</strong>
              {large && <small>{labels[t.status] || t.status}</small>}
            </span>
          </button>
        ) : null;
      })}
    </div>
  );
}
export default function App() {
  const [view, setView] = useState("overview"),
    [filter, setFilter] = useState("all"),
    [config, setConfig] = useState<Config | null>(null),
    [history, setHistory] = useState<History[]>([]),
    [workflow, setWorkflow] = useState("");
  const [snapshot, setSnapshot] = useState<Workflow | null>(null),
    [content, setContent] = useState<Content | null>(null),
    [monitor, setMonitor] = useState<Monitor | null>(null),
    [connected, setConnected] = useState(false),
    [notice, setNotice] = useState(""),
    [metricError, setMetricError] = useState("");
  const [selected, setSelected] = useState(""),
    [follow, setFollow] = useState(true),
    [refreshKey, setRefreshKey] = useState(0),
    [samples, setSamples] = useState<Sample[]>([]);
  const [dialog, setDialog] = useState<"new" | "cancel" | "report" | null>(
      null,
    ),
    [topic, setTopic] = useState(""),
    [sampleID, setSampleID] = useState(""),
    [upload, setUpload] = useState<Record<string, unknown> | null>(null),
    [formError, setFormError] = useState(""),
    [busy, setBusy] = useState(false);
  const selectedRef = useRef(""),
    sessionRef = useRef(0),
    loadedWorkflow = useRef("");
  useEffect(() => {
    const ctrl = new AbortController();
    let alive = true;
    let retry: ReturnType<typeof setTimeout>;
    const load = () => Promise.all([
      api<Config>("/api/aegisrt/status", ctrl.signal),
      api<{ workflows: History[] }>("/api/aegisrt/workflows", ctrl.signal),
    ])
      .then(([c, h]) => {
        if (!alive) return;
        setConfig(c);
        setHistory(h.workflows);
        setConnected(true);
        setWorkflow((previous) => {
          if (c.active_workflow_id) return c.active_workflow_id;
          if (previous && h.workflows.some((w) => w.workflow_id === previous))
            return previous;
          const requested = new URLSearchParams(window.location.search).get("workflow");
          if (requested && h.workflows.some((w) => w.workflow_id === requested))
            return requested;
          if (c.default_workflow_id && h.workflows.some((w) => w.workflow_id === c.default_workflow_id))
            return c.default_workflow_id;
          let saved = "";
          try {
            saved = localStorage.getItem("bettafish-workflow") || "";
          } catch {
            /* Storage may be unavailable. */
          }
          return h.workflows.some((w) => w.workflow_id === saved)
            ? saved
            : h.workflows[0]?.workflow_id || "";
        });
        if (!h.workflows.length)
          setNotice("暂无运行记录，接入实例后即可观察任务执行。");
      })
      .catch((e) => {
        if (alive && !ctrl.signal.aborted) {
          setConnected(false);
          setConfig(null);
          setNotice(e.message);
          retry = setTimeout(load, 5000);
        }
      });
    void load();
    return () => {
      alive = false;
      ctrl.abort();
      clearTimeout(retry);
    };
  }, [refreshKey]);
  useEffect(() => {
    const generation = ++sessionRef.current,
      ctrl = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    if (loadedWorkflow.current !== workflow) {
      setSnapshot(null);
      setContent(null);
      setMonitor(null);
      setSelected("");
      setMetricError("");
      setSamples([]);
      setDialog(null);
      loadedWorkflow.current = workflow;
      selectedRef.current = "";
      setNotice("");
    }
    if (!workflow) return;
    try {
      localStorage.setItem("bettafish-workflow", workflow);
    } catch {
      /* Storage may be unavailable. */
    }
    async function poll() {
      let next = 5000;
      try {
        const [s, c, m] = await Promise.all([
          api<Workflow>(
            `/api/aegisrt/workflows/${encodeURIComponent(workflow)}`,
            ctrl.signal,
          ),
          api<Content>(
            `/api/aegisrt/workflows/${encodeURIComponent(workflow)}/content`,
            ctrl.signal,
          ),
          api<Monitor>(
            `/api/aegisrt/workflows/${encodeURIComponent(workflow)}/monitor`,
            ctrl.signal,
          ).catch((e) => {
            if (!ctrl.signal.aborted && generation === sessionRef.current)
              setMetricError(e.message);
            return null;
          }),
        ]);
        if (ctrl.signal.aborted || generation !== sessionRef.current) return;
        if (
          s.workflow_id !== workflow ||
          c.workflow_id !== workflow ||
          (m && m.workflow_id !== workflow)
        )
          throw Error("工作流身份不一致，已停止更新");
        setSnapshot(s);
        setContent(c);
        setHistory((previous) => previous.map((row) =>
          row.workflow_id === workflow ? { ...row, status: s.status } : row,
        ));
        setConnected(true);
        setNotice(
          c.task_failures?.map((f) => `${f.name}：${f.message}`).join(" ") ||
            s.message ||
            "",
        );
        if (m) {
          setMonitor(m);
          setMetricError("");
          const point = {
            time: Date.parse(m.sampled_at),
            cpu: m.host.cpu_percent,
            memory: m.host.memory_used_bytes,
            tasks: Object.fromEntries(
              m.tasks.map((t) => [
                t.stage,
                {
                  cpu: t.measurement.cpu_percent,
                  memory: t.measurement.memory_rss_bytes,
                },
              ]),
            ),
          };
          if (Number.isFinite(point.time))
            setSamples((previous) => [
              ...previous.filter((p) => p.time >= point.time - 60000),
              point,
            ]);
        }
        if (!activeStates.has(s.status))
          setConfig((previous) =>
            previous?.active_workflow_id === workflow
              ? { ...previous, active_workflow_id: null }
              : previous,
          );
        next = activeStates.has(s.status) ? 2500 : 8000;
      } catch (e) {
        if (!ctrl.signal.aborted) {
          setConnected(false);
          setNotice(e instanceof Error ? e.message : "连接中断，保留上次内容");
        }
      } finally {
        if (!ctrl.signal.aborted && generation === sessionRef.current)
          timer = setTimeout(poll, next);
      }
    }
    void poll();
    return () => {
      ctrl.abort();
      clearTimeout(timer);
    };
  }, [workflow, refreshKey]);
  useEffect(() => {
    if (!snapshot) return;
    if (follow || !snapshot.tasks.some((t) => t.role === selectedRef.current)) {
      const task =
        (activeStates.has(snapshot.status)
          ? snapshot.tasks.find((t) => t.status === "RUNNING")
          : null) ||
        snapshot.tasks.find((t) => t.status === "FAILED") ||
        [...snapshot.tasks].reverse().find((t) => t.status === "SUCCEEDED") ||
        snapshot.tasks[0];
      if (task) {
        setSelected(task.role);
        selectedRef.current = task.role;
      }
    }
  }, [snapshot, follow]);
  const choose = (id: string) => {
    selectedRef.current = id;
    setSelected(id);
    setFollow(false);
  };
  const tasks = snapshot?.tasks || [],
    metrics = monitor?.tasks || [],
    current = tasks.find((t) => t.role === selected),
    currentMetric = metrics.find((t) => t.stage === selected);
  const done = tasks.filter((t) => t.status === "SUCCEEDED").length,
    running = tasks.filter((t) =>
      runningStates.has(effectiveStatus(t, snapshot?.status)),
    ).length,
    waiting = tasks.filter((t) =>
      waitingStates.has(effectiveStatus(t, snapshot?.status)),
    ).length;
  const displayed = tasks.filter(
    (t) =>
      filter === "all" ||
      (filter === "running" &&
        runningStates.has(effectiveStatus(t, snapshot?.status))) ||
      (filter === "waiting" &&
        waitingStates.has(effectiveStatus(t, snapshot?.status))),
  );
  const blocks = content?.blocks.filter((b) => b.stage === selected) || [],
    currentFailure = content?.task_failures?.find((f) => f.stage === selected);
  const expectedReport = workflow
    ? `/api/aegisrt/workflows/${encodeURIComponent(workflow)}/report`
    : null;
  const reportURL =
    snapshot?.status === "SUCCEEDED" && snapshot.report_url === expectedReport
      ? expectedReport
      : null;
  const title = navItems.find((n) => n.id === view)?.name || "任务进程";
  const host = connected && !metricError ? monitor?.host : undefined;
  const active = !!snapshot && activeStates.has(snapshot.status),
    readonly =
      config?.readonly ||
      (config?.ready === false && config.samples.length === 0);
  const submit = async () => {
    if (busy || !config?.ready || config.active_workflow_id || readonly) return;
    setBusy(true);
    setFormError("");
    try {
      if (!topic.trim() || (!upload && !sampleID))
        throw Error("请输入话题并选择或上传样本");
      const result = await api<{ workflow_id: string }>(
        "/api/search",
        undefined,
        {
          method: "POST",
          body: JSON.stringify({
            query: topic.trim(),
            ...(upload ? { sample: upload } : { sample_id: sampleID }),
          }),
        },
      );
      setConfig({ ...config, active_workflow_id: result.workflow_id });
      setFollow(true);
      setWorkflow(result.workflow_id);
      setDialog(null);
      setRefreshKey((k) => k + 1);
    } catch (e) {
      setFormError(
        `${e instanceof Error ? e.message : "提交失败"}。如提交结果不明，请先刷新状态，避免重复提交。`,
      );
    } finally {
      setBusy(false);
    }
  };
  const cancel = async () => {
    if (busy || !workflow || !active || readonly || snapshot?.readonly) return;
    setBusy(true);
    try {
      await api(`${expectedReport?.replace("/report", "")}/cancel`, undefined, {
        method: "POST",
        body: "{}",
      });
      setDialog(null);
      setRefreshKey((k) => k + 1);
    } catch (e) {
      setNotice(e instanceof Error ? e.message : "取消失败");
    } finally {
      setBusy(false);
    }
  };
  const outputPanel = (
    <Card className="output-panel">
      <div className="panel-head">
        <h2>
          生成内容
          <span>
            {" "}
            · {current ? names[current.role] || current.name : "待开始"}
          </span>
        </h2>
        <Button
          variant="ghost"
          size="sm"
          className={follow ? "follow-enabled" : ""}
          aria-pressed={follow}
          onClick={() => setFollow((v) => !v)}
        >
          <Focus size={16} />
          跟随任务
        </Button>
      </div>
      <div className="output-meta">
        <span>
          {current
            ? `${engines[current.role.split("-")[0]] || current.role}Engine`
            : "Agent输出"}
        </span>
        <span>
          {!connected && snapshot
            ? "连接中断，保留内容"
            : current
              ? labels[effectiveStatus(current, snapshot?.status)] ||
                current.status
              : "等待任务"}
        </span>
      </div>
      <div className="output-scroll" tabIndex={0} aria-label="当前任务生成内容">
        {currentFailure && (
          <p className="failure-message">{currentFailure.message}</p>
        )}
        {blocks.map((b) => (
          <article key={b.id} className="output-block">
            <h3>{b.title}</h3>
            <p>{b.text}</p>
            {b.truncated && <small>内容较长，此处显示前一部分。</small>}
          </article>
        ))}
        {!blocks.length && (
          <div className="empty-output">
            <FileText size={30} />
            <h3>
              {selected === "report"
                ? reportURL
                  ? "报告已生成"
                  : active
                    ? "等待报告生成"
                    : "本轮报告未完成"
                : current
                  ? "暂无已保存内容"
                  : "选择任务查看输出"}
            </h3>
            <p>
              {selected === "report"
                ? reportURL
                  ? "报告已通过完整性检查，可打开阅读。"
                  : "报告通过完整性检查后开放。"
                : active
                  ? "内容保存后将自动更新。"
                  : "任务执行内容按实际保存的段落展示。"}
            </p>
          </div>
        )}
      </div>
      <div className="output-footer">
        <span>
          {blocks.length}个段落 ·{" "}
          {fmt(blocks.reduce((n, b) => n + b.text.length, 0))}字
        </span>
        <span>
          <i className={active && connected ? "live-dot" : ""} />
          {active && connected ? "按保存内容更新" : "已保存内容"}
        </span>
      </div>
      <div className="task-completion">
        <span>工作流完成度</span>
        <strong>
          {done} / {tasks.length || 9}
        </strong>
        <Progress value={tasks.length ? (done / tasks.length) * 100 : 0} />
      </div>
    </Card>
  );
  return (
    <div className="console-shell">
      <main className="main-console">
        <header className="header">
          <a className="brand" href="/" aria-label="AegisRT首页">
            AegisRT
          </a>
          <h1>运行监控台</h1>
          <nav aria-label="运行监控导航">
            {navItems.map((n) => (
              <button
                key={n.id}
                onClick={() => setView(n.id)}
                className={view === n.id ? "active" : ""}
                aria-current={view === n.id ? "page" : undefined}
              >
                {n.name}
              </button>
            ))}
          </nav>
          <Badge
            className={`workflow-badge ${stateClass(snapshot?.status || "PENDING")}`}
            variant="outline"
          >
            <i />
            {labels[snapshot?.status || "PENDING"]}
          </Badge>
          <div className="header-actions">
            <div className="instance">
              <Boxes size={17} />
              接入实例：微舆
              <ChevronRight size={15} />
            </div>
            <span
              className={`connection ${connected ? "online" : "offline"}`}
              role="status"
            >
              {connected ? <Wifi size={15} /> : <WifiOff size={15} />}
              <span>{connected ? "后端已连接" : "连接中"}</span>
            </span>
            <Button
              variant="outline"
              size="icon"
              aria-label="刷新状态"
              onClick={() => setRefreshKey((k) => k + 1)}
            >
              <RefreshCw size={17} />
            </Button>
          </div>
        </header>
        <div
          className={`content-wrap ${view === "overview" ? "overview" : ""}`}
        >
          <section className="metrics-strip" aria-label="运行指标">
            <Card className="metric completion-metric">
              <span className="metric-icon green">
                <Check />
              </span>
              <div className="metric-body">
                <div className="metric-label">任务完成{snapshot?.recovery && <span>报告恢复</span>}</div>
                <strong>
                  {done} / {tasks.length || config?.task_count || 9}
                </strong>
                <small>
                  运行中 {running} · 等待 {waiting}
                </small>
              </div>
            </Card>
            <Card className="metric">
              <span className="metric-icon">
                <Cpu />
              </span>
              <div className="metric-body">
                <div className="metric-label">
                  CPU使用率<span>执行节点</span>
                </div>
                <strong data-testid="host-cpu">
                  {percent(host?.cpu_percent)}
                </strong>
                <MetricChart
                  compact
                  values={samples.map((p) => ({ time: p.time, value: p.cpu }))}
                />
              </div>
            </Card>
            <Card className="metric">
              <span className="metric-icon">
                <MemoryStick />
              </span>
              <div className="metric-body">
                <div className="metric-label">
                  内存用量<span>执行节点</span>
                </div>
                <strong className="memory-reading" data-testid="host-memory">
                  {memory(host?.memory_used_bytes)}
                  <em> / {memory(host?.memory_total_bytes)}</em>
                </strong>
                <MetricChart
                  compact
                  unit="bytes"
                  max={host?.memory_total_bytes || 1024 ** 3}
                  values={samples.map((p) => ({
                    time: p.time,
                    value: p.memory,
                  }))}
                />
              </div>
            </Card>
            <Card className="metric context-metric">
              <span className="metric-icon">
                <FileText />
              </span>
              <div className="metric-body">
                <div className="metric-label">上下文计数</div>
                <strong data-testid="context-total">
                  {fmt(monitor?.context.logical_tokens)}
                </strong>
                <small>
                  {monitor?.context.available
                    ? `${fmt(monitor.context.page_count)}页 · 逻辑Token`
                    : "上下文统计未采集"}
                </small>
              </div>
            </Card>
          </section>
          <div className="sampling-status" role="status">
            {metricError || !connected
              ? "采样暂不可用，曲线与上下文保留上次数据；当前资源用量暂停显示。"
              : monitor?.sampled_at
                ? `最近采样 ${new Date(monitor.sampled_at).toLocaleTimeString("zh-CN")} · CPU / 内存为执行节点实测 · 上下文为逻辑Token`
                : "等待运行监控采样"}
          </div>
          <div className="workflow-bar">
            <div className="workflow-caption">
              <span className="section-kicker">当前工作流</span>
              <h2>{snapshot?.query || "等待选择运行记录"}</h2>
              <span>
                {snapshot
                  ? `${snapshot.sample_count}条评论 · ${tasks.length}项任务`
                  : "统一接入 · 依赖组织 · 资源调度"}
                <span className="workflow-id">
                  {workflow && ` · ${workflow}`}
                </span>
              </span>
            </div>
            <div className="workflow-actions">
              <select
                id="history"
                aria-label="选择运行记录"
                value={workflow}
                onChange={(e) => {
                  setFollow(true);
                  setWorkflow(e.target.value);
                }}
              >
                <option value="">选择运行记录</option>
                {history.map((w) => (
                  <option key={w.workflow_id} value={w.workflow_id}>
                    {w.query}{w.task_count ? ` · ${w.task_count}任务` : ""} · {labels[w.status] || w.status} ·{" "}
                    {w.workflow_id.slice(-8)}
                  </option>
                ))}
              </select>
              {!readonly && (
                <Button
                  id="new-research"
                  variant="outline"
                  disabled={
                    !connected ||
                    !config?.ready ||
                    !!config.active_workflow_id ||
                    busy
                  }
                  onClick={() => {
                    setFormError("");
                    setDialog("new");
                  }}
                >
                  <Plus size={17} />
                  新建任务
                </Button>
              )}
              {active && !readonly && !snapshot?.readonly && (
                <Button
                  id="cancel"
                  variant="outline"
                  disabled={
                    !connected || busy || snapshot?.status === "CANCELING"
                  }
                  onClick={() => setDialog("cancel")}
                >
                  停止工作流
                </Button>
              )}
              <Button
                id="open-report"
                disabled={!connected || !reportURL}
                onClick={() => setDialog("report")}
              >
                <FileText size={17} />
                {reportURL ? "打开报告" : "报告未就绪"}
              </Button>
            </div>
          </div>
          {(notice || (!connected && snapshot)) && (
            <div
              className={`notice ${snapshot?.status === "FAILED" || !connected ? "error" : ""}`}
              role="status"
            >
              {!connected && snapshot
                ? "连接中断，保留上次任务状态和研究内容。"
                : notice}
            </div>
          )}
          {view === "overview" ? (
            <section className="overview-grid">
              <Card className="dependency-panel">
                <div className="panel-head">
                  <h2>任务依赖</h2>
                  <div className="graph-legend">
                    <Status state="SUCCEEDED" />
                    <Status state="RUNNING" />
                    <Status state="WAITING_DEPENDENCY" />
                  </div>
                </div>
                <Dag
                  large
                  tasks={tasks.map((t) => ({
                    ...t,
                    status: effectiveStatus(t, snapshot?.status),
                  }))}
                  metrics={metrics}
                  selected={selected}
                  onSelect={choose}
                />
                <div className="dag-foot">
                  <span>
                    <GitBranch size={14} />
                    {tasks.length}项任务 ·{" "}
                    {metrics.reduce((n, t) => n + t.dependencies.length, 0) ||
                      (tasks.length === 9 ? 16 : tasks.length === 5 ? 7 : 0)}
                    条依赖
                  </span>
                  <span>点击任务查看详情</span>
                </div>
              </Card>
              <div className="overview-right">
                <Card className="task-monitor">
                  <div className="panel-head">
                    <h2>任务运行监控</h2>
                    <select
                      aria-label="选择监控任务"
                      value={selected}
                      onChange={(e) => choose(e.target.value)}
                    >
                      {tasks.map((t) => (
                        <option key={t.role} value={t.role}>
                          {names[t.role] || t.name} ·{" "}
                          {engines[t.role.split("-")[0]] || t.role}Agent
                        </option>
                      ))}
                    </select>
                  </div>
                  <div className="task-charts">
                    <div className="task-chart">
                      <div className="task-chart-head">
                        <span>
                          <span className="mini-icon">
                            <Cpu size={20} />
                          </span>
                          CPU使用率
                        </span>
                        <strong data-testid="task-cpu">
                          {percent(
                            connected && !metricError
                              ? currentMetric?.measurement.cpu_percent
                              : null,
                          )}
                        </strong>
                      </div>
                      <MetricChart
                        max={Math.max(
                          100,
                          ...samples.map((p) => p.tasks[selected]?.cpu || 0),
                        )}
                        values={samples.map((p) => ({
                          time: p.time,
                          value: p.tasks[selected]?.cpu ?? null,
                        }))}
                      />
                    </div>
                    <div className="task-chart">
                      <div className="task-chart-head">
                        <span>
                          <span className="mini-icon">
                            <MemoryStick size={20} />
                          </span>
                          内存用量
                        </span>
                        <strong data-testid="task-memory">
                          {memory(
                            connected && !metricError
                              ? currentMetric?.measurement.memory_rss_bytes
                              : null,
                          )}
                        </strong>
                      </div>
                      <MetricChart
                        unit="bytes"
                        max={Math.max(
                          (currentMetric?.resources.memory_mib || 1024) *
                            1024 ** 2,
                          ...samples.map((p) => p.tasks[selected]?.memory || 0),
                        )}
                        values={samples.map((p) => ({
                          time: p.time,
                          value: p.tasks[selected]?.memory ?? null,
                        }))}
                      />
                    </div>
                  </div>
                  <div className="context-info">
                    <div className="context-info-title">
                      上下文信息
                      <span>
                        {currentMetric?.context.source === "attempt_dispatch"
                          ? "执行时上下文"
                          : "ContextVM"}
                      </span>
                    </div>
                    <div
                      className="context-info-stats"
                      data-testid="selected-context"
                    >
                      <div>
                        <span>
                          <FileText size={17} />
                          上下文
                        </span>
                        <strong>
                          {fmt(currentMetric?.context.logical_tokens)}
                        </strong>
                      </div>
                      <div>
                        <span>
                          <Layers3 size={17} />
                          页面
                        </span>
                        <strong>
                          {fmt(currentMetric?.context.page_count)}
                        </strong>
                      </div>
                      <div>
                        <span>
                          <GitBranch size={17} />
                          上游依赖
                        </span>
                        <strong>
                          {fmt(currentMetric?.dependencies.length)}
                        </strong>
                      </div>
                      <div className="context-budget">
                        <span>
                          <LockKeyhole size={16} />
                          上下文预算
                        </span>
                        <Progress
                          value={
                            currentMetric?.resources.token_budget &&
                            currentMetric.context.logical_tokens != null
                              ? Math.min(
                                  100,
                                  (currentMetric.context.logical_tokens /
                                    currentMetric.resources.token_budget) *
                                    100,
                                )
                              : 0
                          }
                        />
                        <small>
                          {fmt(currentMetric?.context.logical_tokens)} /{" "}
                          {fmt(currentMetric?.resources.token_budget)}
                        </small>
                      </div>
                    </div>
                  </div>
                  <div className="monitor-note">
                    {metricError
                      ? "监控接口暂不可用"
                      : !connected
                        ? "连接中断，保留上次采样"
                        : currentMetric?.measurement.available
                          ? "任务CPU以单核为100%，内存为进程RSS。"
                          : current?.status === "RUNNING"
                            ? "当前任务正在运行，尚未取得进程采样；上方为执行节点实际采样。"
                            : "任务未运行，无当前进程用量；上方为执行节点实际采样。"}
                  </div>
                </Card>
                {outputPanel}
              </div>
            </section>
          ) : (
            <Card className="task-panel">
              <div className="panel-head">
                <h2>{title}</h2>
                {view === "records" ? (
                  <Badge variant="outline">本轮执行记录</Badge>
                ) : (
                  <Tabs value={filter} onValueChange={setFilter}>
                    <TabsList>
                      <TabsTrigger value="all">
                        全部任务（{tasks.length}）
                      </TabsTrigger>
                      <TabsTrigger value="running">
                        运行中（{running}）
                      </TabsTrigger>
                      <TabsTrigger value="waiting">
                        等待中（{waiting}）
                      </TabsTrigger>
                    </TabsList>
                  </Tabs>
                )}
                <span className="panel-note">
                  {view === "resources"
                    ? "资源配额与实际采样"
                    : view === "context"
                      ? "ContextVM · 按依赖共享"
                      : "点击任务查看上下文与输出"}
                </span>
              </div>
              <Table className="task-table">
                <TableHeader>
                  <TableRow>
                    <TableHead className="number-col">#</TableHead>
                    <TableHead>任务 / Agent</TableHead>
                    <TableHead>状态</TableHead>
                    {view === "context" ? (
                      <>
                        <TableHead>上下文页</TableHead>
                        <TableHead>逻辑Token</TableHead>
                        <TableHead>共享页 / 私有页</TableHead>
                        <TableHead>版本</TableHead>
                      </>
                    ) : view === "records" ? (
                      <>
                        <TableHead>执行次数</TableHead>
                        <TableHead>运行耗时</TableHead>
                        <TableHead>产物数</TableHead>
                        <TableHead>模型请求</TableHead>
                      </>
                    ) : (
                      <>
                        <TableHead>
                          CPU{view === "resources" ? "配额" : "用量"}
                        </TableHead>
                        <TableHead>
                          内存{view === "resources" ? "配额" : "用量"}
                        </TableHead>
                        <TableHead>上下文Token</TableHead>
                        <TableHead>执行次数</TableHead>
                      </>
                    )}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {displayed.map((t) => {
                    const m = metrics.find((m) => m.stage === t.role),
                      st = effectiveStatus(t, snapshot?.status);
                    return (
                      <TableRow
                        key={t.role}
                        data-stage={t.role}
                        data-state={
                          selected === t.role ? "selected" : undefined
                        }
                        className={`${selected === t.role ? "selected-task" : ""} ${stateClass(st)}`}
                      >
                        <TableCell className="number-col">
                          {tasks.indexOf(t) + 1}
                        </TableCell>
                        <TableCell>
                          <button
                            className="task-name"
                            onClick={() => choose(t.role)}
                          >
                            <span>{names[t.role] || t.name}</span>
                            <span className="agent-name">
                              / {engines[t.role.split("-")[0]] || t.role}
                            </span>
                          </button>
                        </TableCell>
                        <TableCell>
                          <Status state={st} />
                        </TableCell>
                        {view === "context" ? (
                          <>
                            <TableCell>{fmt(m?.context.page_count)}</TableCell>
                            <TableCell>
                              {fmt(m?.context.logical_tokens)}
                            </TableCell>
                            <TableCell>
                              {fmt(m?.context.shared_page_count)} /{" "}
                              {fmt(m?.context.private_page_count)}
                            </TableCell>
                            <TableCell>{fmt(m?.context.revision)}</TableCell>
                          </>
                        ) : view === "records" ? (
                          <>
                            <TableCell>
                              {fmt(m?.attempt_count)}
                              {m?.max_attempts != null && (
                                <small> / {m.max_attempts}</small>
                              )}
                            </TableCell>
                            <TableCell>
                              {duration(
                                m?.timing?.run_ms != null
                                  ? m.timing.run_ms / 1000
                                  : null,
                              )}
                            </TableCell>
                            <TableCell>
                              {fmt(m?.output.artifact_count)}
                            </TableCell>
                            <TableCell>
                              {t.role === selected
                                ? `${snapshot?.model_requests.attempts ?? "—"}次 / 本轮`
                                : "—"}
                            </TableCell>
                          </>
                        ) : (
                          <>
                            <TableCell
                              className={
                                m?.measurement.available ? "heat-cell" : ""
                              }
                            >
                              {view === "resources"
                                ? m?.resources.cpu_milli != null
                                  ? `${m.resources.cpu_milli} mCPU`
                                  : "—"
                                : percent(
                                    connected
                                      ? m?.measurement.cpu_percent
                                      : null,
                                  )}
                            </TableCell>
                            <TableCell
                              className={
                                m?.measurement.available ? "heat-cell" : ""
                              }
                            >
                              {view === "resources"
                                ? m?.resources.memory_mib != null
                                  ? `${m.resources.memory_mib} MiB`
                                  : "—"
                                : memory(
                                    connected
                                      ? m?.measurement.memory_rss_bytes
                                      : null,
                                  )}
                            </TableCell>
                            <TableCell>
                              {fmt(m?.context.logical_tokens)}
                            </TableCell>
                            <TableCell>{fmt(m?.attempt_count)}</TableCell>
                          </>
                        )}
                      </TableRow>
                    );
                  })}
                  {!displayed.length && (
                    <TableRow>
                      <TableCell colSpan={7}>
                        <div className="empty-table">
                          {tasks.length
                            ? "当前筛选下没有任务"
                            : "等待后端任务记录"}
                        </div>
                      </TableCell>
                    </TableRow>
                  )}
                </TableBody>
              </Table>
              <div className="table-foot">
                <span>
                  {view === "resources"
                    ? "配额为调度资源声明，实际用量见任务进程。"
                    : "任务CPU以单核为100%，内存为进程RSS；已结束任务不显示当前用量。"}
                </span>
                <span>
                  {metricError
                    ? "监控接口暂不可用"
                    : monitor?.sampled_at
                      ? `最近采样 ${new Date(monitor.sampled_at).toLocaleTimeString("zh-CN")}`
                      : "等待监控数据"}
                </span>
              </div>
            </Card>
          )}
          {view !== "overview" && (
            <section className="detail-grid">
              <Card className="context-panel">
                <div className="panel-head">
                  <h2>上下文流转</h2>
                  <Badge variant="outline">
                    <LockKeyhole size={14} />
                    按依赖共享
                  </Badge>
                </div>
                <div className="context-flow">
                  <div className="context-stat">
                    <FileText />
                    <div>
                      <span>上游依赖</span>
                      <strong>{fmt(currentMetric?.dependencies.length)}</strong>
                      <small>项任务</small>
                    </div>
                  </div>
                  <ArrowRight className="flow-arrow" />
                  <div className="context-stat">
                    <Layers3 />
                    <div>
                      <span>当前上下文</span>
                      <strong>
                        {fmt(currentMetric?.context.page_count)}
                        <em>页</em>
                      </strong>
                      <small>
                        {fmt(currentMetric?.context.logical_tokens)}逻辑Token
                      </small>
                    </div>
                  </div>
                  <ArrowRight className="flow-arrow" />
                  <div className="context-stat">
                    <LockKeyhole />
                    <div>
                      <span>共享 / 私有</span>
                      <strong>
                        {fmt(currentMetric?.context.shared_page_count)}
                        <em>
                          / {fmt(currentMetric?.context.private_page_count)}
                        </em>
                      </strong>
                      <small>页 · 受控访问</small>
                    </div>
                  </div>
                </div>
                <div className="dependency-caption">
                  <span>任务依赖关系</span>
                  <span>
                    当前：
                    {current ? names[current.role] || current.name : "未选择"}
                  </span>
                </div>
                <Dag
                  tasks={tasks}
                  metrics={metrics}
                  selected={selected}
                  onSelect={choose}
                />
                <div className="context-foot">
                  <span>ContextVM</span>
                  <span>
                    {currentMetric?.context.available
                      ? `版本${fmt(currentMetric.context.revision)} · 逻辑计数`
                      : "等待上下文记录"}
                  </span>
                </div>
              </Card>
              {outputPanel}
            </section>
          )}
          <footer className="console-footer">
            <span>
              <Activity size={15} />
              AegisRT运行状态与任务记录
            </span>
            <span>
              运行时间 {duration(monitor?.elapsed_seconds)} ·{" "}
              模型请求 {snapshot?.model_requests.attempts ?? "—"} /{" "}
              {snapshot?.model_requests.limit ??
                config?.max_model_requests ??
                "—"}
              {readonly ? " · 只读观察" : ""}
            </span>
          </footer>
        </div>
      </main>
      <Dialog
        open={dialog !== null}
        onOpenChange={(open) => {
          if (!open && !busy) setDialog(null);
        }}
      >
        <DialogContent className={dialog === "report" ? "report-dialog" : ""}>
          <DialogHeader>
            <DialogTitle>
              {dialog === "new"
                ? "提交微舆工作流"
                : dialog === "cancel"
                  ? "停止当前工作流"
                  : "本轮研究报告"}
            </DialogTitle>
            <DialogDescription>
              {dialog === "new"
                ? "选择话题与评论样本，交由AegisRT组织和执行任务。"
                : dialog === "cancel"
                  ? "停止执行后，已保存的研究内容仍会保留。"
                  : "报告已通过后端完整性检查。"}
            </DialogDescription>
          </DialogHeader>
          {dialog === "new" && (
            <form
              className="new-form"
              onSubmit={(e) => {
                e.preventDefault();
                void submit();
              }}
            >
              <Label htmlFor="topic-input">分析话题</Label>
              <Input
                id="topic-input"
                value={topic}
                onChange={(e) => setTopic(e.target.value)}
                required
                maxLength={300}
              />
              <Label htmlFor="sample-input">评论样本</Label>
              <select
                id="sample-input"
                value={sampleID}
                onChange={(e) => {
                  setSampleID(e.target.value);
                  setUpload(null);
                  setTopic(
                    config?.samples.find((s) => s.id === e.target.value)
                      ?.topic || "",
                  );
                }}
              >
                <option value="">选择样本</option>
                {config?.samples.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.label} · {s.comment_count}条评论
                  </option>
                ))}
              </select>
              <Label htmlFor="sample-file">或上传评论样本JSON</Label>
              <Input
                id="sample-file"
                type="file"
                accept=".json,application/json"
                onChange={async (e) => {
                  const file = e.target.files?.[0];
                  setUpload(null);
                  if (!file) return;
                  try {
                    if (file.size > 1000000) throw Error("样本文件不能超过1MB");
                    const v = JSON.parse(await file.text());
                    if (
                      !v ||
                      typeof v.topic !== "string" ||
                      !Array.isArray(v.comments) ||
                      !v.provenance
                    )
                      throw Error("样本需包含topic、comments和provenance");
                    setUpload(v);
                    setSampleID("");
                    setTopic(v.topic);
                    setFormError("");
                  } catch (e) {
                    setFormError(
                      e instanceof Error ? e.message : "样本读取失败",
                    );
                  }
                }}
              />
              <p className="form-note">
                本次真实分析最多{config?.max_model_requests}
                次模型请求。提交会产生实际调用。
              </p>
              {upload && (
                <span className="upload-note">
                  <Check size={16} />
                  样本已读取，提交时由后端核验
                </span>
              )}
              {formError && (
                <p role="alert" className="failure-message">
                  {formError}
                </p>
              )}
              <Button type="submit" disabled={busy}>
                {busy ? "提交中…" : "提交真实分析"}
              </Button>
            </form>
          )}
          {dialog === "cancel" && (
            <>
              <p>已经发生的模型请求无法撤回。</p>
              <Button
                variant="destructive"
                disabled={busy}
                onClick={() => void cancel()}
              >
                {busy ? "正在停止…" : "确认停止"}
              </Button>
            </>
          )}
          {dialog === "report" && reportURL && (
            <>
              <iframe
                title="本轮研究报告"
                src={reportURL}
                sandbox=""
                referrerPolicy="no-referrer"
              />
              <a href={reportURL} target="_blank" rel="noopener">
                在新标签页打开报告
              </a>
            </>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}
