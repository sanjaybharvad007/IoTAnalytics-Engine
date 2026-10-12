import assert from "node:assert/strict";
import { test } from "node:test";
import { SseParser } from "./sse.js";

test("parses complete frames with id, event and data", () => {
  const p = new SseParser();
  const frames = p.push("retry: 3000\n\nid: 7\nevent: alert\ndata: {\"a\":1}\n\n");
  assert.equal(frames.length, 1);
  assert.deepEqual(frames[0], { id: "7", event: "alert", data: '{"a":1}', retry: 3000 });
  assert.equal(p.lastEventId, "7");
});

test("buffers partial frames across chunks", () => {
  const p = new SseParser();
  assert.deepEqual(p.push("event: geof"), []);
  assert.deepEqual(p.push("ence\ndata: {\"x\""), []);
  const frames = p.push(":2}\n\n");
  assert.equal(frames.length, 1);
  assert.equal(frames[0]?.event, "geofence");
  assert.equal(frames[0]?.data, '{"x":2}');
});

test("ignores comments/heartbeats and joins multi-line data", () => {
  const p = new SseParser();
  const frames = p.push(": ping\n\ndata: line1\ndata: line2\n\n");
  assert.equal(frames.length, 1);
  assert.equal(frames[0]?.event, "message");
  assert.equal(frames[0]?.data, "line1\nline2");
});

test("handles CRLF line endings and frames without an id", () => {
  const p = new SseParser();
  const frames = p.push("event: reading\r\ndata: {}\r\n\r\n");
  assert.equal(frames.length, 1);
  assert.equal(frames[0]?.id, undefined);
  assert.equal(frames[0]?.event, "reading");
});
