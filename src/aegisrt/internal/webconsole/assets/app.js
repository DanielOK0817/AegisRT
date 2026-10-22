import {
  ApiError, appendContext, cancelTask, clearAuthToken, getAgents, getEvents,
  getReady, getRuntime, getTasks, inspectContext, setAuthToken, submitGraph,
} from "/console/api.js";
import { acceptReplay, initialEventCursor, validateGraphJSON } from "/console/model.js";
import {
  byID, renderAgents, renderConnection, renderEvents, renderInspector,
  renderRuntime, renderTasks, renderWorkflowOptions, showToast,
} from "/console/render.js";
const state = {
  authorized: false,
  ready: false,
  runtime: null,
  agents: [],
  tasks: [],
  events: [],
  eventCursor: initialEventCursor(),
  eventAlerts: [],
  selectedTaskID: "",
  context: null, contextWrite: null,
  workflowFilter: "",
  search: "",
  refreshing: false,
  timer: 0,
};
function selectedTask() {
  return state.tasks.find((item) => item.task.id === state.selectedTaskID) || null;
}
function render() {
  renderConnection(state.authorized, state.ready);
  renderRuntime(state.runtime, state.ready, state.tasks);
  renderAgents(state.agents);
  renderWorkflowOptions(state.tasks, state.workflowFilter);
  renderTasks(state.tasks, state.workflowFilter, state.search, state.selectedTaskID, selectTask);
  renderInspector(selectedTask(), state.context, {
    inspect: inspectSelectedContext,
    append: openContextDialog,
    cancel: openCancelDialog,
  });
  const stream = state.eventCursor.streamID ? `事件流 ${state.eventCursor.streamID.slice(0, 8)}…` : "未连接";
  renderEvents(state.events, stream, state.eventAlerts);
}
async function refreshAnonymous() {
  try {
    state.runtime = await getRuntime();
  } catch {
    state.runtime = null;
    state.ready = false;
    return;
  }
  try {
    await getReady();
    state.ready = true;
  } catch {
    state.ready = false;
  }
}
async function refreshProtected() {
  const [taskPayload, agentPayload] = await Promise.all([getTasks(), getAgents()]);
  state.tasks = Array.isArray(taskPayload?.tasks) ? taskPayload.tasks : [];
  state.agents = Array.isArray(agentPayload?.agents) ? agentPayload.agents : [];
  if (state.selectedTaskID && !selectedTask()) {
    state.selectedTaskID = "";
    state.context = null;
  }
}
async function pollEvents() {
  if (!state.authorized) return;
  for (let page = 0; page < 10; page += 1) {
    let replay;
    try {
      replay = await getEvents(state.eventCursor);
    } catch (error) {
      if (error instanceof ApiError && error.status === 409 &&
          (error.code === "stale_event_cursor" || error.code === "invalid_event_cursor")) {
        state.eventCursor = initialEventCursor();
        state.events = [];
      addEventAlert("Runtime已重启或事件epoch已切换；已从新事件流重新读取。");
        return;
      }
      throw error;
    }
    if (replay.gap) {
      addEventAlert(`事件历史存在缺口：${replay.gap_from_id || "?"}–${replay.gap_through_id || "?"}。`);
    }
    const nextCursor = acceptReplay(state.eventCursor, replay);
    const merged = new Map(state.events.map((event) => [event.id, event]));
    for (const event of replay.events) merged.set(event.id, event);
    state.events = [...merged.values()].slice(-200);
    state.eventCursor = nextCursor;
    if (!replay.has_more) return;
  }
  addEventAlert("事件积压超过本轮分页上限，将在下一轮继续读取。");
}
function addEventAlert(message) {
  if (!state.eventAlerts.includes(message)) state.eventAlerts = [...state.eventAlerts.slice(-2), message];
}
async function refreshCycle() {
  if (state.refreshing) return;
  state.refreshing = true;
  try {
    await refreshAnonymous();
    if (state.authorized) {
      await refreshProtected();
      await pollEvents();
    }
  } catch (error) {
    handleError(error, false);
  } finally {
    state.refreshing = false;
    render();
    scheduleRefresh();
  }
}
function scheduleRefresh() {
  window.clearTimeout(state.timer);
  state.timer = window.setTimeout(refreshCycle, document.hidden ? 5000 : 1200);
}
async function connect(event) {
  event.preventDefault();
  const input = byID("token-input");
  const supplied = input.value;
  input.value = "";
  if (!setAuthToken(supplied)) {
    showToast("请输入有效Token。");
    return;
  }
  try {
    await refreshProtected();
    state.authorized = true;
    await pollEvents();
    showToast("已连接AegisRT管理接口。");
  } catch (error) {
    clearAuthToken();
    handleError(error, true);
  }
  render();
}
function lock(message = "管理会话已锁定。") {
  clearAuthToken();
  state.authorized = false;
  state.agents = [];
  state.tasks = [];
  state.events = [];
  state.eventCursor = initialEventCursor();
  state.eventAlerts = [];
  state.selectedTaskID = "";
  state.context = null;
  render();
  showToast(message);
}
function handleError(error, connecting) {
  if (error instanceof ApiError && error.status === 401) {
    lock(connecting ? "Token无效，连接被拒绝。" : "认证已失效，会话已锁定。");
    return;
  }
  showToast(error instanceof Error ? error.message : "操作失败");
}
async function selectTask(taskID) {
  state.selectedTaskID = taskID;
  state.context = null;
  state.contextWrite = null;
  render();
  await inspectSelectedContext();
}
async function inspectSelectedContext() {
  if (!state.selectedTaskID) return;
  try {
    state.context = await inspectContext(state.selectedTaskID);
    render();
  } catch (error) {
    handleError(error, false);
  }
}
function openSubmitDialog() {
  const editor = byID("graph-json");
  if (!editor.value.trim()) editor.value = JSON.stringify(exampleGraph(), null, 2);
  byID("graph-validation").textContent = "";
  byID("submit-dialog").showModal();
}
function exampleGraph() {
  const suffix = crypto.randomUUID().slice(0, 8);
  const agent = state.agents[0];
  return { tasks: [{
    id: `task-${suffix}`,
    workflow_id: `workflow-${suffix}`,
    agent_id: agent?.id || "replace-agent-id",
    goal: "Execute one generic runtime task and return a valid Agent output envelope.",
    priority: 5,
    resources: agent?.resources || { cpu_milli: 100, memory_mib: 128, llm_slots: 0, token_budget: 1000 },
    max_attempts: 2,
    timeout_ms: Math.min(agent?.execution_timeout_ms || 30000, 30000),
    idempotency_key: `console-${suffix}`,
  }] };
}
async function submitGraphForm(event) {
  event.preventDefault();
  const message = byID("graph-validation");
  try {
    const documentValue = validateGraphJSON(byID("graph-json").value);
    message.className = "form-message";
    message.textContent = "验证通过，正在提交…";
    await submitGraph(documentValue);
    byID("submit-dialog").close();
    byID("graph-json").value = "";
    await refreshProtected();
    showToast("任务图已提交，Runtime将自动调度。");
    render();
  } catch (error) {
    message.className = "form-message error";
    message.textContent = error instanceof Error ? error.message : "提交失败";
    handleError(error, false);
  }
}
function openCancelDialog() {
  const item = selectedTask();
  if (!item) return;
  byID("cancel-task-id").value = item.task.id;
  byID("cancel-copy").textContent = `将向 Runtime 请求取消任务 ${item.task.id}。活动 Worker 退出前状态可能暂不变化。`;
  byID("cancel-dialog").showModal();
}
async function cancelForm(event) {
  event.preventDefault();
  const taskID = byID("cancel-task-id").value;
  try {
    await cancelTask(taskID);
    byID("cancel-dialog").close();
    await refreshProtected();
    showToast("取消请求已发送。");
    render();
  } catch (error) {
    handleError(error, false);
  }
}
async function openContextDialog() {
  await inspectSelectedContext();
  if (!state.context?.etag) return;
  state.contextWrite = null;
  byID("context-task-id").value = state.selectedTaskID;
  byID("context-message").textContent = "";
  byID("context-dialog").showModal();
}
async function contextForm(event) {
  event.preventDefault();
  const taskID = byID("context-task-id").value;
  const message = byID("context-message");
  try {
    const input = {
      content: byID("context-content").value,
      visibility: byID("context-visibility").value,
      pin: byID("context-pin").checked,
      cold: byID("context-cold").checked,
    };
    const fingerprint = JSON.stringify(input);
    if (!state.contextWrite || state.contextWrite.fingerprint !== fingerprint) {
      state.contextWrite = { fingerprint, key: crypto.randomUUID() };
    }
    await appendContext(taskID, state.context.etag, state.contextWrite.key, input);
    byID("context-content").value = "";
    state.contextWrite = null;
    byID("context-dialog").close();
    await inspectSelectedContext();
    showToast("Context已追加并生成新Manifest。");
  } catch (error) {
    if (error instanceof ApiError && error.status === 412) {
      await inspectSelectedContext();
    message.textContent = "Manifest已变化，已刷新元数据；请确认内容后再次提交。";
      return;
    }
    message.textContent = error instanceof Error ? error.message : "追加失败";
    handleError(error, false);
  }
}
function bind() {
  byID("auth-form").addEventListener("submit", connect);
  byID("lock-button").addEventListener("click", () => lock());
  byID("refresh-button").addEventListener("click", refreshCycle);
  byID("open-submit-button").addEventListener("click", openSubmitDialog);
  byID("submit-form").addEventListener("submit", submitGraphForm);
  byID("cancel-form").addEventListener("submit", cancelForm);
  byID("context-form").addEventListener("submit", contextForm);
  byID("workflow-filter").addEventListener("change", (event) => { state.workflowFilter = event.target.value; render(); });
  byID("task-search").addEventListener("input", (event) => { state.search = event.target.value; render(); });
  byID("context-pin").addEventListener("change", (event) => { if (event.target.checked) byID("context-cold").checked = false; });
  byID("context-cold").addEventListener("change", (event) => { if (event.target.checked) byID("context-pin").checked = false; });
  for (const button of document.querySelectorAll("[data-close-dialog]")) {
    button.addEventListener("click", () => {
      const dialog = byID(button.dataset.closeDialog);
      dialog.close();
      if (dialog.id === "context-dialog") {
        byID("context-content").value = "";
        state.contextWrite = null;
      }
    });
  }
  document.addEventListener("visibilitychange", scheduleRefresh);
}
bind();
render();
refreshCycle();
