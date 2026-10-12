import assert from "node:assert/strict";
import { test } from "node:test";
import { FleetStore } from "./fleet-store.js";
import type { Alert, AnyEvent, Reading, Snapshot, Transition } from "./types.js";

const reading = (id: string, at: string): Reading => ({
  vehicle_id: id, timestamp: at, speed: 40, fuel_level: 50, engine_on: true, lat: 37.3, lng: -121.9,
});

const alertEv = (seq: number, id: string, severity: Alert["severity"], at: string): AnyEvent => ({
  seq, at, type: "alert", vehicle_id: id,
  payload: { vehicle_id: id, kind: "speeding", severity, message: "", value: 0, threshold: 0, at },
});

const geoEv = (seq: number, id: string, event: Transition["event"], zone: string): AnyEvent => ({
  seq, at: "2026-01-01T12:00:00Z", type: "geofence", vehicle_id: id,
  payload: { zone, vehicle_id: id, event, point: { lat: 0, lng: 0 }, at: "2026-01-01T12:00:00Z" },
});

test("folds readings, alerts and geofence events into per-vehicle state", () => {
  const store = new FleetStore();
  let notified = 0;
  const unsubscribe = store.subscribe(() => notified++);

  store.apply({ seq: 0, at: "t", type: "reading", vehicle_id: "v1", payload: reading("v1", "2026-01-01T12:00:00Z") });
  store.apply(alertEv(1, "v1", "warning", "2026-01-01T12:00:01Z"));
  store.apply(geoEv(2, "v1", "enter", "depot"));
  store.apply(geoEv(3, "v1", "enter", "downtown"));
  store.apply(geoEv(4, "v1", "exit", "depot"));

  const v1 = store.state.vehicles.get("v1");
  assert.ok(v1);
  assert.equal(v1.reading?.speed, 40);
  assert.equal(v1.lastAlertAt, "2026-01-01T12:00:01Z");
  assert.deepEqual([...v1.zones], ["downtown"]);
  assert.equal(store.state.lastSeq, 4);
  assert.equal(store.state.alerts.length, 1);
  assert.equal(store.state.transitions.length, 3);
  assert.equal(notified, 5);
  unsubscribe();
  store.apply(alertEv(5, "v1", "info", "2026-01-01T12:00:02Z"));
  assert.equal(notified, 5, "unsubscribed listener must not fire");
});

test("bounds history and keeps newest first", () => {
  const store = new FleetStore({ maxHistory: 3 });
  for (let i = 1; i <= 6; i++) store.apply(alertEv(i, "v1", "info", `2026-01-01T12:00:0${i}Z`));
  assert.equal(store.state.alerts.length, 3);
  assert.equal(store.state.alerts[0]?.at, "2026-01-01T12:00:06Z");
});

test("merges REST snapshots, occupancy and alert history without duplicates", () => {
  const store = new FleetStore();
  const snap: Snapshot = {
    vehicle_id: "v2", window_start: "a", window_end: "2026-01-01T12:00:00Z", samples: 5, avg_speed: 70, max_speed: 90,
    idle_count: 0, idle_ratio: 0, distance_m: 1000, fuel_used_pct: 0, fuel_level: 3, engine_on: true, lat: 0, lng: 0, heading: 0,
  };
  store.applySnapshots([snap]);
  store.applyOccupancy({ depot: ["v2"], yard: [] });
  const a = alertEv(1, "v2", "critical", "2026-01-01T12:00:00Z").payload as Alert;
  store.applyAlertHistory([a]);
  store.applyAlertHistory([a]); // idempotent
  assert.equal(store.state.alerts.length, 1);
  assert.deepEqual([...(store.state.vehicles.get("v2")?.zones ?? [])], ["depot"]);

  const rows = store.vehicleSummaries();
  assert.equal(rows[0]?.vehicleId, "v2");
  assert.equal(rows[0]?.avgSpeed, 70);
  assert.equal(rows[0]?.highestSeverity, "critical");
  assert.ok((rows[0]?.attention ?? 0) >= 80, "critical + near-empty tank should score high");
});

test("prioritizedAlerts and staleVehicles selectors", () => {
  const store = new FleetStore();
  store.apply(alertEv(1, "v1", "info", "2026-01-01T12:00:05Z"));
  store.apply(alertEv(2, "v2", "critical", "2026-01-01T12:00:01Z"));
  assert.deepEqual(store.prioritizedAlerts().map((a) => a.vehicle_id), ["v2", "v1"]);
  assert.deepEqual(store.prioritizedAlerts(1).map((a) => a.vehicle_id), ["v2"]);

  const now = Date.parse("2026-01-01T12:10:00Z");
  store.apply({ seq: 0, at: "t", type: "reading", vehicle_id: "v1", payload: reading("v1", "2026-01-01T12:09:50Z") });
  store.apply({ seq: 0, at: "t", type: "reading", vehicle_id: "v3", payload: reading("v3", "2026-01-01T12:00:00Z") });
  assert.deepEqual(store.staleVehicles(60_000, now), ["v2", "v3"]);
});
