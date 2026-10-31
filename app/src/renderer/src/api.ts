import { useEffect, useState } from "react";
import { EVENTS_BACKOFF_BASE_MS, EVENTS_BACKOFF_MAX_STEP, REQUEST_TIMEOUT_MS } from "./constants";
import type { Event, State } from "./types";

let daemon: { addr: string; token: string } | null = null;

// connect asks the main process for the daemon, starting it if needed.
export async function connect(): Promise<void> {
  daemon = await window.rla.daemon();
}

function base(): { addr: string; token: string } {
  if (!daemon) throw new Error("还没连上 rla daemon");
  return daemon;
}

// message is what to show for a failure.
export function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

// call sends a request to the daemon and returns its JSON answer. Errors
// carry the daemon's own message when it gave one.
export async function call<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
  const d = base();
  let res: Response;
  try {
    res = await fetch(`http://${d.addr}${path}`, {
      method,
      headers: { Authorization: `Bearer ${d.token}`, "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    });
  } catch (e) {
    if (e instanceof DOMException && e.name === "TimeoutError") throw new Error(`rla daemon 没有响应：${method} ${path}`);
    throw new Error(`连不上 rla daemon：${message(e)}`);
  }

  const text = await res.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    if (res.ok) throw new Error(`${method} ${path}: 返回的不是 JSON`);
  }
  if (!res.ok) {
    const err = typeof data === "object" && data !== null && "error" in data ? data.error : undefined;
    throw new Error(typeof err === "string" && err ? err : `${method} ${path}: ${res.status}`);
  }
  return data as T;
}

export function wsURL(path: string): string {
  const d = base();
  return `ws://${d.addr}${path}${path.includes("?") ? "&" : "?"}token=${encodeURIComponent(d.token)}`;
}

function parseEvent(raw: unknown): Event | null {
  if (typeof raw !== "string") return null;
  try {
    const msg: unknown = JSON.parse(raw);
    if (typeof msg === "object" && msg !== null && "type" in msg && msg.type === "state" && "state" in msg) {
      return msg as Event;
    }
  } catch {
    // Ignore what we cannot read; the next change sends the whole state.
  }
  return null;
}

// useDaemonState follows the daemon's state: it sends the whole state on
// connect and after every change. It reconnects, starting the daemon again
// if it went away. error is why the last attempt to reach it failed.
export function useDaemonState(): { state: State | null; online: boolean; error: string } {
  const [state, setState] = useState<State | null>(null);
  const [online, setOnline] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    let ws: WebSocket | null = null;
    let closed = false;
    let retry = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const open = async () => {
      try {
        await connect();
        if (closed) return;
        ws = new WebSocket(wsURL("/api/events"));
      } catch (e) {
        setError(message(e));
        schedule();
        return;
      }
      ws.onopen = () => {
        retry = 0;
        setOnline(true);
        setError("");
      };
      ws.onmessage = (e) => {
        const msg = parseEvent(e.data);
        if (msg) setState(msg.state);
      };
      ws.onclose = () => {
        ws = null;
        if (closed) return;
        setOnline(false);
        schedule();
      };
    };

    const schedule = () => {
      if (closed) return;
      clearTimeout(timer);
      retry = Math.min(retry + 1, EVENTS_BACKOFF_MAX_STEP);
      timer = setTimeout(() => void open(), EVENTS_BACKOFF_BASE_MS * 2 ** retry);
    };

    void open();
    return () => {
      closed = true;
      clearTimeout(timer);
      ws?.close();
    };
  }, []);

  return { state, online, error };
}
