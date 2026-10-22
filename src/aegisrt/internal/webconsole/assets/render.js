import { formatTime, isTerminal, resourceRows, statusClass, summarizeTasks, topology, workflows } from "/console/model.js";

const byID = (id) => document.getElementById(id);

function textNode(tag, text, className = "") {
  const result = document.createElement(tag);
  result.textContent = text;
  if (className) result.className = className;
  return result;
}

function replace(id, ...children) {
  byID(id).replaceChildren(...children);
}

function setText(id, value) {
  byID(id).textContent = value ?? "—";
}

export function renderConnection(authorized, ready, message = "") {
  const chip = byID("connection-status");
  chip.className = `status-chip ${authorized ? (ready ? "status-online" : "status-degraded") : "status-offline"}`;
  chip.textContent = authorized ? (ready ? "已连接" : "已连接 · 降级") : "离线";
  byID("lock-button").hidden = !authorized;
  byID("open-submit-button").disabled = !authorized;
  byID("auth-panel").hidden = authorized;
  if (message) byID("announcer").textContent = message;
}

export function renderRuntime(runtime, ready, tasks = []) {
  const stats = runtime?.stats || {};
  const context = runtime?.context_stats || {};
  const store = context.store || {};
  setText("runtime-version", runtime?.version);
  setText("runtime-policy", stats.policy);
  setText("runtime-started", runtime?.started_at ? formatTime(runtime.started_at) : "—");
  setText("runtime-generation", stats.graph_version ?? "—");
  const readyChip = byID("runtime-ready");
  readyChip.className = `status-chip ${ready ? "status-online" : "status-degraded"}`;
  readyChip.textContent = ready ? "就绪" : "降级";
  const summary = summarizeTasks(tasks);
  setText("metric-total", summary.total);
  setText("metric-running", summary.running);
  setText("metric-admitted", summary.admitted);
  setText("metric-pending", stats.pending_completion_count ?? 0);
  setText("metric-ready", summary.ready);
  setText("metric-failed", summary.failed);
  setText("metric-reuse", store.reused_tokens ?? 0);
  setText("metric-manifests", context.manifest_count ?? 0);
  setText("metric-pages", store.unique_pages ?? 0);
  renderResources(stats.capacity || {}, stats.allocated || {});
}

function renderResources(capacity, allocated) {
  const cards = resourceRows(capacity, allocated).map((row) => {
    const card = document.createElement("article");
    card.className = "resource-card";
    const header = document.createElement("header");
    header.append(textNode("span", row.label), textNode("strong", `${row.used} / ${row.capacity} ${row.unit}`));
    const progress = document.createElement("progress");
    progress.max = row.capacity > 0 ? row.capacity : 1;
    progress.value = Math.min(row.used, progress.max);
    progress.setAttribute("aria-label", `${row.label} 已预留 ${row.used}，容量 ${row.capacity}`);
    card.append(header, progress);
    return card;
  });
  replace("resource-grid", ...cards);
}

export function renderAgents(agents) {
  if (!agents.length) {
    replace("agent-grid", textNode("p", "当前注册表没有可展示的Agent。", "empty-state"));
    return;
  }
  const cards = agents.map((agent) => {
    const card = document.createElement("article");
    card.className = "agent-card";
    card.append(
      textNode("h3", agent.name || agent.id),
      textNode("p", `${agent.id} · ${agent.runtime}`),
      textNode("p", `并发上限 ${agent.max_concurrency} · 超时 ${agent.execution_timeout_ms} ms`),
      textNode("p", `请求 ${agent.resources?.cpu_milli || 0} mCPU / ${agent.resources?.memory_mib || 0} MiB`),
    );
    const tags = document.createElement("div");
    tags.className = "tag-row";
    for (const capability of agent.capabilities || []) tags.append(textNode("span", capability, "tag"));
    card.append(tags);
    return card;
  });
  replace("agent-grid", ...cards);
}

export function renderWorkflowOptions(tasks, selected) {
  const options = [new Option("全部", "")];
  for (const workflowID of workflows(tasks).keys()) options.push(new Option(workflowID, workflowID));
  const select = byID("workflow-filter");
  select.replaceChildren(...options);
  select.value = selected;
  if (select.value !== selected) select.value = "";
}

export function renderTasks(tasks, selectedWorkflow, search, selectedTaskID, onSelect) {
  const term = search.trim().toLocaleLowerCase();
  const filtered = tasks.filter((item) => {
    if (selectedWorkflow && item.task.workflow_id !== selectedWorkflow) return false;
    if (!term) return true;
    return [item.task.id, item.task.agent_id, item.task.workflow_id, item.status]
      .some((value) => String(value).toLocaleLowerCase().includes(term));
  });
  byID("workflow-empty").hidden = filtered.length > 0;
  renderDAG(filtered, selectedTaskID, onSelect);
  const rows = filtered.map((item) => {
    const row = document.createElement("tr");
    row.tabIndex = 0;
    row.setAttribute("aria-selected", String(item.task.id === selectedTaskID));
    row.setAttribute("aria-label", `任务 ${item.task.id}，状态 ${item.status}`);
    row.append(
      textNode("td", item.status, statusClass(item.status)),
      textNode("td", item.task.id),
      textNode("td", item.task.agent_id),
      textNode("td", String(item.task.priority)),
      textNode("td", (item.task.dependencies || []).join(", ") || "—"),
    );
    row.addEventListener("click", () => onSelect(item.task.id));
    row.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") onSelect(item.task.id);
    });
    return row;
  });
  replace("task-table-body", ...rows);
}

