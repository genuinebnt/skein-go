/**
 * A WebSocket that reconnects the way docs/api/websocket.md asks: the access
 * token rides in the subprotocol list (`bearer.<token>`, never the URL),
 * close codes decide between reconnecting with backoff, refreshing the token
 * first (4401), waiting the server's number of seconds (4429), or stopping
 * (4403, 4404, 4400, 1009, 1000).
 *
 * Heartbeats: the gateway pings every 30 s and browsers answer pings on
 * their own, so a dead connection surfaces as close 1001 or 1006 and is
 * retried. On top of that the socket follows the browser's online/offline
 * events (reconnecting at once when the network returns), and an optional
 * application heartbeat lets a caller refresh state the server forgets,
 * such as sync presence.
 */

import { backoffDelay, onClose, type SocketState } from "./policy";

export type WebSocketLike = {
  readonly readyState: number;
  binaryType: BinaryType;
  onopen: ((event: Event) => void) | null;
  onclose: ((event: CloseEvent) => void) | null;
  onerror: ((event: Event) => void) | null;
  onmessage: ((event: MessageEvent) => void) | null;
  send(data: string | ArrayBufferLike | Blob | ArrayBufferView): void;
  close(code?: number, reason?: string): void;
};

export type WebSocketFactory = (url: string, protocols: string[]) => WebSocketLike;

export type Timers = {
  setTimeout: (fn: () => void, ms: number) => unknown;
  clearTimeout: (handle: unknown) => void;
  setInterval: (fn: () => void, ms: number) => unknown;
  clearInterval: (handle: unknown) => void;
  now: () => number;
};

const realTimers: Timers = {
  setTimeout: (fn, ms) => globalThis.setTimeout(fn, ms),
  clearTimeout: (h) => globalThis.clearTimeout(h as ReturnType<typeof setTimeout>),
  setInterval: (fn, ms) => globalThis.setInterval(fn, ms),
  clearInterval: (h) => globalThis.clearInterval(h as ReturnType<typeof setInterval>),
  now: () => Date.now(),
};

export type SocketOptions = {
  url: string;
  /** skein.v1.proto or skein.v1.json. */
  subprotocol: string;
  accessToken: () => string | null;
  refresh: () => Promise<boolean>;
  create: WebSocketFactory;
  onOpen?: (socket: ReconnectingSocket) => void;
  onMessage: (data: string | ArrayBuffer) => void;
  onState?: (state: SocketState) => void;
  heartbeat?: { intervalMs: number; beat: (socket: ReconnectingSocket) => void };
  timers?: Timers;
  random?: () => number;
  /** Where online/offline events come from; window in the browser. */
  network?: Pick<EventTarget, "addEventListener" | "removeEventListener"> & { onLine?: boolean };
};

const OPEN = 1;

export class ReconnectingSocket {
  state: SocketState = { status: "idle" };
  private ws: WebSocketLike | null = null;
  private attempt = 0;
  private retryTimer: unknown = null;
  private beatTimer: unknown = null;
  private stopped = false;
  private readonly timers: Timers;
  private readonly random: () => number;

  constructor(private readonly options: SocketOptions) {
    this.timers = options.timers ?? realTimers;
    this.random = options.random ?? Math.random;
  }

  start(): void {
    this.stopped = false;
    this.options.network?.addEventListener("online", this.handleOnline);
    this.options.network?.addEventListener("offline", this.handleOffline);
    if (this.options.network?.onLine === false) {
      this.setState({ status: "offline" });
      return;
    }
    this.connect();
  }

  /** Closes for good (the user left the note or signed out). */
  stop(reason: "finished" | "signed-out" = "finished"): void {
    this.stopped = true;
    this.options.network?.removeEventListener("online", this.handleOnline);
    this.options.network?.removeEventListener("offline", this.handleOffline);
    this.clearTimers();
    const ws = this.ws;
    this.ws = null;
    if (ws) {
      // Nothing from this connection may reach an owner that has stopped (and freed its state).
      ws.onopen = null;
      ws.onmessage = null;
      ws.onerror = null;
      ws.onclose = null;
      ws.close(1000, "client closed");
    }
    this.setState({ status: "stopped", reason });
  }

