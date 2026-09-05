// Types for the field-ops mobile client, mirroring the Go engine's JSON.
export interface TelematicsReading {
  vehicle_id: string;
  speed: number;
  fuel_level: number;
  engine_on: boolean;
  timestamp: string;
}

export interface Alert {
  vehicle_id: string;
  kind: "speeding" | "low_fuel" | string;
  severity: "info" | "warning" | "critical";
  message: string;
}

export interface VehicleSummary {
  vehicleId: string;
  avgSpeed: number;
  idleCount: number;
  alertCount: number;
}
