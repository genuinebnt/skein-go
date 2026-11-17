import { describe, expect, it, vi } from "vitest";
import { BACKOFF_CAP_MS, backoffDelay, onClose } from "./policy";
import { ReconnectingSocket, type Timers, type WebSocketLike } from "./socket";

describe("close policy", () => {
  it.each([
    [1000, { kind: "stop", reason: "finished" }],
    [4401, { kind: "refresh" }],
    [4403, { kind: "stop", reason: "forbidden" }],
    [4404, { kind: "stop", reason: "not-found" }],
    [4400, { kind: "stop", reason: "bad-request" }],
    [1009, { kind: "stop", reason: "too-big" }],
  ])("close %i -> %o", (code, action) => {
    expect(onClose(code, "", 0, () => 0.5)).toEqual(action);
  });

  it.each([1001, 1006, 1011, 1012, 1013])("close %i reconnects with backoff", (code) => {
    expect(onClose(code, "", 0, () => 0).kind).toBe("reconnect");
  });

  it("waits the seconds in a 4429 reason, plus under a second of jitter", () => {
    const action = onClose(4429, "7", 0, () => 0.5);
    expect(action).toEqual({ kind: "reconnect", delayMs: 7500 });
  });

  it("backs off from 1 s, doubling, capped at 30 s, never below half the base", () => {
    expect(backoffDelay(0, () => 0)).toBe(500);
    expect(backoffDelay(0, () => 1)).toBe(1000);
    expect(backoffDelay(3, () => 1)).toBe(8000);
    expect(backoffDelay(20, () => 1)).toBe(BACKOFF_CAP_MS);
    expect(backoffDelay(20, () => 0)).toBe(BACKOFF_CAP_MS / 2);
  });
});

class FakeSocket implements WebSocketLike {
  readyState = 0;
  binaryType: BinaryType = "blob";
  onopen: ((e: Event) => void) | null = null;
  onclose: ((e: CloseEvent) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  sent: unknown[] = [];
  constructor(
    readonly url: string,
    readonly protocols: string[],
  ) {}
  send(data: unknown) {
    this.sent.push(data);
  }
  close() {
    this.readyState = 3;
  }
  open() {
    this.readyState = 1;
    this.onopen?.(new Event("open"));
  }
  drop(code: number, reason = "") {
    this.readyState = 3;
    this.onclose?.({ code, reason } as CloseEvent);
  }
}

function harness(options: { refresh?: () => Promise<boolean>; onOpen?: () => void; onMessage?: (data: string | ArrayBuffer) => void } = {}) {
  const sockets: FakeSocket[] = [];
  const pending: { fn: () => void; at: number }[] = [];
  let now = 0;
  const timers: Timers = {
    setTimeout: (fn, ms) => {
      const t = { fn, at: now + ms };
      pending.push(t);
      return t;
    },
    clearTimeout: (h) => {
      const i = pending.indexOf(h as (typeof pending)[number]);
      if (i >= 0) pending.splice(i, 1);
    },
    setInterval: () => 0,
    clearInterval: () => {},
    now: () => now,
  };
  const advance = (ms: number) => {
    now += ms;
    for (const t of [...pending].sort((a, b) => a.at - b.at)) {
      if (t.at <= now) {
        pending.splice(pending.indexOf(t), 1);
        t.fn();
      }
    }
  };
  const network = new EventTarget() as EventTarget & { onLine: boolean };
  network.onLine = true;
  const states: string[] = [];
  const socket = new ReconnectingSocket({
    url: "wss://gw/v1/notes/n/sync",
    subprotocol: "skein.v1.proto",
    accessToken: () => "tok",
    refresh: options.refresh ?? (async () => true),
    create: (url, protocols) => {
      const s = new FakeSocket(url, protocols);
      sockets.push(s);
      return s;
    },
    onMessage: options.onMessage ?? (() => {}),
    onOpen: options.onOpen,
    onState: (s) => states.push(s.status),
    timers,
    random: () => 1,
    network,
  });
  return { socket, sockets, advance, network, states, last: () => sockets[sockets.length - 1]! };
}

describe("ReconnectingSocket", () => {
  it("offers the token as a bearer subprotocol after the real one, never in the URL", () => {
    const h = harness();
    h.socket.start();
    expect(h.last().protocols).toEqual(["skein.v1.proto", "bearer.tok"]);
    expect(h.last().url).not.toContain("tok");
  });

  it("reconnects after a dropped connection with growing backoff, and resets it once open", () => {
    const h = harness();
    h.socket.start();
    h.last().drop(1006);
    h.advance(999);
    expect(h.sockets).toHaveLength(1);
    h.advance(1);
    expect(h.sockets).toHaveLength(2);
    h.last().drop(1012);
    h.advance(1999);
    expect(h.sockets).toHaveLength(2);
    h.advance(1);
    expect(h.sockets).toHaveLength(3);
    h.last().open();
    h.last().drop(1001);
    h.advance(1000);
    expect(h.sockets).toHaveLength(4);
  });

  it("refreshes the token on 4401 and reconnects at once", async () => {
    const refresh = vi.fn(async () => true);
    const h = harness({ refresh });
    h.socket.start();
    h.last().open();
    h.last().drop(4401);
    await Promise.resolve();
    await Promise.resolve();
    expect(refresh).toHaveBeenCalledOnce();
    expect(h.sockets).toHaveLength(2);
  });

  it("stops as signed out when the refresh fails", async () => {
    const h = harness({ refresh: async () => false });
    h.socket.start();
    h.last().drop(4401);
    await Promise.resolve();
    await Promise.resolve();
    expect(h.socket.state).toEqual({ status: "stopped", reason: "signed-out" });
  });

  it("lets nothing from a stopped connection reach its owner, even an open that arrives late", () => {
    const onOpen = vi.fn();
    const onMessage = vi.fn();
    const h = harness({ onOpen, onMessage });
    h.socket.start();
    const connecting = h.last();
    h.socket.stop();
    connecting.open();
    connecting.onmessage?.({ data: "late" } as MessageEvent);
    expect(onOpen).not.toHaveBeenCalled();
    expect(onMessage).not.toHaveBeenCalled();
    expect(h.socket.state).toEqual({ status: "stopped", reason: "finished" });
  });

  it("stops for good on 4403 and does not reconnect", () => {
    const h = harness();
    h.socket.start();
    h.last().open();
    h.last().drop(4403);
    h.advance(60_000);
    expect(h.sockets).toHaveLength(1);
    expect(h.socket.state).toEqual({ status: "stopped", reason: "forbidden" });
  });

  it("goes offline with the browser and reconnects immediately when it is back", () => {
    const h = harness();
    h.socket.start();
    h.last().open();
    h.network.onLine = false;
    h.network.dispatchEvent(new Event("offline"));
    expect(h.socket.state.status).toBe("offline");
    h.network.onLine = true;
    h.network.dispatchEvent(new Event("online"));
    expect(h.sockets).toHaveLength(2);
    expect(h.socket.state.status).toBe("connecting");
  });

  it("refuses to send while not open so callers keep frames queued", () => {
    const h = harness();
    h.socket.start();
    expect(h.socket.send("x")).toBe(false);
    h.last().open();
    expect(h.socket.send("x")).toBe(true);
    expect(h.last().sent).toEqual(["x"]);
  });
});
