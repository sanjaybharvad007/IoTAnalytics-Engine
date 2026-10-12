import assert from "node:assert/strict";
import { test } from "node:test";
import { alertBreakdown, attentionScore, prioritizeAlerts, summarize } from "./insights.js";
import type { Alert, Snapshot } from "./types.js";

const alert = (over: Partial<Alert>): Alert => ({
  vehicle_id: "v1", kind: "speeding", severity: "warning", message: "", value: 0, threshold: 0,
  at: "2026-01-01T12:00:00Z", ...over,
});

const snapshot = (over: Partial<Snapshot>): Snapshot => ({
  vehicle_id: "v1", window_start: "2026-01-01T11:55:00Z", window_end: "2026-01-01T12:00:00Z", samples: 10,
  avg_speed: 40, max_speed: 60, idle_count: 0, idle_ratio: 0, distance_m: 2500, fuel_used_pct: 1,
  fuel_level: 50, engine_on: true, lat: 37.3, lng: -121.9, heading: 90, ...over,
});

const alerts: Alert[] = [
  alert({ vehicle_id: "v1", kind: "low_fuel", severity: "info", at: "2026-01-01T12:00:03Z" }),
  alert({ vehicle_id: "v1", kind: "speeding", severity: "warning", at: "2026-01-01T12:00:01Z" }),
  alert({ vehicle_id: "v2", kind: "harsh_braking", severity: "critical", at: "2026-01-01T12:00:00Z" }),
  alert({ vehicle_id: "v3", kind: "speeding", severity: "warning", at: "2026-01-01T12:00:02Z" }),
];

test("prioritizeAlerts orders by severity then recency without mutating input", () => {
  const copy = [...alerts];
  const ranked = prioritizeAlerts(alerts);
  assert.deepEqual(alerts, copy);
  assert.deepEqual(
    ranked.map((a) => `${a.vehicle_id}:${a.severity}`),
    ["v2:critical", "v3:warning", "v1:warning", "v1:info"],
  );
});

test("summarize joins alerts with real snapshot metrics and ranks by attention", () => {
  const rows = summarize(alerts, [
    snapshot({ vehicle_id: "v1", avg_speed: 55, max_speed: 110, fuel_level: 12 }),
    snapshot({ vehicle_id: "v2", avg_speed: 30 }),
    snapshot({ vehicle_id: "v4", avg_speed: 20, idle_ratio: 0.8 }), // no alerts
  ]);
  const byId = Object.fromEntries(rows.map((r) => [r.vehicleId, r]));
  assert.equal(rows.length, 4);
  assert.equal(byId.v1?.alertCount, 2);
  assert.equal(byId.v1?.avgSpeed, 55);
  assert.equal(byId.v1?.maxSpeed, 110);
  assert.equal(byId.v1?.highestSeverity, "warning");
  assert.equal(byId.v1?.distanceKm, 2.5);
  assert.equal(byId.v3?.alertCount, 1);
  assert.equal(byId.v3?.avgSpeed, 0, "alert-only vehicle has zero metrics, not fake ones");
  assert.equal(byId.v4?.alertCount, 0);
  // Critical v2 outranks v1 even though v1 has more alerts and low fuel.
  assert.equal(rows[0]?.vehicleId, "v2");
  assert.ok((byId.v1?.attention ?? 0) > (byId.v3?.attention ?? 0), "low fuel raises attention");
  assert.ok((byId.v4?.attention ?? 0) > 0, "heavy idling alone registers");
});

test("attentionScore is bounded and monotonic in severity", () => {
  const base = { vehicleId: "x", avgSpeed: 0, maxSpeed: 0, idleRatio: 0, fuelLevel: 50, distanceKm: 0, alertCount: 1 };
  const info = attentionScore({ ...base, highestSeverity: "info" });
  const warn = attentionScore({ ...base, highestSeverity: "warning" });
  const crit = attentionScore({ ...base, highestSeverity: "critical" });
  assert.ok(info < warn && warn < crit);
  assert.equal(attentionScore({ ...base, alertCount: 999, fuelLevel: 1, idleRatio: 1, highestSeverity: "critical" }), 100);
  assert.equal(attentionScore({ ...base, alertCount: 0, highestSeverity: null }), 0);
});

test("alertBreakdown counts kinds and tracks the worst severity", () => {
  const breakdown = alertBreakdown(alerts);
  assert.deepEqual(breakdown.map((b) => [b.kind, b.count, b.worst]), [
    ["speeding", 2, "warning"],
    ["harsh_braking", 1, "critical"],
    ["low_fuel", 1, "info"],
  ]);
});
