# Architecture

This document explains *why* the engine is shaped the way it is: the data
flow, the concurrency model, the durability guarantees, and the trade-offs
that were made deliberately.

## 1. Pipeline

Every reading takes the same path through `engine.Ingest`:

```
validate → fleet window → alert rules → geofence monitor → store → bus
```

* **Validate first.** `telematics.Reading.Validate` rejects physically
  implausible values (negative speed, > 400 km/h, fuel outside 0–100, bad
  coordinates). Bad sensors and malformed clients therefore cannot poison
  rolling metrics or trigger phantom alerts. Rejections are counted and
  surfaced in `/api/v1/stats` and `iot_readings_rejected_total`.
* **Analysis before persistence.** Window, rules and geofences are pure
  in-memory operations and always run. Persistence comes last; if the disk
  fails the reading is still analysed and streamed (as a non-durable event)
  and `Ingest` returns `engine.ErrPersist`. Availability degrades to
  "not durable" rather than "down".
* **One event type on the wire.** Alerts, geofence transitions and readings
  are all wrapped in `storage.Event{Seq, At, Type, VehicleID, Payload}`. The
  same shape is written to disk, published on the bus, streamed over SSE and
  consumed by the metrics exporter, so there is exactly one serialisation.

## 2. Sliding windows

`aggregation.Window` keeps a time-ordered slice of readings and evicts those
older than `newest − span` on every insert. Two details matter in practice:

* **Late arrivals.** Cellular uplinks reorder samples. Readings older than the
  newest one are inserted at their chronological position (binary search), so
  distance and fuel-burn computations, which depend on order, stay correct.
  Readings older than the whole window are dropped.
* **Memory.** Re-slicing from the front pins the backing array. When capacity
  exceeds 4× the live length the window copies into a fresh slice so memory
  for evicted readings is actually reclaimed over a long run.

`Snapshot()` computes every metric in one pass under a read lock; the hot
path (`Add`) is 155 ns with zero allocations. `Fleet` shards by vehicle ID with
a double-checked map lookup, so concurrent ingest for different vehicles does
not contend.

## 3. Alert engine

Rules implement `Evaluate(Reading) *Alert`. Stateless rules (`Threshold`,
`Speeding`, `LowFuel`) are closures; stateful ones (`HarshBraking`,
`ExcessiveIdle`) keep per-vehicle state behind a mutex.

**Cooldown de-duplication** is the feature that makes alerts usable. A vehicle
sampled at 1 Hz that is speeding for a minute would otherwise raise 60
identical alerts. The engine remembers the last admitted alert per
`(vehicle, kind)` and suppresses repeats within the cooldown *unless the
severity increased*, so a warning that escalates to critical is never hidden.

`HarshBraking` requires both a high deceleration *rate* and a minimum absolute
speed drop (`MinHarshDropKph`). At high sample rates sensor jitter of a few
km/h over 200 ms is a large rate but not a braking event.

History is a fixed ring buffer (`WithHistory`), so a long-running engine has
bounded memory; the durable record lives in the store.

## 4. Geofencing

Zones implement `Contains(geo.Point)`. Circles use haversine distance; polygons
use the even-odd ray-casting test on lat/lng treated as planar, which is
accurate to well under a metre at city scale. The `Monitor` keeps a
`vehicle → zone → entered-at` map and emits a `Transition` only when the
boolean flips. The first observation of a vehicle establishes a baseline
without emitting, so a vehicle that boots inside the depot does not "enter"
it. Exit transitions carry dwell time.

## 5. Durability

`storage.Store` is a newline-delimited JSON append-only log:

* `Append` writes the line, flushes the buffered writer and, in `SyncAlways`
  mode, calls `fsync`. An acknowledged event is on stable storage.
* Each event gets `Seq = lastSeq + 1` under the store mutex, so sequence
  numbers are dense and strictly increasing, and they continue across
  restarts because `Open` replays the log to recover `lastSeq`.
* **Torn tail recovery.** A crash during a write leaves a partial last line.
  `Open` tracks the byte offset of the last well-formed line and truncates the
  file there, so the log is always parseable without manual repair. Corrupt
  lines in the middle (e.g. disk bit-rot) are skipped and counted.
* A bounded in-memory ring (`WithRetention`) serves `Since`, `Recent` and
  `ByVehicle` without touching disk. `Since` reports `truncated=true` when
  the requested position has aged out, which the SSE handler turns into a
  `gap` event.

`SyncNone` trades the per-event fsync (typically 1–10 ms on SSDs) for
throughput when a few seconds of loss on power failure is acceptable.

## 6. Streaming and resume

The SSE handler subscribes to the bus *before* replaying from the store, then
filters duplicates by sequence number. This closes the race where an event is
published between "read the store" and "start listening". Delivery semantics
for durable events are therefore **at-least-once with client-side dedupe by
`seq`**, and in practice exactly-once because the handler tracks `lastSent`.

Readings are not persisted by default (they are high-volume and the windows
already summarise them), so they stream with `seq = 0` and no `id:` line.
Browsers' `EventSource` and the TypeScript `FeedClient` only send
`Last-Event-ID` for events that had an id, which is exactly the durable set.

`bus.Bus` never blocks on `Publish`: a subscriber whose buffer is full misses
the message and the drop is counted (`iot_stream_dropped_total`). A slow
dashboard therefore cannot apply back-pressure to ingest, and because durable
events can be replayed from the store, nothing important is lost.

## 7. Concurrency model

* All shared structures (`Window`, `Fleet`, alert `Engine`, `Monitor`,
  `Store`, `Bus`) are internally synchronised and documented as safe for
  concurrent use. `Ingest` can be called from any number of goroutines (HTTP
  handlers, the in-process simulator).
* Lock ordering is trivial because no component calls into another while
  holding its own lock: the engine calls them sequentially.
* `Bus.Publish` holds a read lock while sending; `Subscription.Close` takes
  the write lock before closing the channel, so a send on a closed channel is
  impossible.
* The whole suite runs under `-race` in CI, including dedicated contention
  tests for every shared structure.

## 8. Observability

* Structured logs via `log/slog` (text or JSON), including per-request access
  logs with request IDs.
* `/metrics` in Prometheus exposition format, hand-written to avoid a client
  dependency. Counters that need labels (alert kind/severity, zone/event) are
  derived by subscribing to the bus, which keeps the engine free of metrics
  concerns.
* `/api/v1/stats` for quick human inspection and the dashboard.
* The Grafana board in `deploy/grafana/dashboards` is provisioned
  automatically by `docker compose up`.

## 9. What would change at scale

| Concern | Today | Next step |
|---|---|---|
| Ingress | HTTP POST (devices or gateway) | MQTT/Kafka consumer calling `engine.Ingest` |
| Horizontal scale | single process | shard by `vehicle_id`; windows and rule state are per-vehicle so no cross-shard coordination is needed |
| Storage | local NDJSON log | segment rotation + object store, or a TSDB; `storage.Store` is the only package to replace |
| Auth | none | middleware in `internal/api` (the request-ID/CORS layers show where) |
| Zones | static JSON | CRUD API; `Monitor.AddZone` already exists |
