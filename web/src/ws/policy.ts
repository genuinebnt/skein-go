/**
 * What to do when a Skein WebSocket closes, from docs/api/websocket.md's
 * close-code table, and how long to wait before the next attempt. Pure, so
 * the whole policy is unit-tested without a socket.
 */

export const CloseCode = {
  NORMAL: 1000,
  GOING_AWAY: 1001,
  /** The browser reports this when the connection dropped without a close frame. */
  ABNORMAL: 1006,
  TOO_BIG: 1009,
  INTERNAL: 1011,
  RESTART: 1012,
  TRY_AGAIN_LATER: 1013,
  BAD_REQUEST: 4400,
  UNAUTHENTICATED: 4401,
  FORBIDDEN: 4403,
  NOT_FOUND: 4404,
  RATE_LIMITED: 4429,
} as const;

export type StopReason = "finished" | "forbidden" | "not-found" | "bad-request" | "too-big";

export type CloseAction =
  | { kind: "reconnect"; delayMs: number }
  | { kind: "refresh" }
  | { kind: "stop"; reason: StopReason };

export const BACKOFF_START_MS = 1000;
export const BACKOFF_CAP_MS = 30_000;

/**
 * Exponential backoff with jitter, from 1 s to a 30 s cap (websocket.md). The
 * delay is half fixed and half random ("equal jitter"), so reconnects after
 * a gateway restart spread out but never come back immediately.
 */
export function backoffDelay(attempt: number, random: () => number): number {
  const base = Math.min(BACKOFF_CAP_MS, BACKOFF_START_MS * 2 ** Math.max(0, attempt));
  return Math.round(base / 2 + random() * (base / 2));
}

/**
 * Decides the response to a close. `attempt` counts consecutive failed
 * connections, so the backoff grows only while the server keeps refusing.
 */
export function onClose(code: number, reason: string, attempt: number, random: () => number): CloseAction {
  switch (code) {
    case CloseCode.NORMAL:
      return { kind: "stop", reason: "finished" };
    case CloseCode.UNAUTHENTICATED:
      return { kind: "refresh" };
    case CloseCode.FORBIDDEN:
      return { kind: "stop", reason: "forbidden" };
    case CloseCode.NOT_FOUND:
      return { kind: "stop", reason: "not-found" };
    case CloseCode.BAD_REQUEST:
      return { kind: "stop", reason: "bad-request" };
    case CloseCode.TOO_BIG:
      return { kind: "stop", reason: "too-big" };
    case CloseCode.RATE_LIMITED: {
      // The reason is the number of seconds to wait; a little jitter on top.
      const seconds = Number.parseFloat(reason);
      const wait = Number.isFinite(seconds) && seconds >= 0 ? seconds * 1000 : backoffDelay(attempt, random);
      return { kind: "reconnect", delayMs: Math.round(wait + random() * 1000) };
    }
    default:
      // 1001, 1006, 1011, 1012, 1013 and anything unknown: transient.
      return { kind: "reconnect", delayMs: backoffDelay(attempt, random) };
  }
}

/** The connection's lifecycle, as the UI shows it. */
export type SocketState =
  | { status: "idle" }
  | { status: "connecting"; attempt: number }
  | { status: "open" }
  | { status: "waiting"; attempt: number; retryAt: number }
  | { status: "offline" }
  | { status: "stopped"; reason: StopReason | "signed-out" };
