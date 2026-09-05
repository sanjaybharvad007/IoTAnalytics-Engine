<div align="center">

# 🚚 IoTAnalytics-Engine

### A real-time IoT analytics & field-operations engine

*Sliding-window telematics · safety-alert rules · geofencing · durable operational storage*

<p>
<img src="https://img.shields.io/badge/Go-00ADD8?style=for-the-badge&logo=go&logoColor=white"/>
<img src="https://img.shields.io/badge/TypeScript-007ACC?style=for-the-badge&logo=typescript&logoColor=white"/>
</p>

</div>

---

## Resume summary

> **IoTAnalytics-Engine** — Go, TypeScript
>
> Engineered a real-time IoT field-operations analytics engine in Go with
> sliding-window telemetry aggregation computing rolling fleet metrics such as
> average speed and idle-time detection, geofencing with enter/exit alerts, and a
> composable safety-alert engine handling speeding, low-fuel, and threshold rules
> with severity-ranked alerts. Operational events are durably stored and pushed in
> real time to a TypeScript field-ops client for single-pane operational visibility.

Every claim above maps to a component in this repo.

---

## What it does: claim -> code

| Resume claim | Where it lives | What it really is |
|---|---|---|
| Sliding-window telemetry aggregation | `aggregation-go/` | Time-window that auto-evicts old readings; rolling avg speed + idle detection |
| Geofencing with enter/exit alerts | `geofence-go/` | Haversine distance zones; fires only on boundary crossings |
| Composable safety-alert engine | `alerts-go/` | Speeding / low-fuel / threshold rules, severity-ranked |
| Durable operational storage | `storage-go/` | Append-only log surviving restarts + live broadcast broker |
| Real-time push to field-ops client | `storage-go/` (broker) + `mobile-ts/` | SSE-style fan-out to a TypeScript client that prioritizes alerts |

---

## Architecture

```
 vehicles --telematics--> [ Sliding window ]   [ Alert engine ]   [ Geofence monitor ]
                          (rolling metrics)     (rules -> alerts)  (enter/exit zones)
                                    \                |                  /
                                     \               v                 /
                                      ----->  [ Durable store + live broker ]
                                                        |
                                                        v
                                          [ TypeScript field-ops client ]
                                          (severity-ranked, single pane)
```

---

## Run it

```bash
go run ./cmd/server            # telematics -> window + alerts + geofence, stored durably
go test ./...                  # all Go components tested

cd mobile-ts && npm install
npx tsc --noEmit src/types.ts src/insights.ts
```

Example output:
```
window size=61  avg_speed=61.7  idle=0
stored operational records: 33
```

---

## Highlights worth a closer look

**Geofencing (`geofence-go/`)** uses the haversine formula for true
great-circle distance, so circular zones are geographically accurate. A monitor
tracks each vehicle's inside/outside state per zone and emits an alert only on
the transition (enter or exit), not on every reading.

**Sliding window (`aggregation-go/`)** keeps only readings within the time span,
evicting aged-out data as new readings arrive, so metrics always reflect the
current window. It computes rolling average speed and idle detection
(engine on but not moving) in one pass.

---

## Structure

```
IoTAnalytics-Engine/
|- telematics-go/    telemetry reading model
|- aggregation-go/   sliding-window rolling metrics
|- alerts-go/        composable safety-alert rule engine
|- geofence-go/      haversine geofencing + enter/exit
|- storage-go/       durable append-only store + live broker
|- mobile-ts/        TypeScript field-ops client
|- cmd/server/       full engine demo
```

---

## Scope

The real, runnable analytics core of a field-ops platform. A production system
would add a managed datastore, a full React Native app, and a live telemetry
feed at scale; the durable local store and simulated stream here keep everything
runnable and testable on one machine.
