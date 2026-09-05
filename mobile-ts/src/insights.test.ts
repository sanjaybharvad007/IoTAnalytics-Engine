// Lightweight assertions (run with ts-node or tsc + node). Documents behavior.
import { prioritizeAlerts, summarize } from "./insights";
import type { Alert } from "./types";

const alerts: Alert[] = [
  { vehicle_id: "v1", kind: "low_fuel", severity: "info", message: "" },
  { vehicle_id: "v1", kind: "speeding", severity: "warning", message: "" },
  { vehicle_id: "v2", kind: "harsh_brake", severity: "critical", message: "" },
];

const ranked = prioritizeAlerts(alerts);
console.assert(ranked[0].severity === "critical", "critical should rank first");

const summary = summarize(alerts);
console.assert(summary.find((s) => s.vehicleId === "v1")?.alertCount === 2, "v1 has 2 alerts");
console.log("insights tests passed");
