/**
 * Wire types mirroring the Go engine's JSON. Field names are snake_case to
 * match the server exactly, so payloads can be used without mapping.
 */

export type Severity = "info" | "warning" | "critical";

/** Numeric rank of a severity; higher is more urgent. */
export const SEVERITY_RANK: Readonly<Record<Severity, number>> = { info: 1, warning: 2, critical: 3 };

export interface Reading {
  vehicle_id: string;
  timestamp: string; // RFC 3339
  speed: number; // km/h
  fuel_level: number; // percent
  engine_on: boolean;
  lat: number;
  lng: number;
  heading?: number;
}

export interface Alert {
  vehicle_id: string;
  kind: string; // "speeding" | "low_fuel" | "harsh_braking" | "excessive_idle" | custom
  severity: Severity;
  message: string;
  value: number;
  threshold: number;
  at: string;
}

export type GeofenceEvent = "enter" | "exit";

export interface Transition {
  zone: string;
  vehicle_id: string;
  event: GeofenceEvent;
  point: { lat: number; lng: number };
  at: string;
  dwell_seconds?: number;
}

export interface Snapshot {
  vehicle_id: string;
  window_start: string;
  window_end: string;
  samples: number;
  avg_speed: number;
  max_speed: number;
  idle_count: number;
  idle_ratio: number;
  distance_m: number;
  fuel_used_pct: number;
  fuel_level: number;
  engine_on: boolean;
  lat: number;
  lng: number;
  heading: number;
}

export interface Summary {
  vehicles: number;
  moving: number;
  idling: number;
  engine_off: number;
  avg_speed: number;
  total_distance_m: number;
  low_fuel_count: number;
  low_fuel_cutoff: number;
}

export interface ZoneSpec {
  name: string;
  type: "circle" | "polygon";
  center?: { lat: number; lng: number };
  radius_m?: number;
  vertices?: { lat: number; lng: number }[];
}

export type EventType = "reading" | "alert" | "geofence";

/** A stored/streamed engine event. `seq` is 0 for non-durable readings. */
export interface EngineEvent<T = unknown> {
  seq: number;
  at: string;
  type: EventType;
  vehicle_id: string;
  payload: T;
}

export type ReadingEvent = EngineEvent<Reading> & { type: "reading" };
export type AlertEvent = EngineEvent<Alert> & { type: "alert" };
export type GeofenceEventRecord = EngineEvent<Transition> & { type: "geofence" };
export type AnyEvent = ReadingEvent | AlertEvent | GeofenceEventRecord;

export interface Stats {
  uptime_seconds: number;
  readings_ingested: number;
  readings_rejected: number;
  alerts_raised: number;
  geofence_transitions: number;
  vehicles_tracked: number;
  stored_events: number;
  last_seq: number;
  stream_subscribers: number;
  stream_dropped: number;
  rules: string[];
}

export interface IngestResponse {
  accepted: number;
  rejected: number;
  errors?: { index: number; error: string }[];
  alerts: Alert[];
  transitions: Transition[];
}

export interface FleetResponse {
  summary: Summary;
  vehicles: Snapshot[];
}

export interface VehicleResponse {
  snapshot: Snapshot;
  zones: string[];
  events: EngineEvent[];
}

export interface ZonesResponse {
  zones: ZoneSpec[];
  occupancy: Record<string, string[]>;
}

export interface EventsResponse {
  events: EngineEvent[];
  truncated: boolean;
  last_seq: number;
}
