# @iotanalytics/client

TypeScript SDK for the IoTAnalytics Engine. Zero runtime dependencies; works in
browsers, React Native (with a streaming `fetch`) and Node 18+.

```ts
import { EngineClient, FeedClient, FleetStore } from "@iotanalytics/client";

const api = new EngineClient({ baseUrl: "http://localhost:8080" });
const store = new FleetStore();

// Seed from REST, then follow the live stream. The feed resumes from the last
// durable sequence number on reconnect, so alerts are never lost.
store.applySnapshots((await api.fleet()).vehicles);
store.applyAlertHistory(await api.alerts({ limit: 50 }));

const feed = new FeedClient({ baseUrl: "http://localhost:8080" }).on({
  reading: (e) => store.apply(e),
  alert: (e) => store.apply(e),
  geofence: (e) => store.apply(e),
  gap: async () => store.applyAlertHistory(await api.alerts({ limit: 50 })),
  status: (s) => console.log("feed", s),
});
void feed.start();

store.subscribe(() => {
  render(store.vehicleSummaries());      // one row per vehicle, ranked by attention
  renderAlerts(store.prioritizedAlerts()); // critical > warning > info, newest first
});
```

| Module | Purpose |
|---|---|
| `EngineClient` | Typed wrapper over `/api/v1/*` (ingest, fleet, vehicles, alerts, zones, events, stats) with `ApiError` |
| `FeedClient` | SSE consumer over `fetch` + `ReadableStream`; `Last-Event-ID` resume, jittered exponential backoff, `gap` handling |
| `SseParser` | Spec-conformant, chunk-tolerant SSE frame parser |
| `FleetStore` | Framework-agnostic reducer folding events + snapshots into fleet state, with selectors |
| `insights` | `prioritizeAlerts`, `summarize`, `attentionScore`, `alertBreakdown` |

```bash
npm install
npm test        # tsc (strict) + node:test
```
