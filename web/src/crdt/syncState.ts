/**
 * The sync badge's state machine: four states the design names (Synced,
 * Syncing, Offline, Draft), derived from the connection and the local queue.
 * A pure reducer, so every transition is a unit test.
 *
 * The sync protocol has no acknowledgement frame, so a sent batch counts as
 * delivered once the connection has stayed open for ACK_GRACE_MS without a
 * rejection (`confirmed`); batches still unconfirmed when the connection
 * drops go back to pending and are resent on reconnect, which is safe
 * because operations are idempotent.
 */

export const ACK_GRACE_MS = 1500;

export type Connection = "idle" | "connecting" | "open" | "waiting" | "offline" | "stopped";

export type SyncState = {
  connection: Connection;
  /** Local batches not yet sent on an open connection. */
  pending: number;
  /** Sent, waiting out the grace period. */
  inflight: number;
  /** Operations the server refused that sit in a draft. */
  rejected: number;
  /** Waiting for the server's answer to our hello. */
  catchingUp: boolean;
  lastSyncedAt: number | null;
  stopReason: string | null;
};

export type SyncEvent =
  | { type: "connection"; connection: Connection; stopReason?: string }
  | { type: "local-edit" }
  | { type: "restored"; pending: number; rejected: number }
  | { type: "hello-sent" }
  | { type: "caught-up"; at: number }
  | { type: "sent"; count: number }
  | { type: "confirmed"; count: number; at: number }
  | { type: "rejected"; operations: number; batches: number }
  | { type: "draft-resolved"; operations: number };

export const initialSync: SyncState = {
  connection: "idle",
  pending: 0,
  inflight: 0,
  rejected: 0,
  catchingUp: false,
  lastSyncedAt: null,
  stopReason: null,
};

export function syncReducer(state: SyncState, event: SyncEvent): SyncState {
  switch (event.type) {
    case "connection": {
      const next = { ...state, connection: event.connection, stopReason: event.stopReason ?? null };
      // A dropped connection may have lost what was in flight: it goes back to pending.
      if (event.connection !== "open" && state.inflight > 0) {
        next.pending += state.inflight;
        next.inflight = 0;
      }
      if (event.connection !== "open") next.catchingUp = false;
      return next;
    }
    case "local-edit":
      return { ...state, pending: state.pending + 1 };
    case "restored":
      return { ...state, pending: event.pending, rejected: event.rejected };
    case "hello-sent":
      return { ...state, catchingUp: true };
    case "caught-up":
      return { ...state, catchingUp: false, lastSyncedAt: event.at };
    case "sent": {
      const count = Math.min(event.count, state.pending);
      return { ...state, pending: state.pending - count, inflight: state.inflight + count };
    }
    case "confirmed": {
      const count = Math.min(event.count, state.inflight);
      return { ...state, inflight: state.inflight - count, lastSyncedAt: event.at };
    }
    case "rejected": {
      // The refused batches leave the queue (the client drops them) and become a draft.
      const fromInflight = Math.min(event.batches, state.inflight);
      const fromPending = Math.min(event.batches - fromInflight, state.pending);
      return {
        ...state,
        inflight: state.inflight - fromInflight,
        pending: state.pending - fromPending,
        rejected: state.rejected + event.operations,
      };
    }
    case "draft-resolved":
      return { ...state, rejected: Math.max(0, state.rejected - event.operations) };
  }
}

export type SyncStatus = "synced" | "syncing" | "offline" | "draft";

export function syncStatus(state: SyncState): SyncStatus {
  if (state.rejected > 0) return "draft";
  if (state.connection !== "open") {
    // Connecting for the first time with nothing queued reads as syncing, not as a problem.
    if ((state.connection === "connecting" || state.connection === "idle") && state.pending === 0) return "syncing";
    return "offline";
  }
  if (state.catchingUp || state.inflight > 0 || state.pending > 0) return "syncing";
  return "synced";
}

/** The badge's label, its bold count and the tooltip, in the design's words. */
export function describeSync(state: SyncState, versionLabel?: string): { status: SyncStatus; label: string; count: string; title: string } {
  const status = syncStatus(state);
  const queued = state.pending + state.inflight;
  switch (status) {
    case "draft":
      return {
        status,
        label: "Draft",
        count: `${state.rejected} rejected`,
        title: `The server rejected ${state.rejected} change${state.rejected === 1 ? "" : "s"}; they are kept as a draft`,
      };
    case "offline":
      return {
        status,
        label: "Offline",
        count: queued > 0 ? `${queued} pending` : "",
        title: state.stopReason === "forbidden" ? "Your access to this note changed" : "Saved on this device; sends when you reconnect",
      };
    case "syncing":
      return { status, label: "Syncing", count: queued > 0 ? String(queued) : "", title: queued > 0 ? `Sending ${queued} changes` : "Catching up with the server" };
    case "synced":
      return { status, label: "Synced", count: versionLabel ?? "", title: "Every change is on the server" };
  }
}
