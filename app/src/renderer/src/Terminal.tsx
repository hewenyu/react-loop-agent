import { useEffect, useRef } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { wsURL } from "./api";

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
// if typed in its own terminal.
export function Terminal({ agentId }: { agentId: string }) {
  const host = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const el = host.current;
    if (!el) return;

    const term = new XTerm({
      fontFamily: '"SF Mono", "JetBrains Mono", Menlo, monospace',
      fontSize: 13,
      lineHeight: 1.15,
      cursorBlink: true,
      scrollback: 20000,
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

    const open = () => {
      term.reset();
      ws = new WebSocket(wsURL(`/api/agents/${agentId}/term`));
      ws.binaryType = "arraybuffer";
      ws.onopen = () => {
        fit.fit();
        sendSize();
      };
      ws.onmessage = (e) => {
        if (e.data instanceof ArrayBuffer) term.write(new Uint8Array(e.data));
      };
      ws.onclose = (e) => {
        if (closed) return;
        if (e.code === 4000) {
          // Fell behind: replay from the start.
          open();
        } else if (e.reason !== "exited") {
          timer = setTimeout(open, 1500);
        }
      };
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
      }, 60);
    });
    observer.observe(el);

    fit.fit();
    open();
    term.focus();

    return () => {
      closed = true;
      clearTimeout(timer);
      clearTimeout(resizeTimer);
      observer.disconnect();
      input.dispose();
      binary.dispose();
      ws?.close();
      term.dispose();
    };
  }, [agentId]);

  return <div className="term" ref={host} />;
}
