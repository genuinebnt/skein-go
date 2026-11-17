/**
 * The two JSON streams of docs/api/websocket.md: the agent's
 * /v1/chats/{chatId}/messages (one run per frame sent) and the caller's
 * /v1/notifications/live. Both reconnect through ReconnectingSocket; neither
 * replays what was missed while disconnected, so `onReconnect` lets callers
 * catch up over REST (GetRun, ListInbox).
 */

import type { Notification, SendMessageResponse } from "../api/schema.gen";
import type { SocketState } from "./policy";
import { ReconnectingSocket, type WebSocketFactory } from "./socket";

export type ChannelDeps = {
  wsBase: string;
  createSocket: WebSocketFactory;
  accessToken: () => string | null;
  refresh: () => Promise<boolean>;
  network?: Pick<EventTarget, "addEventListener" | "removeEventListener"> & { onLine?: boolean };
};

export class AgentChannel {
  private socket: ReconnectingSocket;
  private queue: string[] = [];
  private opened = false;

  constructor(
    chatId: string,
    deps: ChannelDeps,
    onEvent: (event: SendMessageResponse) => void,
    onState?: (state: SocketState) => void,
    onReconnect?: () => void,
  ) {
    this.socket = new ReconnectingSocket({
      url: `${deps.wsBase}/v1/chats/${encodeURIComponent(chatId)}/messages`,
      subprotocol: "skein.v1.json",
      accessToken: deps.accessToken,
      refresh: deps.refresh,
      create: deps.createSocket,
      network: deps.network,
      onState,
      onOpen: (socket) => {
        if (this.opened) onReconnect?.();
        this.opened = true;
        // Messages typed while connecting go out now, in order.
        while (this.queue.length > 0 && socket.send(this.queue[0]!)) this.queue.shift();
      },
      onMessage: (data) => {
        if (typeof data !== "string") return;
        try {
          onEvent(JSON.parse(data) as SendMessageResponse);
        } catch {
          // ignore a frame that is not JSON
        }
      },
    });
    this.socket.start();
  }

  /** Starts a run. Closing the socket does not cancel it (websocket.md). */
  send(text: string): void {
    const frame = JSON.stringify({ text });
    if (!this.socket.send(frame)) this.queue.push(frame);
  }

  close(): void {
    this.socket.stop();
  }
}

export class NotificationChannel {
  private socket: ReconnectingSocket;
  private opened = false;

  constructor(deps: ChannelDeps, onNotification: (n: Notification) => void, onReconnect: () => void, onState?: (state: SocketState) => void) {
    this.socket = new ReconnectingSocket({
      url: `${deps.wsBase}/v1/notifications/live`,
      subprotocol: "skein.v1.json",
      accessToken: deps.accessToken,
      refresh: deps.refresh,
      create: deps.createSocket,
      network: deps.network,
      onState,
      onOpen: () => {
        if (this.opened) onReconnect();
        this.opened = true;
      },
      onMessage: (data) => {
        if (typeof data !== "string") return;
        try {
          const frame = JSON.parse(data) as { notification?: Notification };
          if (frame.notification) onNotification(frame.notification);
        } catch {
          // ignore
        }
      },
    });
    this.socket.start();
  }

  close(): void {
    this.socket.stop();
  }
}
