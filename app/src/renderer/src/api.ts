import { useEffect, useState } from "react";
import type { State } from "./types";

let daemon: { addr: string; token: string } | null = null;

// connect asks the main process for the daemon, starting it if needed.
export async function connect(): Promise<void> {
  daemon = await window.rla.daemon();
}

function base(): { addr: string; token: string } {
  if (!daemon) throw new Error("not connected to the daemon");
  return daemon;
}

// call sends a request to the daemon and returns its JSON answer.
export async function call<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
  const d = base();
  const res = await fetch(`http://${d.addr}${path}`, {
    method,
    headers: { Authorization: `Bearer ${d.token}`, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) throw new Error(data?.error || `${method} ${path}: ${res.status}`);
  return data as T;
}

export function wsURL(path: string): string {
  const d = base();
  return `ws://${d.addr}${path}${path.includes("?") ? "&" : "?"}token=${d.token}`;
}

// useDaemonState follows the daemon's state: it sends the whole state on
// connect and after every change. It reconnects, starting the daemon again
// if it went away.
export function useDaemonState(): { state: State | null; online: boolean } {
  const [state, setState] = useState<State | null>(null);
  const [online, setOnline] = useState(false);

  useEffect(() => {
    let ws: WebSocket | null = null;
    let closed = false;
    let retry = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const open = async () => {
      try {
        await connect();
      } catch {
        schedule();
        return;
      }
      if (closed) return;
      ws = new WebSocket(wsURL("/api/events"));
      ws.onopen = () => {
        retry = 0;
        setOnline(true);
      };
      ws.onmessage = (e) => {
        const msg = JSON.parse(e.data as string);
        if (msg.type === "state") setState(msg.state);
      };
      ws.onclose = () => {
        setOnline(false);
        schedule();
      };
    };

    const schedule = () => {
      if (closed) return;
      retry = Math.min(retry + 1, 6);
      timer = setTimeout(open, 250 * 2 ** retry);
    };

    void open();
    return () => {
      closed = true;
      clearTimeout(timer);
      ws?.close();
    };
  }, []);

  return { state, online };
}
