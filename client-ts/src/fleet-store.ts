import { prioritizeAlerts, summarize, type VehicleSummary } from "./insights.js";
import type { Alert, AnyEvent, Reading, Snapshot, Transition } from "./types.js";

export interface VehicleState {
  vehicleId: string;
  /** Latest raw reading from the live stream, if any. */
  reading?: Reading;
  /** Latest rolling snapshot from the REST API, if any. */
  snapshot?: Snapshot;
  /** Zones the vehicle is currently inside (maintained from geofence events). */
  zones: Set<string>;
  lastAlertAt?: string;
}

export interface FleetState {
  vehicles: Map<string, VehicleState>;
  alerts: Alert[]; // newest first
  transitions: Transition[]; // newest first
  lastSeq: number;
}

export interface FleetStoreOptions {
  /** Maximum alerts/transitions retained in memory (default 500). */
  maxHistory?: number;
}

type Listener = (state: FleetState) => void;

/**
 * FleetStore is a small, framework-agnostic reducer that folds stream events
 * and REST snapshots into one consistent fleet picture. Subscribe from React,
 * Vue, a React Native screen or a plain DOM renderer; selectors derive the
 * prioritised views.
 */
export class FleetStore {
  private readonly max: number;
  private readonly listeners = new Set<Listener>();
  readonly state: FleetState = { vehicles: new Map(), alerts: [], transitions: [], lastSeq: 0 };

  constructor(opts: FleetStoreOptions = {}) {
    this.max = opts.maxHistory ?? 500;
  }

  subscribe(fn: Listener): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  /** Apply one stream event. */
  apply(event: AnyEvent): void {
    if (event.seq > this.state.lastSeq) this.state.lastSeq = event.seq;
    const v = this.vehicle(event.vehicle_id);
    switch (event.type) {
      case "reading":
        v.reading = event.payload;
        break;
      case "alert":
        v.lastAlertAt = event.payload.at;
        this.state.alerts.unshift(event.payload);
        if (this.state.alerts.length > this.max) this.state.alerts.length = this.max;
        break;
      case "geofence":
        if (event.payload.event === "enter") v.zones.add(event.payload.zone);
        else v.zones.delete(event.payload.zone);
        this.state.transitions.unshift(event.payload);
        if (this.state.transitions.length > this.max) this.state.transitions.length = this.max;
        break;
    }
    this.emit();
  }

  /** Merge REST snapshots (e.g. from EngineClient.fleet()). */
  applySnapshots(snapshots: readonly Snapshot[]): void {
    for (const s of snapshots) this.vehicle(s.vehicle_id).snapshot = s;
    this.emit();
  }

  /** Replace zone membership from GET /api/v1/zones occupancy. */
  applyOccupancy(occupancy: Record<string, string[]>): void {
    for (const v of this.state.vehicles.values()) v.zones.clear();
    for (const [zone, ids] of Object.entries(occupancy)) {
      for (const id of ids) this.vehicle(id).zones.add(zone);
    }
    this.emit();
  }

  /** Seed alert history from GET /api/v1/alerts (newest first). */
  applyAlertHistory(alerts: readonly Alert[]): void {
    const seen = new Set(this.state.alerts.map(alertKey));
    for (const a of alerts) {
      if (!seen.has(alertKey(a))) this.state.alerts.push(a);
    }
    this.state.alerts = prioritizeByTime(this.state.alerts).slice(0, this.max);
    this.emit();
  }

  // ---- selectors ------------------------------------------------------------

  /** Alerts ordered for display: severity first, then recency. */
  prioritizedAlerts(limit = 50): Alert[] {
    return prioritizeAlerts(this.state.alerts).slice(0, limit);
  }

  /** One row per vehicle ordered by attention score. */
  vehicleSummaries(): VehicleSummary[] {
    const snapshots = [...this.state.vehicles.values()].flatMap((v) => (v.snapshot ? [v.snapshot] : []));
    return summarize(this.state.alerts, snapshots);
  }

  /** Vehicles that have not reported within `staleMs` of `now`. */
  staleVehicles(staleMs: number, now = Date.now()): string[] {
    const out: string[] = [];
    for (const v of this.state.vehicles.values()) {
      const last = v.reading?.timestamp ?? v.snapshot?.window_end;
      if (!last || now - Date.parse(last) > staleMs) out.push(v.vehicleId);
    }
    return out.sort();
  }

  private vehicle(id: string): VehicleState {
    let v = this.state.vehicles.get(id);
    if (!v) {
      v = { vehicleId: id, zones: new Set() };
      this.state.vehicles.set(id, v);
    }
    return v;
  }

  private emit(): void {
    for (const fn of this.listeners) fn(this.state);
  }
}

function alertKey(a: Alert): string {
  return `${a.vehicle_id}|${a.kind}|${a.at}`;
}

function prioritizeByTime(alerts: Alert[]): Alert[] {
  return alerts.sort((a, b) => Date.parse(b.at) - Date.parse(a.at));
}
