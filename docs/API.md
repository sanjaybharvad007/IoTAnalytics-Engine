# HTTP API

Base path: `/api/v1`. All responses are JSON unless noted. Errors use
`{"error": "<message>"}` with an appropriate 4xx/5xx status. Every response
carries an `X-Request-ID` header (echoed from the request if supplied).

## Ingest

### `POST /api/v1/telemetry`

Body: a single reading or a JSON array of readings (max 1 MiB).

```json
{
  "vehicle_id": "van-7",
  "timestamp": "2026-10-12T06:30:00Z",
  "speed": 112.4,
  "fuel_level": 41,
  "engine_on": true,
  "lat": 37.3382,
  "lng": -121.8863,
  "heading": 270
}
```

Validation: `vehicle_id` and `timestamp` required; `0 ≤ speed ≤ 400`;
`0 ≤ fuel_level ≤ 100`; valid WGS-84 coordinates. `lat/lng = 0,0` means "no
fix" and skips geofencing.

Response `202 Accepted` (or `400` if nothing in the batch was accepted):

```json
{
  "accepted": 2,
  "rejected": 1,
  "errors": [{ "index": 2, "error": "telematics: vehicle_id is required" }],
  "alerts": [{ "vehicle_id": "van-7", "kind": "speeding", "severity": "warning",
               "message": "speed 112 km/h exceeds limit 100 km/h", "value": 112.4,
               "threshold": 100, "at": "2026-10-12T06:30:00Z" }],
  "transitions": [{ "zone": "depot", "vehicle_id": "van-7", "event": "exit",
                    "point": { "lat": 37.33, "lng": -121.89 }, "at": "...", "dwell_seconds": 540 }]
}
```

## Queries

| Endpoint | Returns |
|---|---|
| `GET /api/v1/fleet` | `{ summary: Summary, vehicles: Snapshot[] }` |
| `GET /api/v1/vehicles` | `Snapshot[]` sorted by vehicle ID |
| `GET /api/v1/vehicles/{id}?limit=50` | `{ snapshot, zones: string[], events: Event[] }`; `404` if unknown |
| `GET /api/v1/alerts?limit=50&severity=warning` | recent alerts, newest first; `severity` is a minimum |
| `GET /api/v1/zones` | `{ zones: ZoneSpec[], occupancy: { [zone]: vehicleId[] } }` |
| `GET /api/v1/events?after=<seq>&limit=100` | durable events with `seq > after`, oldest first, plus `truncated` and `last_seq` |
| `GET /api/v1/events?type=alert&limit=100` | newest events of one type |
| `GET /api/v1/stats` | engine counters (ingested, rejected, alerts, transitions, subscribers, drops, rules…) |
| `GET /healthz`, `GET /readyz` | `{ "status": "ok", "last_seq": n }` |
| `GET /metrics` | Prometheus text format (not JSON) |
| `GET /` | embedded dashboard (HTML) |

### Snapshot

```json
{
  "vehicle_id": "van-7", "window_start": "...", "window_end": "...", "samples": 300,
  "avg_speed": 48.2, "max_speed": 112.4, "idle_count": 12, "idle_ratio": 0.04,
  "distance_m": 4012.5, "fuel_used_pct": 1.5, "fuel_level": 41,
  "engine_on": true, "lat": 37.33, "lng": -121.89, "heading": 270
}
```

### Summary

```json
{ "vehicles": 8, "moving": 6, "idling": 1, "engine_off": 1, "avg_speed": 44.3,
  "total_distance_m": 5160, "low_fuel_count": 1, "low_fuel_cutoff": 15 }
```

## Live stream

### `GET /api/v1/stream?types=alert,geofence`

`text/event-stream`. Query `types` filters to a comma-separated subset of
`reading`, `alert`, `geofence` (default all).

Frames:

```
retry: 3000

id: 42
event: alert
data: {"seq":42,"at":"...","type":"alert","vehicle_id":"van-7","payload":{...Alert}}

event: reading
data: {"seq":0,"at":"...","type":"reading","vehicle_id":"van-7","payload":{...Reading}}

: ping
```

* Durable events (`alert`, `geofence`, and `reading` when `-persist-readings`
  is on) carry an `id:` equal to their sequence number. Non-durable readings
  have `seq: 0` and no `id:`.
* **Resume:** send `Last-Event-ID: <seq>` (browsers' `EventSource` does this
  automatically on reconnect) or `?after=<seq>`. Every durable event with a
  higher sequence number is replayed from the store before live delivery.
* If the requested position has aged out of in-memory retention the first
  frame is `event: gap` with `data: {"after": n, "oldest": m}`; clients should
  refresh their state via REST.
* A comment heartbeat (`: ping`) is sent every 15 s to keep proxies from
  closing idle connections.

## CORS

`Access-Control-Allow-Origin: *` by default (configurable with `-cors`), with
`OPTIONS` preflight handled for all routes. `Last-Event-ID` is an allowed
request header so browser clients can resume cross-origin.