function renderDAG(tasks, selectedTaskID, onSelect) {
  if (!tasks.length) {
    replace("dag-board");
    return;
  }
  if (tasks.length > 50) {
    replace("dag-board", textNode("p", "该视图超过 50 个节点，请使用下方紧凑任务表。", "empty-state"));
    return;
  }
  const layers = topology(tasks).map((items, index) => {
    const layer = document.createElement("section");
    layer.className = "dag-layer";
    layer.setAttribute("aria-label", `拓扑层级 ${index + 1}`);
    for (const item of items) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = `dag-node ${statusClass(item.status)}${item.task.id === selectedTaskID ? " dag-node-selected" : ""}`;
      button.setAttribute("aria-label", `任务 ${item.task.id}，Agent ${item.task.agent_id}，状态 ${item.status}`);
      button.append(
        textNode("strong", item.task.id),
      textNode("small", `${item.task.agent_id} · ${item.status}`),
        textNode("small", `依赖: ${(item.task.dependencies || []).join(", ") || "无"}`),
      );
      button.addEventListener("click", () => onSelect(item.task.id));
      layer.append(button);
    }
    return layer;
  });
  replace("dag-board", ...layers);
}

export function renderInspector(item, context, actions) {
  if (!item) {
    replace("task-inspector", textNode("p", "选择一个任务查看资源、依赖和Context元数据。", "empty-state"));
    return;
  }
  const list = document.createElement("dl");
  list.className = "inspector-list";
  const fields = [
    ["任务ID", item.task.id], ["工作流", item.task.workflow_id],
    ["Agent", item.task.agent_id], ["状态", item.status],
    ["目标", item.task.goal], ["优先级", item.task.priority],
    ["依赖任务", (item.task.dependencies || []).join(", ") || "无"],
    ["资源请求", JSON.stringify(item.task.resources || {})],
    ["超时时间", `${item.task.timeout_ms} ms`], ["最大尝试次数", item.task.max_attempts],
  ];
  if (context?.snapshot?.manifest) {
    const manifest = context.snapshot.manifest;
    fields.push(
      ["Context版本", manifest.revision], ["Context页面数", manifest.page_count],
      ["逻辑Token数", manifest.logical_tokens], ["固定Token数", manifest.pinned_tokens],
    );
  }
  for (const [label, value] of fields) {
    const entry = document.createElement("div");
    entry.append(textNode("dt", label), textNode("dd", String(value ?? "—")));
    list.append(entry);
  }
  const controls = document.createElement("div");
  controls.className = "inspector-actions";
  const refresh = actionButton("刷新Context", "button-secondary", actions.inspect);
  const append = actionButton("追加Context", "button-primary", actions.append);
  controls.append(refresh, append);
  if (!isTerminal(item.status)) controls.append(actionButton("取消任务", "button-danger", actions.cancel));
  replace("task-inspector", list, controls);
}

function actionButton(label, className, action) {
  const button = textNode("button", label, `button ${className}`);
  button.type = "button";
  button.addEventListener("click", action);
  return button;
}

export function renderEvents(events, streamLabel, alerts) {
  setText("event-stream-status", streamLabel || "未连接");
  replace("event-alerts", ...alerts.map((message) => textNode("p", message, "alert")));
  if (!events.length) {
    replace("event-list", textNode("li", "当前事件流尚无终态事件。", "empty-state"));
    return;
  }
  const items = events.slice(-100).reverse().map((event) => {
    const item = document.createElement("li");
    item.className = `event-item ${eventStatusClass(event.type)}`;
    const marker = document.createElement("span");
    marker.className = "event-marker";
    marker.setAttribute("aria-hidden", "true");
    const copy = document.createElement("div");
    copy.className = "event-copy";
    const context = event.payload?.context;
    const metrics = context ? ` · Context ${context.physical_tokens}/${context.logical_tokens} Token` : "";
    const scheduling = event.payload?.scheduling;
    const output = event.payload?.output;
    const phase = { before_start: "启动前结束", process_started: "已启动，退出未确认", process_exited: "进程退出已确认" }[event.payload?.lifecycle?.phase];
    copy.append(textNode("strong", `${event.type} · ${event.task_id || "runtime"}`),
      textNode("small", `${event.workflow_id || "—"} · 尝试 ${event.attempt_id || "—"}${metrics}`));
    if (phase) copy.append(textNode("small", phase));
    if (scheduling) copy.append(textNode("small", `调度时账目 ${scheduling.workflow_vruntime} · 在途权重 ${scheduling.workflow_reserved_charge}`));
    if (output) copy.append(textNode("small", `已确认产物 ${output.artifact_count} 项 · 内容 ${output.content_bytes} 字节`));
    item.append(marker, copy, textNode("time", formatTime(event.time), "event-time"));
    return item;
  });
  replace("event-list", ...items);
}

function eventStatusClass(type) {
  const classes = {
    "run.succeeded": "status-succeeded", "run.failed": "status-failed",
    "run.canceled": "status-canceled", "run.timed_out": "status-failed",
    "run.interrupted": "status-lost",
  };
  return classes[type] || "status-unknown";
}

export function showToast(message) {
  const toast = byID("toast");
  toast.textContent = message;
  toast.hidden = false;
  window.setTimeout(() => { toast.hidden = true; }, 3200);
}

export { byID };
