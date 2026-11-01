import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { useCallback, useEffect, useRef, useState } from "react";

import { Hotkey, Shell } from "@/components/Shell";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { THEME } from "@/components/XTerm";
import { ApiError, recApi, type Recording } from "@/lib/api";
import { t } from "@/lib/i18n";
import { href, type Route, typingIn } from "@/lib/router";
import { cn, formatBytes } from "@/lib/utils";

function fmtDuration(sec: number): string {
  const s = Math.max(0, Math.floor(sec));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const ss = String(s % 60).padStart(2, "0");
  return h ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`;
}

function fmtTime(iso: string): string {
  const d = new Date(iso);
  return `${d.toISOString().slice(0, 10)} ${d.toISOString().slice(11, 19)}`;
}

function recDuration(r: Recording): number | undefined {
  if (!r.ended_at) return undefined;
  return (Date.parse(r.ended_at) - Date.parse(r.started_at)) / 1000;
}

export function RecordingsPage({
  id,
  route,
  version,
  onLogout,
}: {
  id?: number;
  route: Route;
  version: string;
  onLogout: () => void;
}) {
  return (
    <Shell
      route={route}
      version={version}
      onLogout={onLogout}
      footer={
        id ? (
          <>
            <Hotkey k="space">{t("play / pause")}</Hotkey>
            <Hotkey k="← →">{t("±5 s")}</Hotkey>
            <Hotkey k="esc">{t("to the list")}</Hotkey>
          </>
        ) : (
          <>
            <Hotkey k="3">{t("terminal")}</Hotkey>
            <span className="text-muted-foreground">{t("recordings keep terminal output only, never keystrokes")}</span>
          </>
        )
      }
      footerNote={t("recordings are encrypted with the master key")}
    >
      {id ? <Player id={id} onLogout={onLogout} /> : <RecordingList onLogout={onLogout} />}
    </Shell>
  );
}

function RecordingList({ onLogout }: { onLogout: () => void }) {
  const [list, setList] = useState<Recording[] | null>(null);
  const [more, setMore] = useState(false);
  const [servers, setServers] = useState<[number, string][]>([]);
  const [server, setServer] = useState(0);
  const [err, setErr] = useState("");
  // Bumped on every filter change and reload: a response for an older
  // generation (another server, a page in flight) is dropped.
  const gen = useRef(0);

  const fail = useCallback(
    (e: unknown) => {
      if (e instanceof ApiError && e.status === 401) onLogout();
      else setErr(t("could not load"));
    },
    [onLogout],
  );
  // The server filter and pages come from the API: the list holds a page
  // at a time, so filtering it here would miss older recordings.
  const load = useCallback(() => {
    setErr("");
    const g = ++gen.current;
    recApi
      .list(server)
      .then((l) => {
        if (g !== gen.current) return;
        setList(l);
        setMore(l.length >= PAGE);
      })
      .catch((e) => g === gen.current && fail(e));
    recApi
      .servers()
      .then((s) => setServers(s.map((x) => [x.id, x.name])))
      .catch(() => undefined);
  }, [server, fail]);
  useEffect(load, [load]);

  const loadMore = () => {
    const last = list?.[list.length - 1];
    if (!last) return;
    const g = gen.current;
    recApi
      .list(server, last.id)
      .then((l) => {
        if (g !== gen.current) return;
        setList((cur) => [...(cur ?? []), ...l]);
        setMore(l.length >= PAGE);
      })
      .catch((e) => g === gen.current && fail(e));
  };
  const shown = list ?? [];

  const remove = async (r: Recording) => {
    if (!confirm(t("Delete the recording of {target}?", { target: r.target }))) return;
    try {
      await recApi.remove(r.id);
      load();
    } catch {
      setErr(t("could not delete"));
    }
  };

  return (
    <Panel
      title={t("terminal recordings")}
      right={
        <a className="text-primary" href={href.settings()}>
          {t("settings")}
        </a>
      }
    >
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Button size="sm" variant={server === 0 ? "default" : "outline"} onClick={() => setServer(0)}>
          {t("all")}
        </Button>
        {servers.map(([id, name]) => (
          <Button key={id} size="sm" variant={server === id ? "default" : "outline"} onClick={() => setServer(id)}>
            {name}
          </Button>
        ))}
      </div>
      {err && <div className="mb-2 text-sm text-crit">{err}</div>}
      {list === null && !err && <div className="text-muted-foreground">{t("loading…")}</div>}
      {list && shown.length === 0 && (
        <div className="text-muted-foreground">
          {t("No recordings yet. Every web terminal session is recorded while recording is on in settings.")}
        </div>
      )}
      {shown.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-muted-foreground">
              <tr>
                <th className="py-1 pr-4 font-normal">{t("started (UTC)")}</th>
                <th className="py-1 pr-4 font-normal">{t("session")}</th>
                <th className="py-1 pr-4 font-normal">{t("duration")}</th>
                <th className="py-1 pr-4 font-normal">{t("size")}</th>
                <th className="py-1 pr-4 font-normal">ip</th>
                <th className="py-1 font-normal" />
              </tr>
            </thead>
            <tbody>
              {shown.map((r) => {
                const d = recDuration(r);
                return (
                  <tr key={r.id} className="border-t border-border/60">
                    <td className="py-1.5 pr-4 whitespace-nowrap">{fmtTime(r.started_at)}</td>
                    <td className="py-1.5 pr-4 text-hi">{r.target}</td>
                    <td className="py-1.5 pr-4 whitespace-nowrap">
                      {d === undefined ? <span className="text-crit">● {t("live")}</span> : fmtDuration(d)}
                    </td>
                    <td className="py-1.5 pr-4 whitespace-nowrap">
                      {formatBytes(r.bytes)}
                      {r.truncated && <span className="ml-1 text-warn" title={t("stopped at the size limit")}>▲</span>}
                    </td>
                    <td className="py-1.5 pr-4 text-muted-foreground">{r.ip}</td>
                    <td className="py-1.5 whitespace-nowrap text-right">
                      <Button size="sm" asChild>
                        <a href={href.recordings(r.id)}>▶ {t("play")}</a>
                      </Button>{" "}
                      <Button size="sm" variant="outline" asChild>
                        <a href={recApi.castURL(r.id, true)}>⤓ .cast</a>
                      </Button>{" "}
                      <Button size="sm" variant="ghost" aria-label={t("delete")} onClick={() => void remove(r)}>
                        ×
                      </Button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {more && (
            <div className="mt-3">
              <Button size="sm" variant="outline" onClick={loadMore}>
                {t("show older")}
              </Button>
            </div>
          )}
        </div>
      )}
    </Panel>
  );
}

const PAGE = 100; // recordings per request, as on the server

// asciicast v2 event with the idle time already compressed.
interface Ev {
  t: number;
  code: string;
  data: string;
}

// Pauses longer than this play as this long (like asciinema's idle limit).
const IDLE_LIMIT = 2;
const SPEEDS = [1, 2, 4, 8];

function parseCast(text: string): { cols: number; rows: number; events: Ev[]; duration: number } {
  const lines = text.split("\n");
  const head = JSON.parse(lines[0]) as { width?: number; height?: number };
  const events: Ev[] = [];
  let prev = 0;
  let at = 0;
  for (let i = 1; i < lines.length; i++) {
    const l = lines[i];
    if (!l) continue;
    try {
      const [ts, code, data] = JSON.parse(l) as [number, string, string];
      at += Math.min(Math.max(ts - prev, 0), IDLE_LIMIT);
      prev = ts;
      events.push({ t: at, code, data });
    } catch {
      /* a line cut by a crash */
    }
  }
  return { cols: head.width || 80, rows: head.height || 24, events, duration: at };
}

function Player({ id, onLogout }: { id: number; onLogout: () => void }) {
  const [meta, setMeta] = useState<Recording | null>(null);
  const [err, setErr] = useState("");
  const [cast, setCast] = useState<ReturnType<typeof parseCast> | null>(null);
  const [playing, setPlaying] = useState(false);
  const [speed, setSpeed] = useState(1);
  const [pos, setPos] = useState(0);
  const box = useRef<HTMLDivElement>(null);
  const term = useRef<Terminal | null>(null);
  const state = useRef({ idx: 0, clock: 0 });
  // Terminal operations in order: xterm parses writes later, a resize or
  // reset applies at once, so each waits for the writes queued before it.
  const queue = useRef<Promise<void>>(Promise.resolve());

  useEffect(() => {
    let alive = true;
    Promise.all([recApi.get(id), recApi.cast(id)])
      .then(([m, text]) => {
        if (!alive) return;
        setMeta(m);
        setCast(parseCast(text));
        setPlaying(true);
      })
      .catch((e) => {
        if (!alive) return;
        if (e instanceof ApiError && e.status === 401) onLogout();
        else setErr(e instanceof ApiError && e.status === 404 ? t("Recording not found.") : t("could not load"));
      });
    return () => {
      alive = false;
    };
  }, [id, onLogout]);

  useEffect(() => {
    if (!cast || !box.current) return;
    const tm = new Terminal({
      fontFamily: '"JetBrains Mono", ui-monospace, Menlo, monospace',
      fontSize: 13,
      lineHeight: 1.2,
      cols: cast.cols,
      rows: cast.rows,
      disableStdin: true,
      cursorBlink: false,
      scrollback: 2000,
      theme: THEME,
    });
    tm.open(box.current);
    term.current = tm;
    state.current = { idx: 0, clock: 0 };
    queue.current = Promise.resolve();
    return () => {
      tm.dispose();
      term.current = null;
    };
  }, [cast]);

  // Writes every event up to time `to`; going back replays from the start.
  const seek = useCallback(
    (to: number) => {
      const tm = term.current;
      if (!tm || !cast) return;
      const st = state.current;
      const run = (f: () => void) => {
        queue.current = queue.current.then(() => {
          if (term.current === tm) f();
        });
      };
      const write = (data: string) => {
        queue.current = queue.current.then(
          () => new Promise<void>((done) => (term.current === tm ? tm.write(data, done) : done())),
        );
      };
      if (to < st.clock) {
        run(() => {
          tm.reset();
          tm.resize(cast.cols, cast.rows);
        });
        st.idx = 0;
      }
      let buf = "";
      while (st.idx < cast.events.length && cast.events[st.idx].t <= to) {
        const ev = cast.events[st.idx++];
        if (ev.code === "o") buf += ev.data;
        else if (ev.code === "r") {
          if (buf) write(buf);
          buf = "";
          const [c, r] = ev.data.split("x").map(Number);
          if (c > 0 && r > 0) run(() => tm.resize(c, r));
        }
      }
      if (buf) write(buf);
      st.clock = to;
      setPos(to);
    },
    [cast],
  );

  useEffect(() => {
    if (!playing || !cast) return;
    let raf = 0;
    let last = performance.now();
    const tick = (now: number) => {
      const next = Math.min(state.current.clock + ((now - last) / 1000) * speed, cast.duration);
      last = now;
      seek(next);
      if (next >= cast.duration) {
        setPlaying(false);
        return;
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [playing, speed, cast, seek]);

  const toggle = useCallback(() => {
    if (!cast) return;
    if (!playing && state.current.clock >= cast.duration) seek(0);
    setPlaying((p) => !p);
  }, [cast, playing, seek]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (typingIn(e) && !(e.target as HTMLElement).closest(".xterm")) return;
      if (!cast) return;
      if (e.key === " ") {
        e.preventDefault();
        toggle();
      } else if (e.key === "ArrowRight") seek(Math.min(state.current.clock + 5, cast.duration));
      else if (e.key === "ArrowLeft") seek(Math.max(state.current.clock - 5, 0));
      else if (e.key === "Escape") window.location.hash = href.recordings();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [cast, toggle, seek]);

  return (
    <Panel
      title={meta ? `▶ ${meta.target}` : t("recording")}
      right={meta ? `${fmtTime(meta.started_at)} UTC · ${formatBytes(meta.bytes)} · ${meta.ip}` : undefined}
      accent
    >
      {err && <div className="text-crit">{err}</div>}
      {!err && !cast && <div className="text-muted-foreground">{t("loading…")}</div>}
      <div className={cn("overflow-auto bg-field p-2", !cast && "hidden")}>
        <div ref={box} className="w-max" />
      </div>
      {cast && (
        <div className="mt-3 flex flex-wrap items-center gap-3">
          <Button size="sm" onClick={toggle} className="w-20">
            {playing ? `❚❚ ${t("pause")}` : `▶ ${t("play")}`}
          </Button>
          <input
            type="range"
            min={0}
            max={cast.duration || 0.001}
            step={0.1}
            value={pos}
            onChange={(e) => seek(Number(e.target.value))}
            className="min-w-40 flex-1 accent-[#3ddc6e]"
            aria-label={t("position")}
          />
          <span className="tabular-nums text-muted-foreground">
            {fmtDuration(pos)} / {fmtDuration(cast.duration)}
          </span>
          <div className="flex gap-1">
            {SPEEDS.map((s) => (
              <Button key={s} size="sm" variant={s === speed ? "default" : "outline"} onClick={() => setSpeed(s)}>
                {s}×
              </Button>
            ))}
          </div>
          <Button size="sm" variant="outline" asChild>
            <a href={recApi.castURL(id, true)}>⤓ .cast</a>
          </Button>
          <Button size="sm" variant="ghost" asChild>
            <a href={href.recordings()}>← {t("all recordings")}</a>
          </Button>
        </div>
      )}
      {cast && (
        <div className="mt-2 text-xs text-muted-foreground">
          {t("Pauses longer than {n} s are shortened. The .cast file plays in asciinema too: asciinema play file.cast", {
            n: IDLE_LIMIT,
          })}
        </div>
      )}
    </Panel>
  );
}
