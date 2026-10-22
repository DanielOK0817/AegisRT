import { readFile } from "node:fs/promises";
import assert from "node:assert/strict";
import test from "node:test";

const source = await readFile(new URL("./assets/model.js", import.meta.url), "utf8");
const model = await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}`);
const modelURL = `data:text/javascript;base64,${Buffer.from(source).toString("base64")}`;
const renderSource = (await readFile(new URL("./assets/render.js", import.meta.url), "utf8"))
  .replace('"/console/model.js"', JSON.stringify(modelURL));
const render = await import(`data:text/javascript;base64,${Buffer.from(renderSource).toString("base64")}`);

test("admitted tasks are not reported as running", () => {
  const summary = model.summarizeTasks([
    { status: "ADMITTED" }, { status: "RUNNING" }, { status: "READY" }, { status: "FAILED" },
  ]);
  assert.equal(summary.running, 1);
  assert.equal(summary.admitted, 1);
});

test("console renders separated task counts and safe event details", async () => {
  const html = await readFile(new URL("./assets/index.html", import.meta.url), "utf8");
  const element = () => ({
    textContent: "", children: [], setAttribute() {},
    append(...children) { this.children.push(...children); },
    replaceChildren(...children) { this.children = children; },
  });
  const nodes = new Map([...html.matchAll(/id="([^"]+)"/g)].map((match) => [match[1], element()]));
  globalThis.document = { createElement: element, getElementById: (id) => {
    assert(nodes.has(id), `missing HTML node ${id}`);
    return nodes.get(id);
  } };
  render.renderRuntime({ stats: { pending_completion_count: 1 } }, true, [
    { status: "ADMITTED" }, { status: "RUNNING" },
  ]);
  assert.equal(nodes.get("metric-admitted").textContent, 1);
  assert.equal(nodes.get("metric-running").textContent, 1);
  assert.equal(nodes.get("metric-pending").textContent, 1);
  render.renderEvents([{
    type: "run.succeeded", task_id: "task-1", time: "2026-09-22T00:00:00Z",
    payload: { scheduling: { workflow_vruntime: 2, workflow_reserved_charge: 0.5 },
      lifecycle: { phase: "process_exited" }, output: { artifact_count: 1, content_bytes: 14 } },
  }], "stream-1", []);
  const allText = (node) => [String(node.textContent), ...node.children.map(allText)].join(" ");
  const text = allText(nodes.get("event-list"));
  assert.match(text, /在途权重 0.5/);
  assert.match(text, /已确认产物 1 项/);
  assert.match(text, /进程退出已确认/);
  delete globalThis.document;
});

test("event replay preserves uint64 precision and explicit eviction gaps", () => {
  const cursor = { streamID: "stream-1", afterID: "9007199254740993" };
  const replay = {
    stream_id: cursor.streamID, events: [{ id: "9007199254740996" }, { id: "9007199254740997" }],
    gap: true, gap_from_id: "9007199254740994", gap_through_id: "9007199254740995",
    next_after_id: "9007199254740997", has_more: false,
  };
  assert.deepEqual(model.acceptReplay(cursor, replay), { streamID: cursor.streamID, afterID: replay.next_after_id });
  assert.throws(() => model.acceptReplay(cursor, { ...replay, gap_from_id: cursor.afterID }));
  assert.throws(() => model.acceptReplay(cursor, { ...replay, stream_id: "new-stream" }));
});

test("event replay rejects a cursor rewind and hidden missing event", () => {
  const cursor = { streamID: "stream-1", afterID: "9007199254740993" };
  assert.throws(() => model.acceptReplay(cursor, {
    stream_id: cursor.streamID, events: [], next_after_id: "9007199254740992",
  }));
  assert.throws(() => model.acceptReplay(cursor, {
    stream_id: cursor.streamID, events: [{ id: "9007199254740995" }],
    next_after_id: "9007199254740995", gap: false,
  }));
});
