let bearerToken = "";

export class ApiError extends Error {
  constructor(status, code, message) {
    super(message || "请求失败");
    this.name = "ApiError";
    this.status = status;
    this.code = code || "request_failed";
  }
}

export function setAuthToken(value) {
  bearerToken = typeof value === "string" ? value.trim() : "";
  return bearerToken.length > 0;
}

export function clearAuthToken() {
  bearerToken = "";
}

export function hasAuthToken() {
  return bearerToken.length > 0;
}

async function request(path, options = {}) {
  if (!path.startsWith("/") || path.startsWith("//")) {
    throw new ApiError(0, "invalid_target", "拒绝非同源请求");
  }
  const headers = new Headers(options.headers || {});
  if (options.auth) {
    if (!bearerToken) {
    throw new ApiError(401, "unauthorized", "需要Bearer Token");
    }
    headers.set("Authorization", `Bearer ${bearerToken}`);
  }
  if (options.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }
  let response;
  try {
    response = await fetch(path, {
      method: options.method || "GET",
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      credentials: "omit",
      cache: "no-store",
      signal: options.signal,
    });
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") {
      throw error;
    }
      throw new ApiError(0, "network_error", "无法连接本地AegisRT守护进程");
  }

  const contentType = response.headers.get("Content-Type") || "";
  let payload = null;
  if (contentType.startsWith("application/json")) {
    try {
      payload = await response.json();
    } catch {
    throw new ApiError(response.status, "invalid_response", "Runtime返回了无效JSON");
    }
  }
  if (!response.ok) {
    const envelope = payload && typeof payload.error === "object" ? payload.error : {};
    throw new ApiError(response.status, envelope.code, envelope.message || `请求失败 (${response.status})`);
  }
  return { data: payload, headers: response.headers, status: response.status };
}

export async function getHealth(signal) {
  return (await request("/healthz", { signal })).data;
}

export async function getReady(signal) {
  return (await request("/readyz", { signal })).data;
}

export async function getRuntime(signal) {
  return (await request("/v1/runtime", { signal })).data;
}

export async function getAgents(signal) {
  return (await request("/v1/agents", { auth: true, signal })).data;
}

export async function getTasks(signal) {
  return (await request("/v1/tasks", { auth: true, signal })).data;
}

export async function getEvents(cursor, signal) {
  const params = new URLSearchParams({ after_id: cursor.afterID, limit: "100" });
  if (cursor.streamID) {
    params.set("stream_id", cursor.streamID);
  }
  return (await request(`/v1/events?${params.toString()}`, { auth: true, signal })).data;
}

export async function submitGraph(document, signal) {
  return (await request("/v1/task-graphs/patches", {
    auth: true, method: "POST", body: document, signal,
  })).data;
}

export async function cancelTask(taskID, signal) {
  return (await request(`/v1/tasks/${encodeURIComponent(taskID)}/cancel`, {
    auth: true, method: "POST", body: {}, signal,
  })).data;
}

export async function inspectContext(taskID, signal) {
  const result = await request(`/v1/tasks/${encodeURIComponent(taskID)}/context`, {
    auth: true, signal,
  });
  return { snapshot: result.data, etag: result.headers.get("ETag") || "" };
}

export async function appendContext(taskID, etag, idempotencyKey, input, signal) {
  return (await request(`/v1/tasks/${encodeURIComponent(taskID)}/context/pages`, {
    auth: true,
    method: "POST",
    body: input,
    signal,
    headers: { "If-Match": etag, "Idempotency-Key": idempotencyKey },
  })).data;
}
