import { BASE } from "./api";

export function wsBase(): string {
  return BASE.replace(/^http/, "ws");
}

export function gameSocketUrl(roomId: string, token?: string): string {
  const t = token ? `&token=${encodeURIComponent(token)}` : "";
  return `${wsBase()}/ws?room=${encodeURIComponent(roomId)}${t}`;
}

export function adminStreamUrl(roomId: string): string {
  return `${wsBase()}/admin/rooms/${encodeURIComponent(roomId)}/stream`;
}

// Server -> client message shapes
export type WsSnapshot = { type: "snapshot"; room_id: string; state: any };
export type WsEvent = {
  type: "event";
  room_id: string;
  event: {
    seq: number;
    type: string;
    player_id: string;
    payload: any;
    timestamp: string;
    [k: string]: any;
  };
  state: any;
};
export type WsError = { type: "error"; error: string };
export type WsMessage = WsSnapshot | WsEvent | WsError | { type: string; [k: string]: any };

export type ReconnectingSocketHandlers = {
  onMessage?: (msg: WsMessage) => void;
  onStatusChange?: (status: WsStatus) => void;
};

export type WsStatus = "connecting" | "open" | "reconnecting" | "closed";

/**
 * A small auto-reconnecting WebSocket wrapper. The server resends a snapshot
 * on (re)connect, so we don't need to buffer state ourselves.
 */
export class ReconnectingSocket {
  private url: string;
  private ws: WebSocket | null = null;
  private handlers: ReconnectingSocketHandlers;
  private closedByUser = false;
  private retry = 0;
  private timer: ReturnType<typeof setTimeout> | null = null;

  constructor(url: string, handlers: ReconnectingSocketHandlers = {}) {
    this.url = url;
    this.handlers = handlers;
  }

  connect() {
    this.closedByUser = false;
    this.open();
  }

  private setStatus(s: WsStatus) {
    this.handlers.onStatusChange?.(s);
  }

  private open() {
    this.setStatus(this.retry === 0 ? "connecting" : "reconnecting");
    let ws: WebSocket;
    try {
      ws = new WebSocket(this.url);
    } catch {
      this.scheduleReconnect();
      return;
    }
    this.ws = ws;

    ws.onopen = () => {
      this.retry = 0;
      this.setStatus("open");
    };
    ws.onmessage = (ev) => {
      let parsed: WsMessage;
      try {
        parsed = JSON.parse(ev.data);
      } catch {
        return;
      }
      this.handlers.onMessage?.(parsed);
    };
    ws.onerror = () => {
      // onclose will follow and drive reconnect
    };
    ws.onclose = () => {
      this.ws = null;
      if (this.closedByUser) {
        this.setStatus("closed");
        return;
      }
      this.scheduleReconnect();
    };
  }

  private scheduleReconnect() {
    this.setStatus("reconnecting");
    const delay = Math.min(1000 * Math.pow(1.6, this.retry), 8000);
    this.retry += 1;
    this.timer = setTimeout(() => this.open(), delay);
  }

  send(obj: unknown): boolean {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(obj));
      return true;
    }
    return false;
  }

  close() {
    this.closedByUser = true;
    if (this.timer) clearTimeout(this.timer);
    this.ws?.close();
    this.ws = null;
    this.setStatus("closed");
  }
}
