import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { useEffect, useRef } from "react";

import { terminalURL } from "@/lib/api";

const THEME = {
  background: "#050806",
  foreground: "#c9e4cf",
  cursor: "#3ddc6e",
  cursorAccent: "#050806",
  selectionBackground: "#3ddc6e55",
  black: "#0a0e0b",
  red: "#ff6b5e",
  green: "#3ddc6e",
  yellow: "#f2c14e",
  blue: "#5ab0ff",
  magenta: "#d68cff",
  cyan: "#5fd7d7",
  white: "#c9e4cf",
  brightBlack: "#7f9c86",
  brightRed: "#ff8a80",
  brightGreen: "#8cffb0",
  brightYellow: "#ffd97a",
  brightBlue: "#8cc8ff",
  brightMagenta: "#e5b3ff",
  brightCyan: "#9df0f0",
  brightWhite: "#e8fff0",
};

export type TermStatus = "connecting" | "open" | "closed";

// One SSH session: xterm.js <-> WebSocket <-> srvmon-server <-> sshd.
// Binary frames carry terminal bytes, text frames carry resize events.
export function XTerm({
  serverId,
  container,
  active,
  onStatus,
  onSize,
}: {
  serverId: number;
  container?: string;
  active: boolean;
  onStatus: (s: TermStatus) => void;
  onSize: (cols: number, rows: number) => void;
}) {
  const box = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const cb = useRef({ onStatus, onSize });
  cb.current = { onStatus, onSize };

  useEffect(() => {
    const term = new Terminal({
      fontFamily: '"JetBrains Mono", ui-monospace, Menlo, monospace',
      fontSize: 13,
      lineHeight: 1.2,
      cursorBlink: true,
      scrollback: 5000,
      theme: THEME,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(box.current!);
    termRef.current = term;
    fitRef.current = fit;
    try {
      fit.fit();
    } catch {
      /* hidden tab: fit later */
    }

    const enc = new TextEncoder();
    const ws = new WebSocket(terminalURL(serverId, term.cols, term.rows, container));
    ws.binaryType = "arraybuffer";
    cb.current.onStatus("connecting");
    ws.onopen = () => {
      cb.current.onStatus("open");
      ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
    };
    ws.onmessage = (ev) => {
      if (ev.data instanceof ArrayBuffer) term.write(new Uint8Array(ev.data));
      else term.write(String(ev.data));
    };
    ws.onclose = (ev) => {
      cb.current.onStatus("closed");
      const why = ev.code === 1000 ? "" : ` (код ${ev.code}${ev.code === 1006 ? ": нет связи или нет доступа" : ""})`;
      term.write(`\r\n\x1b[90m[соединение закрыто${why}]\x1b[0m\r\n`);
    };
    const onData = term.onData((d) => ws.readyState === WebSocket.OPEN && ws.send(enc.encode(d)));
    const onBinary = term.onBinary((d) => {
      if (ws.readyState !== WebSocket.OPEN) return;
      const buf = new Uint8Array(d.length);
      for (let i = 0; i < d.length; i++) buf[i] = d.charCodeAt(i) & 0xff;
      ws.send(buf);
    });
    const onResize = term.onResize(({ cols, rows }) => {
      cb.current.onSize(cols, rows);
      if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "resize", cols, rows }));
    });
    cb.current.onSize(term.cols, term.rows);

    const ro = new ResizeObserver(() => {
      if (box.current && box.current.offsetParent !== null) {
        try {
          fit.fit();
        } catch {
          /* ignore */
        }
      }
    });
    ro.observe(box.current!);

    return () => {
      ro.disconnect();
      onData.dispose();
      onBinary.dispose();
      onResize.dispose();
      ws.close();
      term.dispose();
      termRef.current = null;
    };
  }, [serverId, container]);

  useEffect(() => {
    if (!active) return;
    const id = requestAnimationFrame(() => {
      try {
        fitRef.current?.fit();
      } catch {
        /* ignore */
      }
      termRef.current?.focus();
    });
    return () => cancelAnimationFrame(id);
  }, [active]);

  return <div ref={box} className="h-full w-full" />;
}
