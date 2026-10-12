import type { FetchLike } from "./client.js";
import { SseParser } from "./sse.js";
import type { Alert, AnyEvent, EngineEvent, EventType, Reading, Transition } from "./types.js";

export type FeedStatus = "connecting" | "live" | "reconnecting" | "closed";

export interface FeedHandlers {
  reading?: (event: EngineEvent<Reading>) => void;
  alert?: (event: EngineEvent<Alert>) => void;
  geofence?: (event: EngineEvent<Transition>) => void;
  /** The server could not replay from our last seq; do a full refresh. */
  gap?: (info: { after: number; oldest: number }) => void;
  status?: (status: FeedStatus, attempt: number) => void;
  error?: (err: unknown) => void;
}

export interface FeedOptions {
  baseUrl: string;
  /** Subscribe to a subset of event types (default: all). */
  types?: EventType[];
  /** Resume from this sequence number on first connect. */
  after?: number;
  fetch?: FetchLike;
  headers?: Record<string, string>;
  /** Backoff bounds for reconnects in ms (defaults 500 .. 15 000). */
  minBackoffMs?: number;
  maxBackoffMs?: number;
  /** Give up after this many consecutive failures (default: never). */
  maxAttempts?: number;
}

/**
 * FeedClient consumes the engine's SSE stream using fetch + ReadableStream,
 * so it works identically in browsers, React Native (with a streaming fetch
 * polyfill) and Node 18+. It tracks the last durable sequence number and
 * sends it as Last-Event-ID on reconnect, so alerts are never lost across
 * network blips; exponential backoff with jitter avoids thundering herds.
 */
export class FeedClient {
  private readonly opts: Required<Pick<FeedOptions, "baseUrl" | "minBackoffMs" | "maxBackoffMs">> & FeedOptions;
  private readonly fetchImpl: FetchLike;
  private handlers: FeedHandlers = {};
  private controller: AbortController | undefined;
  private reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
  private closed = false;
  private attempt = 0;
  private _lastSeq: number;
  private _status: FeedStatus = "closed";
  private retryHint: number | undefined;

  constructor(opts: FeedOptions) {
    this.opts = { minBackoffMs: 500, maxBackoffMs: 15_000, ...opts, baseUrl: opts.baseUrl.replace(/\/+$/, "") };
    this.fetchImpl = opts.fetch ?? ((input, init) => fetch(input, init));
    this._lastSeq = opts.after ?? 0;
  }

  /** Last durable sequence number delivered (0 if none yet). */
  get lastSeq(): number {
    return this._lastSeq;
  }

  get status(): FeedStatus {
    return this._status;
  }

  on(handlers: FeedHandlers): this {
    this.handlers = { ...this.handlers, ...handlers };
    return this;
  }

  /** Start consuming. Resolves when the feed is closed. */
  async start(): Promise<void> {
    this.closed = false;
    while (!this.closed) {
      this.setStatus(this.attempt === 0 ? "connecting" : "reconnecting");
      try {
        await this.connectOnce();
        if (this.closed) break;
        // Server closed the stream cleanly: reconnect promptly.
        this.attempt++;
      } catch (err) {
        if (this.closed) break;
        this.attempt++;
        this.handlers.error?.(err);
      }
      if (this.opts.maxAttempts !== undefined && this.attempt >= this.opts.maxAttempts) {
        this.close();
        break;
      }
      await sleep(this.backoff());
    }
    this.setStatus("closed");
  }

  /** Stop consuming and abort any open connection. */
  close(): void {
    this.closed = true;
    this.controller?.abort();
    // Also cancel the reader directly: some fetch implementations (and test
    // doubles) do not propagate the abort signal into an already-open body.
    void this.reader?.cancel().catch(() => undefined);
  }

  private url(): string {
    const q = new URLSearchParams();
    if (this.opts.types?.length) q.set("types", this.opts.types.join(","));
    const qs = q.toString();
    return `${this.opts.baseUrl}/api/v1/stream${qs ? `?${qs}` : ""}`;
  }

  private async connectOnce(): Promise<void> {
    this.controller = new AbortController();
    const headers: Record<string, string> = { Accept: "text/event-stream", ...(this.opts.headers ?? {}) };
    if (this._lastSeq > 0) headers["Last-Event-ID"] = String(this._lastSeq);

    const res = await this.fetchImpl(this.url(), { headers, signal: this.controller.signal });
    if (!res.ok) throw new Error(`stream: HTTP ${res.status}`);
    if (!res.body) throw new Error("stream: response has no body");

    this.attempt = 0;
    this.setStatus("live");
    const parser = new SseParser();
    const reader = res.body.getReader();
    this.reader = reader;
    const decoder = new TextDecoder();
    try {
      for (;;) {
        const { value, done } = await reader.read();
        if (done || this.closed) return;
        for (const frame of parser.push(decoder.decode(value, { stream: true }))) {
          if (frame.retry !== undefined) this.retryHint = frame.retry;
          this.dispatch(frame.event, frame.data);
          if (this.closed) return;
        }
      }
    } finally {
      this.reader = undefined;
      reader.releaseLock();
    }
  }

  private dispatch(event: string, data: string): void {
    if (!data) return;
    let parsed: unknown;
    try {
      parsed = JSON.parse(data);
    } catch (err) {
      this.handlers.error?.(err);
      return;
    }
    if (event === "gap") {
      this.handlers.gap?.(parsed as { after: number; oldest: number });
      return;
    }
    const ev = parsed as AnyEvent;
    if (typeof ev.seq === "number" && ev.seq > this._lastSeq) this._lastSeq = ev.seq;
    switch (ev.type) {
      case "reading":
        this.handlers.reading?.(ev);
        break;
      case "alert":
        this.handlers.alert?.(ev);
        break;
      case "geofence":
        this.handlers.geofence?.(ev);
        break;
      default:
        break;
    }
  }

  private backoff(): number {
    const base = this.retryHint ?? this.opts.minBackoffMs;
    const exp = Math.min(this.opts.maxBackoffMs, base * 2 ** Math.max(0, this.attempt - 1));
    return Math.round(exp * (0.5 + Math.random() * 0.5)); // full jitter
  }

  private setStatus(s: FeedStatus): void {
    if (s === this._status) return;
    this._status = s;
    this.handlers.status?.(s, this.attempt);
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}
