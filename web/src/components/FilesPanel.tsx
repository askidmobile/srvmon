import { useCallback, useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import { ApiError, errorMessage, type FileEntry, filesApi } from "@/lib/api";
import { t } from "@/lib/i18n";
import { cn, formatBytes } from "@/lib/utils";

interface Upload {
  key: number;
  name: string;
  size: number;
  sent: number;
  error?: string;
}

let uploadKey = 1;

function join(dir: string, name: string) {
  return dir === "/" ? `/${name}` : `${dir}/${name}`;
}

function parent(p: string) {
  const i = p.lastIndexOf("/");
  return i <= 0 ? "/" : p.slice(0, i);
}

function errText(e: unknown, fallback: string): string {
  if (e instanceof ApiError) {
    if (e.code === "not_found") return t("no such file or folder");
    if (e.code === "permission") return t("permission denied for the srvmon user");
    if (e.code === "exists") return t("a file with this name already exists");
    if (e.code === "no_ssh_access") return t("no ssh access to this server");
    if (e.code === "bad_path") return t("bad path");
  }
  return errorMessage(e, fallback);
}

function fmtDate(iso: string) {
  const d = new Date(iso);
  return `${d.toISOString().slice(0, 10)} ${d.toISOString().slice(11, 16)}`;
}

// File manager over SFTP for one server, as the srvmon user.
export function FilesPanel({ serverId, serverName, className }: { serverId: number; serverName: string; className?: string }) {
  const [path, setPath] = useState("");
  const [input, setInput] = useState("");
  const [entries, setEntries] = useState<FileEntry[] | null>(null);
  const [truncated, setTruncated] = useState(false);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [uploads, setUploads] = useState<Upload[]>([]);
  const [drag, setDrag] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const seq = useRef(0);

  const load = useCallback(
    async (p?: string) => {
      const n = ++seq.current;
      setBusy(true);
      setErr("");
      try {
        const l = await filesApi.list(serverId, p);
        if (n !== seq.current) return;
        setPath(l.path);
        setInput(l.path);
        setEntries(l.entries ?? []);
        setTruncated(!!l.truncated);
      } catch (e) {
        if (n !== seq.current) return;
        setErr(errText(e, t("could not load")));
        if (p === undefined) setEntries([]);
      }
      if (n === seq.current) setBusy(false);
    },
    [serverId],
  );

  useEffect(() => {
    setEntries(null);
    setPath("");
    void load();
  }, [load]);

  const mkdir = async () => {
    const name = prompt(t("New folder name"));
    if (!name) return;
    try {
      await filesApi.mkdir(serverId, join(path, name));
      await load(path);
    } catch (e) {
      setErr(errText(e, t("could not create")));
    }
  };

  const rename = async (e: FileEntry) => {
    const name = prompt(t("New name"), e.name);
    if (!name || name === e.name) return;
    try {
      await filesApi.rename(serverId, join(path, e.name), join(path, name));
      await load(path);
    } catch (er) {
      setErr(errText(er, t("could not rename")));
    }
  };

  const remove = async (e: FileEntry) => {
    const q = e.dir && !e.link
      ? t("Delete the folder {name} with everything inside?", { name: e.name })
      : t("Delete {name}?", { name: e.name });
    if (!confirm(q)) return;
    try {
      await filesApi.remove(serverId, join(path, e.name), e.dir && !e.link);
      await load(path);
    } catch (er) {
      setErr(errText(er, t("could not delete")));
    }
  };

  const upload = async (files: FileList | File[]) => {
    const dir = path;
    for (const f of Array.from(files)) {
      const key = uploadKey++;
      setUploads((u) => [...u, { key, name: f.name, size: f.size, sent: 0 }]);
      const progress = (sent: number) => setUploads((u) => u.map((x) => (x.key === key ? { ...x, sent } : x)));
      const target = join(dir, f.name);
      try {
        try {
          await filesApi.upload(serverId, target, f, false, progress);
        } catch (e) {
          if (!(e instanceof ApiError && e.code === "exists")) throw e;
          if (!confirm(t("{name} already exists. Replace it?", { name: f.name }))) {
            setUploads((u) => u.filter((x) => x.key !== key));
            continue;
          }
          await filesApi.upload(serverId, target, f, true, progress);
        }
        setUploads((u) => u.filter((x) => x.key !== key));
      } catch (e) {
        const msg = errText(e, t("upload failed"));
        setUploads((u) => u.map((x) => (x.key === key ? { ...x, error: msg } : x)));
      }
    }
    void load(dir);
  };

  return (
    <section
      className={cn("relative flex min-h-0 flex-col border bg-background", drag ? "border-primary" : "border-border", className)}
      onDragOver={(e) => {
        if (!e.dataTransfer.types.includes("Files")) return;
        e.preventDefault();
        setDrag(true);
      }}
      onDragLeave={() => setDrag(false)}
      onDrop={(e) => {
        e.preventDefault();
        setDrag(false);
        if (e.dataTransfer.files.length) void upload(e.dataTransfer.files);
      }}
    >
      <div className="flex items-center gap-2 border-b border-border px-3 py-2">
        <span className="text-primary">{t("files")}</span>
        <span className="truncate text-muted-foreground">sftp · srvmon@{serverName}</span>
        <span className="flex-1" />
        {busy && <span className="text-xs text-muted-foreground">…</span>}
      </div>
      <form
        className="flex gap-1 px-3 pt-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (input.trim()) void load(input.trim());
        }}
      >
        <Button type="button" size="sm" variant="outline" title={t("up")} onClick={() => void load(parent(path))} disabled={!path || path === "/"}>
          ↑
        </Button>
        <Button type="button" size="sm" variant="outline" title={t("home folder")} onClick={() => void load()}>
          ~
        </Button>
        <input
          className="h-6 min-w-0 flex-1 border border-input bg-field px-2 text-xs text-hi focus-visible:outline-1 focus-visible:outline-ring"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          aria-label={t("path")}
          spellCheck={false}
        />
        <Button type="button" size="sm" variant="outline" title={t("refresh")} onClick={() => void load(path)}>
          ⟳
        </Button>
      </form>
      <div className="flex gap-1 px-3 py-2">
        <Button size="sm" onClick={() => fileInput.current?.click()} disabled={!path}>
          ⤒ {t("upload")}
        </Button>
        <Button size="sm" variant="outline" onClick={() => void mkdir()} disabled={!path}>
          + {t("folder")}
        </Button>
        <input
          ref={fileInput}
          type="file"
          multiple
          className="hidden"
          onChange={(e) => {
            if (e.target.files?.length) void upload(e.target.files);
            e.target.value = "";
          }}
        />
      </div>
      {err && <div className="px-3 pb-2 text-xs text-crit">{err}</div>}
      {uploads.length > 0 && (
        <div className="flex flex-col gap-1 px-3 pb-2 text-xs">
          {uploads.map((u) => (
            <div key={u.key} className="flex items-center gap-2">
              <span className="min-w-0 flex-1 truncate">{u.name}</span>
              {u.error ? (
                <>
                  <span className="text-crit">{u.error}</span>
                  <button type="button" className="text-muted-foreground hover:text-crit" onClick={() => setUploads((x) => x.filter((y) => y.key !== u.key))}>
                    ×
                  </button>
                </>
              ) : (
                <span className="tabular-nums text-muted-foreground">{u.size ? Math.floor((u.sent / u.size) * 100) : 100}%</span>
              )}
            </div>
          ))}
        </div>
      )}
      <ScrollArea className="min-h-0 flex-1">
        <div className="px-1 pb-2 text-sm">
          {entries === null && <div className="px-2 text-muted-foreground">{t("loading…")}</div>}
          {entries?.length === 0 && !err && <div className="px-2 text-muted-foreground">{t("empty folder")}</div>}
          {entries?.map((e) => (
            <div key={e.name} className="group flex items-center gap-2 px-2 py-0.5 hover:bg-[#12361d]">
              {e.dir ? (
                <button type="button" className="min-w-0 flex-1 truncate text-left text-primary" onClick={() => void load(join(path, e.name))} title={e.mode}>
                  ▸ {e.name}/
                </button>
              ) : (
                <a
                  className="min-w-0 flex-1 truncate text-hi hover:text-primary"
                  href={filesApi.downloadURL(serverId, join(path, e.name))}
                  download={e.name}
                  title={`${e.mode} · ${t("download")}`}
                >
                  {"  "}
                  {e.name}
                  {e.link && <span className="text-muted-foreground"> →</span>}
                </a>
              )}
              <span className="shrink-0 tabular-nums text-xs text-muted-foreground">{e.dir ? "" : formatBytes(e.size)}</span>
              <span className="hidden shrink-0 text-xs text-muted-foreground xl:inline">{fmtDate(e.mtime)}</span>
              <span className="flex shrink-0 gap-1 opacity-0 group-focus-within:opacity-100 group-hover:opacity-100">
                <button type="button" className="text-muted-foreground hover:text-primary" aria-label={t("rename")} title={t("rename")} onClick={() => void rename(e)}>
                  ✎
                </button>
                <button type="button" className="text-muted-foreground hover:text-crit" aria-label={t("delete")} title={t("delete")} onClick={() => void remove(e)}>
                  ×
                </button>
              </span>
            </div>
          ))}
          {truncated && <div className="px-2 pt-1 text-xs text-warn">{t("only the first 5000 entries are shown")}</div>}
        </div>
      </ScrollArea>
      {drag && (
        <div className="pointer-events-none absolute inset-0 flex items-center justify-center bg-background/80 text-primary">
          {t("drop files to upload to {path}", { path })}
        </div>
      )}
    </section>
  );
}
