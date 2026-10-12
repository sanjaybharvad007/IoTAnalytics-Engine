import type {
  Alert,
  EventsResponse,
  EventType,
  FleetResponse,
  IngestResponse,
  Reading,
  Severity,
  Snapshot,
  Stats,
  VehicleResponse,
  ZonesResponse,
} from "./types.js";

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

export interface ClientOptions {
  /** Base URL of the engine, e.g. "http://localhost:8080". */
  baseUrl: string;
  /** fetch implementation (defaults to the global fetch). Injectable for tests. */
  fetch?: FetchLike;
  /** Extra headers sent with every request (auth tokens, tenant IDs, ...). */
  headers?: Record<string, string>;
  /** Per-request timeout in milliseconds (default 10 000). */
  timeoutMs?: number;
}

/** Raised for non-2xx responses, carrying the server's error message. */
export class ApiError extends Error {
  constructor(
    public readonly status: number,
    message: string,
    public readonly path: string,
  ) {
    super(`${status} ${path}: ${message}`);
    this.name = "ApiError";
  }
}

/** Typed REST client for the engine's /api/v1 endpoints. */
export class EngineClient {
  private readonly baseUrl: string;
  private readonly fetchImpl: FetchLike;
  private readonly headers: Record<string, string>;
  private readonly timeoutMs: number;

  constructor(opts: ClientOptions) {
    this.baseUrl = opts.baseUrl.replace(/\/+$/, "");
    this.fetchImpl = opts.fetch ?? ((input, init) => fetch(input, init));
    this.headers = opts.headers ?? {};
    this.timeoutMs = opts.timeoutMs ?? 10_000;
  }

  /** Ingest one reading or a batch. Accepted readings may raise alerts and transitions. */
  ingest(readings: Reading | Reading[]): Promise<IngestResponse> {
    return this.request<IngestResponse>("POST", "/api/v1/telemetry", readings);
  }

  fleet(): Promise<FleetResponse> {
    return this.request("GET", "/api/v1/fleet");
  }

  vehicles(): Promise<Snapshot[]> {
    return this.request("GET", "/api/v1/vehicles");
  }

  vehicle(id: string, limit = 50): Promise<VehicleResponse> {
    return this.request("GET", `/api/v1/vehicles/${encodeURIComponent(id)}?limit=${limit}`);
  }

  alerts(opts: { limit?: number; severity?: Severity } = {}): Promise<Alert[]> {
    const q = new URLSearchParams();
    if (opts.limit !== undefined) q.set("limit", String(opts.limit));
    if (opts.severity) q.set("severity", opts.severity);
    const qs = q.toString();
    return this.request("GET", `/api/v1/alerts${qs ? `?${qs}` : ""}`);
  }

  zones(): Promise<ZonesResponse> {
    return this.request("GET", "/api/v1/zones");
  }

  /** Durable events. Pass `after` to page forward by sequence number. */
  events(opts: { after?: number; limit?: number; type?: EventType } = {}): Promise<EventsResponse> {
    const q = new URLSearchParams();
    if (opts.after !== undefined) q.set("after", String(opts.after));
    if (opts.limit !== undefined) q.set("limit", String(opts.limit));
    if (opts.type) q.set("type", opts.type);
    const qs = q.toString();
    return this.request("GET", `/api/v1/events${qs ? `?${qs}` : ""}`);
  }

  stats(): Promise<Stats> {
    return this.request("GET", "/api/v1/stats");
  }

  health(): Promise<{ status: string; last_seq: number }> {
    return this.request("GET", "/healthz");
  }

  private async request<T>(method: "GET" | "POST", path: string, body?: unknown): Promise<T> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    try {
      const init: RequestInit = {
        method,
        headers: { Accept: "application/json", ...this.headers },
        signal: controller.signal,
      };
      if (body !== undefined) {
        init.headers = { ...(init.headers as Record<string, string>), "Content-Type": "application/json" };
        init.body = JSON.stringify(body);
      }
      const res = await this.fetchImpl(this.baseUrl + path, init);
      const text = await res.text();
      if (!res.ok) {
        let message = text || res.statusText;
        try {
          const parsed = JSON.parse(text) as { error?: string };
          if (parsed.error) message = parsed.error;
        } catch {
          /* non-JSON error body */
        }
        throw new ApiError(res.status, message, path);
      }
      return (text ? JSON.parse(text) : undefined) as T;
    } finally {
      clearTimeout(timer);
    }
  }
}
