/**
 * Operational-insights logic for field-ops clients: turns raw alerts and
 * per-vehicle snapshots into the prioritised, actionable view a dispatcher
 * or driver sees on their screen. Pure functions, no I/O.
 */
import { SEVERITY_RANK, type Alert, type Severity, type Snapshot } from "./types.js";

/**
 * Rank alerts for display: most severe first, then most recent. Stable for
 * equal keys so the server's ordering is preserved as a final tiebreak.
 */
export function prioritizeAlerts(alerts: readonly Alert[]): Alert[] {
  return [...alerts].sort((a, b) => {
    const bySeverity = (SEVERITY_RANK[b.severity] ?? 0) - (SEVERITY_RANK[a.severity] ?? 0);
    if (bySeverity !== 0) return bySeverity;
    return Date.parse(b.at) - Date.parse(a.at);
  });
}

export interface VehicleSummary {
  vehicleId: string;
  avgSpeed: number;
  maxSpeed: number;
  idleRatio: number;
  fuelLevel: number;
  distanceKm: number;
  alertCount: number;
  highestSeverity: Severity | null;
  /** 0-100 score of how urgently this vehicle needs attention. */
  attention: number;
}

/**
 * Join alerts with the latest snapshot per vehicle into one row per vehicle,
 * sorted by attention score. Vehicles with alerts but no snapshot (e.g. a
 * client that only subscribed to alerts) still appear with zeroed metrics.
 */
export function summarize(alerts: readonly Alert[], snapshots: readonly Snapshot[] = []): VehicleSummary[] {
  const rows = new Map<string, VehicleSummary>();
  const row = (id: string): VehicleSummary => {
    let r = rows.get(id);
    if (!r) {
      r = {
        vehicleId: id, avgSpeed: 0, maxSpeed: 0, idleRatio: 0, fuelLevel: 0, distanceKm: 0,
        alertCount: 0, highestSeverity: null, attention: 0,
      };
      rows.set(id, r);
    }
    return r;
  };
  for (const s of snapshots) {
    const r = row(s.vehicle_id);
    r.avgSpeed = s.avg_speed;
    r.maxSpeed = s.max_speed;
    r.idleRatio = s.idle_ratio;
    r.fuelLevel = s.fuel_level;
    r.distanceKm = s.distance_m / 1000;
  }
  for (const a of alerts) {
    const r = row(a.vehicle_id);
    r.alertCount++;
    if (!r.highestSeverity || SEVERITY_RANK[a.severity] > SEVERITY_RANK[r.highestSeverity]) {
      r.highestSeverity = a.severity;
    }
  }
  for (const r of rows.values()) r.attention = attentionScore(r);
  return [...rows.values()].sort((a, b) => b.attention - a.attention || a.vehicleId.localeCompare(b.vehicleId));
}

/**
 * Attention score (0-100): a transparent heuristic combining the worst alert
 * severity, alert volume, fuel state and idling. It is intentionally simple
 * so operators can reason about why a vehicle is at the top of the list.
 */
export function attentionScore(r: Omit<VehicleSummary, "attention">): number {
  let score = 0;
  if (r.highestSeverity) score += SEVERITY_RANK[r.highestSeverity] * 20; // 20 / 40 / 60
  score += Math.min(15, r.alertCount * 3);
  if (r.fuelLevel > 0 && r.fuelLevel < 5) score += 20;
  else if (r.fuelLevel > 0 && r.fuelLevel < 15) score += 10;
  if (r.idleRatio > 0.5) score += 5;
  return Math.min(100, score);
}

/** Group alerts by kind with counts, most frequent first. */
export function alertBreakdown(alerts: readonly Alert[]): { kind: string; count: number; worst: Severity }[] {
  const by = new Map<string, { kind: string; count: number; worst: Severity }>();
  for (const a of alerts) {
    const e = by.get(a.kind) ?? { kind: a.kind, count: 0, worst: a.severity };
    e.count++;
    if (SEVERITY_RANK[a.severity] > SEVERITY_RANK[e.worst]) e.worst = a.severity;
    by.set(a.kind, e);
  }
  return [...by.values()].sort((a, b) => b.count - a.count || a.kind.localeCompare(b.kind));
}
