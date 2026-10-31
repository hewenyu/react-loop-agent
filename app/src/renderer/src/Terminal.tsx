import { useEffect, useRef } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { wsURL } from "./api";
import {
  TERM_CLOSE_EXITED,
  TERM_CLOSE_RESYNC,
  TERM_RESIZE_DEBOUNCE_MS,
  TERM_RETRY_MS,
  TERM_SCROLLBACK,
} from "./constants";

const theme = {
  background: "#141311",
  foreground: "#e9e6df",
  cursor: "#e07a52",
  selectionBackground: "#4a3326",
  black: "#1f1e1c",
  brightBlack: "#6f6b63",
};

// Terminal shows an agent's terminal and types into it. It replays what
// the agent printed so far, then follows it live; keys go to the agent as
// if typed in its own terminal. An agent that ended and is resumed keeps
// its id, so when live turns true again the terminal reattaches.
export function Terminal({ agentId, live }: { agentId: string; live: boolean }) {
  const host = useRef<HTMLDivElement>(null);
  // reattach opens the stream again if it is not open; set by the effect
  // that owns the terminal.
  const reattach = useRef<(() => void) | null>(null);

  useEffect(() => {
    const el = host.current;
    if (!el) return;

    const term = new XTerm({
      fontFamily: '"SF Mono", "JetBrains Mono", Menlo, monospace',
      fontSize: 13,
      lineHeight: 1.15,
      cursorBlink: true,
      scrollback: TERM_SCROLLBACK,
      allowProposedApi: true,
      theme,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(el);

    const enc = new TextEncoder();
    let ws: WebSocket | null = null;
    let closed = false;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const sendSize = () => {
      if (ws?.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      }
    };

    const retry = () => {
      clearTimeout(timer);
      timer = setTimeout(open, TERM_RETRY_MS);
    };

    const open = () => {
      if (closed) return;
      clearTimeout(timer);
      let sock: WebSocket;
      try {
        sock = new WebSocket(wsURL(`/api/agents/${encodeURIComponent(agentId)}/term`));
      } catch {
        // Not connected to the daemon yet.
        retry();
        return;
      }
      term.reset();
      ws = sock;
      sock.binaryType = "arraybuffer";
      sock.onopen = () => {
        fit.fit();
        sendSize();
      };
      sock.onmessage = (e) => {
        if (e.data instanceof ArrayBuffer) term.write(new Uint8Array(e.data));
      };
      sock.onclose = (e) => {
        if (ws === sock) ws = null;
        if (closed) return;
        if (e.code === TERM_CLOSE_RESYNC) {
          // Fell behind: replay from the start.
          open();
        } else if (e.reason !== TERM_CLOSE_EXITED) {
          retry();
        }
      };
    };

    reattach.current = () => {
      if (!ws) open();
    };

    const input = term.onData((d) => {
      if (ws?.readyState === WebSocket.OPEN) ws.send(enc.encode(d));
    });
    const binary = term.onBinary((d) => {
      if (ws?.readyState === WebSocket.OPEN) {
        ws.send(Uint8Array.from(d, (c) => c.charCodeAt(0) & 0xff));
      }
    });

    let resizeTimer: ReturnType<typeof setTimeout> | undefined;
    const observer = new ResizeObserver(() => {
      clearTimeout(resizeTimer);
      resizeTimer = setTimeout(() => {
        if (el.clientWidth === 0) return;
        fit.fit();
        sendSize();
      }, TERM_RESIZE_DEBOUNCE_MS);
    });
    observer.observe(el);

    fit.fit();
    open();
    term.focus();

    return () => {
      closed = true;
      reattach.current = null;
      clearTimeout(timer);
      clearTimeout(resizeTimer);
      observer.disconnect();
      input.dispose();
      binary.dispose();
      ws?.close();
      term.dispose();
    };
  }, [agentId]);

  // The stream ends with the program; a resumed agent needs it again.
  useEffect(() => {
    if (live) reattach.current?.();
  }, [live]);

  return <div className="term" ref={host} />;
}
