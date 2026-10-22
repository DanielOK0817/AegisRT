const terminalStatuses = new Set(["SUCCEEDED", "FAILED", "LOST", "CANCELED"]);
const statusClasses = new Map([
  ["SUBMITTED", "status-submitted"],
  ["WAITING_DEPENDENCY", "status-waiting"],
  ["READY", "status-ready"],
  ["ADMITTED", "status-admitted"],
  ["RUNNING", "status-running"],
  ["SUCCEEDED", "status-succeeded"],
  ["FAILED", "status-failed"],
  ["LOST", "status-lost"],
  ["RECOVERING", "status-recovering"],
  ["CANCELED", "status-canceled"],
]);

export function isTerminal(status) {
  return terminalStatuses.has(status);
}

export function statusClass(status) {
  return statusClasses.get(status) || "status-unknown";
}

export function summarizeTasks(tasks) {
  const summary = { total: tasks.length, running: 0, admitted: 0, ready: 0, failed: 0 };
  for (const item of tasks) {
    if (item.status === "RUNNING") summary.running += 1;
    if (item.status === "ADMITTED") summary.admitted += 1;
    if (item.status === "READY") summary.ready += 1;
    if (item.status === "FAILED" || item.status === "LOST") summary.failed += 1;
  }
  return summary;
}

export function workflows(tasks) {
  const grouped = new Map();
  for (const item of tasks) {
    const id = item.task.workflow_id;
    if (!grouped.has(id)) grouped.set(id, []);
    grouped.get(id).push(item);
  }
  for (const items of grouped.values()) {
    items.sort((left, right) => left.task.id.localeCompare(right.task.id));
  }
  return new Map([...grouped.entries()].sort(([left], [right]) => left.localeCompare(right)));
}

export function topology(tasks) {
  const byID = new Map(tasks.map((item) => [item.task.id, item]));
  const ranks = new Map();
  const visiting = new Set();
  const rank = (taskID) => {
    if (ranks.has(taskID)) return ranks.get(taskID);
    if (visiting.has(taskID)) return 0;
    visiting.add(taskID);
    const item = byID.get(taskID);
    let value = 0;
    if (item) {
      for (const dependency of item.task.dependencies || []) {
        if (byID.has(dependency)) value = Math.max(value, rank(dependency) + 1);
      }
    }
    visiting.delete(taskID);
    ranks.set(taskID, value);
    return value;
  };
  const layers = [];
  for (const item of tasks) {
    const value = rank(item.task.id);
    if (!layers[value]) layers[value] = [];
    layers[value].push(item);
  }
  return layers.filter(Boolean).map((items) => items.sort((a, b) => a.task.id.localeCompare(b.task.id)));
}

export function resourceRows(capacity = {}, allocated = {}) {
  const rows = [
    ["CPU", "mCPU", "cpu_milli"],
    ["内存", "MiB", "memory_mib"],
    ["LLM并发槽", "个", "llm_slots"],
    ["Token预算", "Token", "token_budget"],
  ].map(([label, unit, key]) => resourceRow(label, unit, allocated[key], capacity[key]));
  const tools = new Set([
    ...Object.keys(capacity.tool_slots || {}),
    ...Object.keys(allocated.tool_slots || {}),
  ]);
  for (const name of [...tools].sort()) {
    rows.push(resourceRow(`工具：${name}`, "个", allocated.tool_slots?.[name], capacity.tool_slots?.[name]));
  }
  return rows;
}

function resourceRow(label, unit, usedValue, capacityValue) {
  const used = safeNumber(usedValue);
  const capacity = safeNumber(capacityValue);
  return { label, unit, used, capacity, ratio: capacity > 0 ? Math.min(1, used / capacity) : 0 };
}

function safeNumber(value) {
  return Number.isFinite(value) && value >= 0 ? value : 0;
}

export function initialEventCursor() {
  return { streamID: "", afterID: "0" };
}

export function acceptReplay(cursor, replay) {
  if (!replay || typeof replay.stream_id !== "string" || !replay.stream_id ||
      typeof replay.next_after_id !== "string" || !Array.isArray(replay.events)) {
    throw new Error("事件响应格式无效");
  }
  if (cursor.streamID && replay.stream_id !== cursor.streamID) {
    throw new Error("事件流已切换");
  }
  const after = eventID(cursor.afterID);
  const next = eventID(replay.next_after_id);
  let expected = after + 1n;
  if (replay.gap) {
    const from = eventID(replay.gap_from_id);
    const through = eventID(replay.gap_through_id);
    if (from !== expected || through < from || replay.events.length === 0) throw new Error("事件缺口范围无效");
    expected = through + 1n;
  }
  for (const event of replay.events) {
    if (eventID(event.id) !== expected) throw new Error("事件顺序或缺口无效");
    expected += 1n;
  }
  if (next !== (replay.events.length ? expected - 1n : after) ||
      replay.has_more && replay.events.length === 0) throw new Error("事件游标无效");
  return { streamID: replay.stream_id, afterID: replay.next_after_id };
}

function eventID(value) {
  if (typeof value !== "string" || !/^(0|[1-9][0-9]{0,19})$/.test(value) ||
      BigInt(value) > 18446744073709551615n) throw new Error("事件ID无效");
  return BigInt(value);
}

export function validateGraphJSON(source) {
  let document;
  try {
    document = JSON.parse(source);
  } catch {
    throw new Error("请输入有效 JSON");
  }
  if (!document || typeof document !== "object" || Array.isArray(document) || !Array.isArray(document.tasks)) {
    throw new Error("顶层必须是包含 tasks 数组的对象");
  }
  if (document.tasks.length === 0 || document.tasks.length > 256) {
    throw new Error("任务数必须在 1 到 256 之间");
  }
  const ids = new Set();
  const workflowByID = new Map();
  for (const [index, task] of document.tasks.entries()) {
    if (!task || typeof task !== "object" || typeof task.id !== "string" || !task.id) {
      throw new Error(`tasks[${index}] 缺少有效 id`);
    }
    if (ids.has(task.id)) throw new Error(`任务 ID 重复: ${task.id}`);
    if (typeof task.workflow_id !== "string" || !task.workflow_id) {
      throw new Error(`任务 ${task.id} 缺少 workflow_id`);
    }
    if (task.dependencies !== undefined && !Array.isArray(task.dependencies)) {
      throw new Error(`任务 ${task.id} 的 dependencies 必须是数组`);
    }
    ids.add(task.id);
    workflowByID.set(task.id, task.workflow_id);
  }
  for (const task of document.tasks) {
    for (const dependency of task.dependencies || []) {
      if (!ids.has(dependency)) throw new Error(`任务 ${task.id} 依赖不存在的 ${dependency}`);
      if (workflowByID.get(dependency) !== task.workflow_id) {
        throw new Error(`任务 ${task.id} 存在跨工作流依赖`);
      }
    }
  }
  assertAcyclic(document.tasks);
  return document;
}

function assertAcyclic(tasks) {
  const dependencies = new Map(tasks.map((task) => [task.id, task.dependencies || []]));
  const visiting = new Set();
  const complete = new Set();
  const visit = (id) => {
    if (complete.has(id)) return;
    if (visiting.has(id)) throw new Error(`任务图包含环: ${id}`);
    visiting.add(id);
    for (const dependency of dependencies.get(id) || []) visit(dependency);
    visiting.delete(id);
    complete.add(id);
  };
  for (const id of dependencies.keys()) visit(id);
}

export function formatTime(value) {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? "—" : date.toLocaleString();
}