  /** Sends when open; returns false otherwise, so callers keep the frame queued. */
  send(data: string | Uint8Array): boolean {
    if (!this.ws || this.ws.readyState !== OPEN) return false;
    this.ws.send(data);
    return true;
  }

  get isOpen(): boolean {
    return this.state.status === "open";
  }

  /** Reconnect now, skipping any backoff (the user pressed "retry"). */
  retryNow(): void {
    if (this.stopped || this.state.status === "open" || this.state.status === "connecting") return;
    this.clearTimers();
    this.connect();
  }

  private connect(): void {
    if (this.stopped) return;
    const token = this.options.accessToken();
    const protocols = token ? [this.options.subprotocol, `bearer.${token}`] : [this.options.subprotocol];
    this.setState({ status: "connecting", attempt: this.attempt });
    let ws: WebSocketLike;
    try {
      ws = this.options.create(this.options.url, protocols);
    } catch {
      this.scheduleReconnect(backoffDelay(this.attempt++, this.random));
      return;
    }
    ws.binaryType = "arraybuffer";
    this.ws = ws;
    ws.onopen = () => {
      this.attempt = 0;
      this.setState({ status: "open" });
      this.startHeartbeat();
      this.options.onOpen?.(this);
    };
    ws.onmessage = (event) => this.options.onMessage(event.data as string | ArrayBuffer);
    ws.onerror = () => {
      // A close event always follows; the decision is made there.
    };
    ws.onclose = (event) => this.handleClose(event.code, event.reason);
  }

  private handleClose(code: number, reason: string): void {
    this.ws = null;
    this.stopHeartbeat();
    if (this.stopped) return;
    if (this.options.network?.onLine === false) {
      this.setState({ status: "offline" });
      return;
    }
    const action = onClose(code, reason, this.attempt, this.random);
    switch (action.kind) {
      case "stop":
        this.stopped = true;
        this.setState({ status: "stopped", reason: action.reason });
        return;
      case "refresh":
        void this.options.refresh().then((ok) => {
          if (this.stopped) return;
          if (!ok) {
            this.stopped = true;
            this.setState({ status: "stopped", reason: "signed-out" });
            return;
          }
          // After a refresh the docs say reconnect at once: sync costs one hello.
          this.connect();
        });
        return;
      case "reconnect":
        this.attempt += 1;
        this.scheduleReconnect(action.delayMs);
    }
  }

  private scheduleReconnect(delayMs: number): void {
    this.setState({ status: "waiting", attempt: this.attempt, retryAt: this.timers.now() + delayMs });
    this.retryTimer = this.timers.setTimeout(() => {
      this.retryTimer = null;
      this.connect();
    }, delayMs);
  }

  private readonly handleOnline = () => {
    if (this.stopped) return;
    this.attempt = 0;
    this.clearTimers();
    if (this.state.status !== "open" && this.state.status !== "connecting") this.connect();
  };

  private readonly handleOffline = () => {
    if (this.stopped) return;
    this.clearTimers();
    const ws = this.ws;
    this.ws = null;
    if (ws) {
      ws.onclose = null;
      ws.close(1000, "offline");
    }
    this.setState({ status: "offline" });
  };

  private startHeartbeat(): void {
    const hb = this.options.heartbeat;
    if (!hb) return;
    this.beatTimer = this.timers.setInterval(() => hb.beat(this), hb.intervalMs);
  }

  private stopHeartbeat(): void {
    if (this.beatTimer !== null) this.timers.clearInterval(this.beatTimer);
    this.beatTimer = null;
  }

  private clearTimers(): void {
    if (this.retryTimer !== null) this.timers.clearTimeout(this.retryTimer);
    this.retryTimer = null;
    this.stopHeartbeat();
  }

  private setState(state: SocketState): void {
    this.state = state;
    this.options.onState?.(state);
  }
}
