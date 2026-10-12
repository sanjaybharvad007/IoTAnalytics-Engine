/**
 * A minimal, spec-conformant Server-Sent Events frame parser. It is a pure
 * function of the incoming text so it can be unit-tested without sockets and
 * reused by the FeedClient on any platform that has fetch + ReadableStream.
 */

export interface SseFrame {
  id?: string;
  event: string; // defaults to "message" per the spec
  data: string;
  retry?: number;
}

export class SseParser {
  private buffer = "";
  private id: string | undefined;
  private event = "";
  private data: string[] = [];
  private retry: number | undefined;

  /** Feed a chunk of text; returns every complete frame it terminated. */
  push(chunk: string): SseFrame[] {
    this.buffer += chunk;
    const frames: SseFrame[] = [];
    let nl: number;
    // Handle CRLF, CR and LF line endings.
    while ((nl = this.buffer.search(/\r\n|\r|\n/)) !== -1) {
      const line = this.buffer.slice(0, nl);
      const sepLen = this.buffer.startsWith("\r\n", nl) ? 2 : 1;
      this.buffer = this.buffer.slice(nl + sepLen);
      const frame = this.line(line);
      if (frame) frames.push(frame);
    }
    return frames;
  }

  private line(line: string): SseFrame | undefined {
    if (line === "") return this.dispatch();
    if (line.startsWith(":")) return undefined; // comment / heartbeat
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    switch (field) {
      case "event":
        this.event = value;
        break;
      case "data":
        this.data.push(value);
        break;
      case "id":
        if (!value.includes("\0")) this.id = value;
        break;
      case "retry": {
        const n = Number(value);
        if (Number.isInteger(n) && n >= 0) this.retry = n;
        break;
      }
      default:
        break; // unknown fields are ignored per spec
    }
    return undefined;
  }

  private dispatch(): SseFrame | undefined {
    const hasData = this.data.length > 0;
    const frame: SseFrame | undefined =
      hasData || this.event
        ? { event: this.event || "message", data: this.data.join("\n") }
        : undefined;
    if (frame) {
      if (this.id !== undefined) frame.id = this.id;
      if (this.retry !== undefined) frame.retry = this.retry;
    }
    this.event = "";
    this.data = [];
    // Note: per spec the last event ID persists across frames; retry is a
    // connection-level setting. We surface retry on every frame for callers.
    return frame;
  }

  /** The most recent event ID seen, for Last-Event-ID on reconnect. */
  get lastEventId(): string | undefined {
    return this.id;
  }
}
