// Operational-insights logic for the mobile app. Turns raw alerts into the
// prioritized, actionable summary a field worker sees on their phone.
import type { Alert, VehicleSummary } from "./types";

// Rank vehicles by how urgently they need attention (critical > warning > info).
const SEVERITY_WEIGHT: Record<string, number> = {
  critical: 3,
  warning: 2,
  info: 1,
};

export function prioritizeAlerts(alerts: Alert[]): Alert[] {
  return [...alerts].sort(
    (a, b) => (SEVERITY_WEIGHT[b.severity] ?? 0) - (SEVERITY_WEIGHT[a.severity] ?? 0)
  );
}

// Group alerts per vehicle into a summary the dashboard can render.
export function summarize(alerts: Alert[]): VehicleSummary[] {
  const byVehicle = new Map<string, number>();
  for (const a of alerts) {
    byVehicle.set(a.vehicle_id, (byVehicle.get(a.vehicle_id) ?? 0) + 1);
  }
  return [...byVehicle.entries()].map(([vehicleId, alertCount]) => ({
    vehicleId,
    avgSpeed: 0,
    idleCount: 0,
    alertCount,
  }));
}
