import assert from "node:assert/strict";
import { test } from "node:test";
import { ApiError, EngineClient } from "./client.js";
import { FeedClient } from "./feed.js";

interface Call {
  url: string;
  init?: RequestInit;
}

function fakeFetch(router: (url: string, init?: RequestInit) => Response | Promise<Response>) {
  const calls: Call[] = [];
  const fetchImpl = async (url: string, init?: RequestInit) => {
    calls.push({ url, ...(init ? { init } : {}) });
    return router(url, init);
  };
  return { calls, fetchImpl };
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

test("EngineClient builds URLs, sends JSON bodies and parses responses", async () => {
  const { calls, fetchImpl } = fakeFetch((url) => {
    if (url.endsWith("/api/v1/telemetry")) return json({ accepted: 1, rejected: 0, alerts: [], transitions: [] }, 202);
    if (url.includes("/api/v1/alerts?limit=5&severity=critical")) return json([]);
    if (url.includes("/api/v1/events?after=10&limit=2&type=alert")) return json({ events: [], truncated: false, last_seq: 10 });
    if (url.endsWith("/api/v1/vehicles/van%2F1?limit=50")) return json({ snapshot: {}, zones: [], events: [] });
    return json({ error: "not found" }, 404);
  });
  const c = new EngineClient({ baseUrl: "http://engine/", fetch: fetchImpl, headers: { "X-Tenant": "acme" } });

  const r = await c.ingest({ vehicle_id: "v", timestamp: "t", speed: 1, fuel_level: 1, engine_on: true, lat: 0, lng: 0 });
  assert.equal(r.accepted, 1);
  assert.equal(calls[0]?.url, "http://engine/api/v1/telemetry");
  assert.equal(calls[0]?.init?.method, "POST");
  assert.equal((calls[0]?.init?.headers as Record<string, string>)["X-Tenant"], "acme");
  assert.equal((calls[0]?.init?.headers as Record<string, string>)["Content-Type"], "application/json");
  assert.match(String(calls[0]?.init?.body), /"vehicle_id":"v"/);

  assert.deepEqual(await c.alerts({ limit: 5, severity: "critical" }), []);
  assert.equal((await c.events({ after: 10, limit: 2, type: "alert" })).last_seq, 10);
  await c.vehicle("van/1"); // path segment is encoded

  await assert.rejects(c.vehicle("ghost"), (err: unknown) => {
    assert.ok(err instanceof ApiError);
    assert.equal(err.status, 404);
    assert.match(err.message, /not found/);
    return true;
  });
});

/** Builds a streaming Response from SSE text, optionally never ending. */
function sseResponse(text: string, keepOpen = false): Response {
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(text));
      if (!keepOpen) controller.close();
    },
  });
  return new Response(stream, { status: 200, headers: { "Content-Type": "text/event-stream" } });
}

const alertFrame = (seq: number) =>
  `id: ${seq}\nevent: alert\ndata: ${JSON.stringify({
    seq, at: "t", type: "alert", vehicle_id: "v1",
    payload: { vehicle_id: "v1", kind: "speeding", severity: "warning", message: "", value: 0, threshold: 0, at: "t" },
  })}\n\n`;

test("FeedClient dispatches typed events, tracks lastSeq and resumes with Last-Event-ID", async () => {
  let connection = 0;
  const { calls, fetchImpl } = fakeFetch(() => {
    connection++;
    if (connection === 1) return sseResponse(`retry: 10\n\n${alertFrame(1)}${alertFrame(2)}`); // server closes
    return sseResponse(`event: gap\ndata: {"after":2,"oldest":5}\n\n${alertFrame(5)}`, true);
  });

  const feed = new FeedClient({ baseUrl: "http://engine", fetch: fetchImpl, types: ["alert"], minBackoffMs: 1, maxBackoffMs: 2 });
  const seen: number[] = [];
  const statuses: string[] = [];
  let gap: { after: number; oldest: number } | undefined;
  feed.on({
    alert: (e) => {
      seen.push(e.seq);
      if (e.seq === 5) feed.close();
    },
    gap: (g) => (gap = g),
    status: (s) => statuses.push(s),
  });
  await feed.start();

  assert.deepEqual(seen, [1, 2, 5]);
  assert.equal(feed.lastSeq, 5);
  assert.deepEqual(gap, { after: 2, oldest: 5 });
  assert.equal(calls[0]?.url, "http://engine/api/v1/stream?types=alert");
  assert.equal((calls[0]?.init?.headers as Record<string, string>)["Last-Event-ID"], undefined);
  assert.equal((calls[1]?.init?.headers as Record<string, string>)["Last-Event-ID"], "2");
  assert.deepEqual(statuses, ["connecting", "live", "reconnecting", "live", "closed"]);
});

test("FeedClient reports errors, backs off and honours maxAttempts", async () => {
  const { calls, fetchImpl } = fakeFetch(() => new Response("nope", { status: 503 }));
  const feed = new FeedClient({ baseUrl: "http://engine", fetch: fetchImpl, minBackoffMs: 1, maxBackoffMs: 2, maxAttempts: 3 });
  const errors: unknown[] = [];
  feed.on({ error: (e) => errors.push(e) });
  await feed.start();
  assert.equal(calls.length, 3);
  assert.equal(errors.length, 3);
  assert.equal(feed.status, "closed");
});
