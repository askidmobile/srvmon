// Thin client for srvmon-server. Cookies are HttpOnly; the browser sends them.

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    public retryAfter?: number,
  ) {
    super(code);
  }
}

// Validation errors carry a message for the user.
export function errorMessage(e: unknown, fallback: string): string {
  if (e instanceof ApiError && e.message && e.message !== e.code) return e.message;
  return fallback;
}

// readBody parses a JSON response. A successful response whose body does not
// parse (the connection was cut mid-body, a proxy answered with HTML) is a
// network failure, not an empty object: callers would read fields of {} and
// crash. Error responses may have any body.
export async function readBody(res: Response): Promise<unknown> {
  if (res.status === 204) return {};
  let text: string;
  try {
    text = await res.text();
  } catch {
    throw new ApiError(0, "network");
  }
  try {
    return JSON.parse(text);
  } catch {
    if (res.ok) throw new ApiError(0, "bad_response");
    return {};
  }
}

// Go encodes empty slices as null; the screens read .length on them.
function normalizeServer<T extends ServerView>(v: T): T {
  v.tags ??= [];
  return v;
}

function normalizeDetail(v: ServerDetailView): ServerDetailView {
  normalizeServer(v);
  v.logs ??= [];
  if (v.detail) {
    v.detail.cores ??= [];
    v.detail.procs ??= [];
  }
  return v;
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const data = await readBody(res);
  if (!res.ok) {
    const retry = Number(res.headers.get("Retry-After") ?? "") || undefined;
    const err = new ApiError(res.status, (data as { error?: string }).error ?? "error", retry);
    const msg = (data as { message?: string }).message;
    if (msg) err.message = msg;
    throw err;
  }
  return data as T;
}

export interface Me {
  authenticated: boolean;
  initialized: boolean;
  version: string;
  lang?: string;
}

export interface Container {
  id: string;
  name: string;
  image: string;
  state: string;
  status: string;
  health?: string;
  ports?: string[];
  compose?: string;
  restarts?: number;
  cpu_percent?: number;
  mem_bytes?: number;
}

export interface Report {
  hostname: string;
  agent_version: string;
  ts: string;
  docker_ok: boolean;
  containers?: Container[];
  host: {
    cpu_percent: number;
    cpu_cores: number;
    load1: number;
    load5: number;
    load15: number;
    mem_total: number;
    mem_available: number;
    mem_free?: number;
    mem_cached?: number;
    mem_buffers?: number;
    swap_total: number;
    swap_free: number;
    uptime_sec: number;
    disks?: { mount: string; fs: string; total: number; used: number }[];
    net?: { name: string; rx_bytes: number; tx_bytes: number }[];
  };
}

export interface ServerView {
  id: number;
  name: string;
  description: string;
  project: string;
  group: string;
  tags: string[];
  host: string;
  port: number;
  provider: string;
  plan: string;
  cost?: number;
  currency: string;
  next_payment: string;
  period: string;
  panel_url: string;
  ssh_user: string;
  has_ssh: boolean;
  last_seen?: string;
  online: boolean;
  snapshot?: { received: string; report: Report };
  // The agent reports another version than the panel (agent_latest).
  agent_outdated?: boolean;
  agent_latest?: string;
  agent_update?: AgentUpdate;
  // Hours until a growing filesystem is full, by mount point (≤ 30 days).
  disk_full_hours?: Record<string, number>;
  // HTTP/TCP checks of the server, when it has any.
  checks?: { total: number; down: number };
}

export interface AgentUpdate {
  state: "running" | "ok" | "failed";
  image: string;
  error?: string;
  at: string;
}

export const api = {
  me: () => request<Me>("GET", "/api/auth/me"),
  password: (password: string) => request<{ next: string }>("POST", "/api/auth/password", { password }),
  totp: (code: string) => request<{ authenticated: boolean }>("POST", "/api/auth/totp", { code }),
  setLanguage: (lang: string) => request<{ lang: string }>("PUT", "/api/settings/lang", { lang }),
  logout: () => request<unknown>("POST", "/api/auth/logout"),
  servers: () => request<ServerView[] | null>("GET", "/api/servers").then((l) => (l ?? []).map(normalizeServer)),
  server: (id: number) => request<ServerDetailView>("GET", `/api/servers/${id}`).then(normalizeDetail),
  history: (id: number, range: HistoryRange) =>
    request<History>("GET", `/api/servers/${id}/history?range=${encodeURIComponent(range)}`),
  updateCard: (id: number, card: ServerCard) => request<ServerView>("PATCH", `/api/servers/${id}`, card),
  setupKey: () => request<SetupKey>("POST", "/api/servers/setup-key", {}),
  probe: (host: string, port: number) => request<ProbeResult>("POST", "/api/servers/probe", { host, port }),
  onboard: (req: OnboardRequest) => request<{ job: string; server_id: number }>("POST", "/api/servers/onboard", req),
  onboardStatus: (job: string, from: number) => request<OnboardStatus>("GET", `/api/onboard/${job}?from=${from}`),
  cancelOnboard: (job: string) => request<unknown>("DELETE", `/api/onboard/${job}`),
  deleteServer: (id: number) => request<unknown>("DELETE", `/api/servers/${id}`),
  containerAction: (id: number, name: string, action: "restart" | "stop" | "start") =>
    request<{ ok: boolean }>("POST", `/api/servers/${id}/containers/${encodeURIComponent(name)}/${action}`),
  updateAgent: (id: number) => request<{ started: boolean }>("POST", `/api/servers/${id}/agent/update`),
  agentSmart: (id: number, enabled: boolean) =>
    request<{ started: boolean }>("POST", `/api/servers/${id}/agent/smart`, { enabled }),
  containerHistory: (id: number, name: string, range: HistoryRange) =>
    request<ContainerHistory>(
      "GET",
      `/api/servers/${id}/containers/${encodeURIComponent(name)}/history?range=${encodeURIComponent(range)}`,
    ),
  updateAgents: () => request<{ started: number }>("POST", "/api/servers/agents/update"),
  sudo: (id: number) => request<SudoState>("GET", `/api/servers/${id}/sudo`),
  setSudo: (id: number, mode: SudoMode) => request<SudoState>("PUT", `/api/servers/${id}/sudo`, { mode }),
  alerts: () => request<AlertsView>("GET", "/api/alerts"),
  alertSummary: () => request<AlertSummary>("GET", "/api/alerts/summary"),
  ackAlert: (id: number) => request<Alert>("POST", `/api/alerts/${id}/ack`),
  diagnoseAlert: (id: number) => request<Diagnosis>("POST", `/api/alerts/${id}/diagnose`),
  silenceAlert: (id: number, minutes: number) =>
    request<{ alert: Alert; until: string }>("POST", `/api/alerts/${id}/silence`, { minutes }),
  rules: () => request<Rule[]>("GET", "/api/alert-rules"),
  createRule: (r: Omit<Rule, "id">) => request<Rule>("POST", "/api/alert-rules", r),
  updateRule: (r: Rule) => request<Rule>("PUT", `/api/alert-rules/${r.id}`, r),
  deleteRule: (id: number) => request<unknown>("DELETE", `/api/alert-rules/${id}`),
  notify: () => request<NotifyView>("GET", "/api/notify"),
  saveTelegram: (chat_id: string, token?: string) =>
    request<TelegramStatus>("PUT", "/api/notify/telegram", token === undefined ? { chat_id } : { chat_id, token }),
  testTelegram: () => request<unknown>("POST", "/api/notify/telegram/test"),
  saveQuiet: (q: Quiet) => request<Quiet>("PUT", "/api/notify/quiet", q),
  saveHeartbeat: (fail_endpoint: boolean, url?: string) =>
    request<HeartbeatStatus>("PUT", "/api/notify/heartbeat", url === undefined ? { fail_endpoint } : { fail_endpoint, url }),
  testHeartbeat: () => request<HeartbeatStatus>("POST", "/api/notify/heartbeat/test"),
  createChannel: (c: ChannelInput & { url: string }) => request<NotifyChannel>("POST", "/api/notify/channels", c),
  updateChannel: (id: number, c: ChannelInput) => request<NotifyChannel>("PUT", `/api/notify/channels/${id}`, c),
  deleteChannel: (id: number) => request<unknown>("DELETE", `/api/notify/channels/${id}`),
  testChannel: (id: number) => request<unknown>("POST", `/api/notify/channels/${id}/test`),
  checks: (id: number) => request<ChecksView>("GET", `/api/servers/${id}/checks`).then(normalizeChecks),
  saveChecks: (id: number, targets: string[]) =>
    request<ChecksView>("PUT", `/api/servers/${id}/checks`, { targets }).then(normalizeChecks),
};

// --- HTTP/TCP/TLS checks from the panel ---

export interface CheckResult {
  up: boolean;
  latency_ms: number;
  status?: number;
  error?: string;
  checked_at: string;
  down_since?: string;
  cert?: { subject: string; issuer: string; not_after: string };
}

export interface CheckState {
  id: number;
  target: string;
  kind: "http" | "tcp";
  tls: boolean;
  domain?: string;
  result?: CheckResult;
}

export interface DomainInfo {
  domain: string;
  expires?: string;
  source?: "rdap" | "whois";
  error?: string;
  checked_at: string;
}

export interface ChecksView {
  checks: CheckState[];
  domains: DomainInfo[];
}

function normalizeChecks(v: ChecksView): ChecksView {
  v.checks ??= [];
  v.domains ??= [];
  return v;
}

// --- stage 4: alerts and Telegram ---

export type Severity = "crit" | "warn" | "info";
export type Channel = "tg" | "web";

export interface Alert {
  id: number;
  rule_id: number;
  kind: string;
  server_id: number;
  server_name: string;
  subject: string;
  severity: Severity;
  channel: Channel;
  state: "firing" | "acked" | "resolved" | "event";
  message: string;
  started_at: string;
  acked_at?: string;
  resolved_at?: string;
  notified: boolean;
  diagnosis?: Diagnosis;
}

// The AI agent's read-only investigation of an alert.
export interface Diagnosis {
  alert_id: number;
  state: "queued" | "running" | "done" | "error" | "skipped";
  trigger: "auto" | "manual";
  text: string;
  tools: number;
  started_at: string;
  finished_at?: string;
}

export interface AlertsView {
  open: Alert[];
  recent: Alert[];
  last24h: number;
}

export interface AlertSummary {
  open: number;
  crit: number;
  last24h: number;
  telegram: "ok" | "off" | "error";
  channels: number; // enabled Shoutrrr channels
  channels_failing: number;
  latest: Alert[];
}

export interface Rule {
  id: number;
  kind: string;
  enabled: boolean;
  threshold: number;
  duration_sec: number;
  severity: Severity;
  channel: Channel;
}

export interface TelegramStatus {
  configured: boolean;
  chat_id: string;
  connected: boolean;
  bot_name?: string;
  last_error?: string;
  last_message?: string;
  last_sent?: string;
  pending_chat?: { id: string; title: string; at: string };
}

export interface Quiet {
  from: string;
  to: string;
  tz: string;
}

// Dead man's switch: the panel pings an external URL once a minute.
export interface HeartbeatStatus {
  configured: boolean;
  target?: string; // URL with its secret part hidden
  fail_endpoint: boolean;
  last_ping?: string;
  last_result?: "ok" | "fail" | "skipped" | "error";
  last_error?: string;
  problems?: string[];
}

// A Shoutrrr notification channel; the URL (with its credentials) never
// comes back from the server.
export interface NotifyChannel {
  id: number;
  name: string;
  service: string;
  target: string;
  min_severity: Severity;
  enabled: boolean;
  last_sent?: string;
  last_error?: string;
}

export interface ChannelInput {
  name: string;
  url?: string; // absent on update: keep the stored one
  min_severity: Severity;
  enabled: boolean;
}

export interface NotifyView {
  telegram: TelegramStatus;
  quiet: Quiet;
  heartbeat?: HeartbeatStatus;
  channels: NotifyChannel[];
  services: string[];
  channels_ok: boolean; // false: no master key to seal channel URLs
}

// --- server screen (stage 2) ---

export interface Process {
  pid: number;
  name: string;
  cmd?: string;
  user?: string;
  cpu: number;
  mem: number;
  container?: string;
}

export interface LogLine {
  ts: string;
  level: "ERR" | "WARN" | string;
  source: string;
  msg: string;
}

export interface Detail {
  cpu_user: number;
  cpu_system: number;
  cpu_iowait: number;
  cpu_steal: number;
  cores: number[];
  temp_c?: number;
  disk_read_bps: number;
  disk_write_bps: number;
  tcp_conns: number;
  procs_total: number;
  procs: Process[];
}

export interface Inventory {
  os?: string;
  kernel?: string;
  cpu_model?: string;
  docker_version?: string;
  ips?: string[];
  // The agent runs with S.M.A.R.T. reads on.
  smart?: boolean;
}

// S.M.A.R.T. state of one disk; fields the disk does not report are absent.
export interface DiskHealth {
  device: string;
  type?: "ata" | "nvme";
  model?: string;
  serial?: string;
  passed?: boolean;
  temp_c?: number;
  power_on_hours?: number;
  reallocated?: number;
  pending?: number;
  uncorrectable?: number;
  percent_used?: number;
  available_spare?: number;
  spare_threshold?: number;
  media_errors?: number;
  critical_warning?: number;
  // standby | unsupported | permission
  error?: string;
}

export interface ServerDetailView extends ServerView {
  port: number;
  plan: string;
  created_at: string;
  detail?: Detail;
  detail_at?: string;
  inventory?: Inventory;
  logs: LogLine[];
  smart?: DiskHealth[];
  smart_at?: string;
}

export type HistoryRange = "15m" | "1h" | "6h" | "24h" | "7d";
export const historyRanges: HistoryRange[] = ["15m", "1h", "6h", "24h", "7d"];

export interface History {
  range: HistoryRange;
  start: string;
  step_sec: number;
  cpu: (number | null)[];
  rx: (number | null)[];
  tx: (number | null)[];
}

export interface ContainerHistory {
  range: HistoryRange;
  container: string;
  start: string;
  step_sec: number;
  cpu: (number | null)[];
  mem: (number | null)[];
}

export interface ServerCard {
  description: string;
  project: string;
  group: string;
  tags: string[];
  provider: string;
  plan: string;
  cost: number | null;
  currency: string;
  next_payment: string;
  // Left out by the passport editor: the server keeps the stored value.
  period?: string;
  panel_url?: string;
}

// --- stage 3: add wizard and terminal ---

export interface NewServerCard extends ServerCard {
  name: string;
  host: string;
  port: number;
  period: string;
  panel_url: string;
}

export interface ProbeResult {
  host_key: string;
  fingerprint: string;
  type: string;
}

export interface SetupKey {
  id: string;
  public_key: string;
  fingerprint: string;
  command: string;
}

// Sudo of the panel's SSH user on a server ("custom": rules srvmon did not write).
export type SudoMode = "none" | "limited" | "full";
export interface SudoState {
  mode: SudoMode | "custom";
  user: string;
}

export interface OnboardRequest {
  server_id: number;
  card: NewServerCard;
  user: string;
  method: "password" | "key" | "panel_key";
  password: string; // with a key: the sudo password, may be empty
  private_key: string;
  passphrase: string;
  setup_key: string;
  host_key: string;
  sudo_mode: SudoMode;
  disable_password: boolean;
  install_agent: boolean;
}

export interface OnboardLine {
  state: "ok" | "run" | "warn" | "fail" | "info";
  text: string;
  ts: number;
}

export interface OnboardStatus {
  lines: OnboardLine[];
  next: number;
  done: boolean;
  ok: boolean;
  committed: boolean;
  server_id: number;
}

export function terminalURL(serverId: number, cols: number, rows: number, container?: string): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const q = new URLSearchParams({ cols: String(cols), rows: String(rows) });
  if (container) q.set("container", container);
  return `${proto}//${location.host}/api/servers/${serverId}/terminal?${q}`;
}

// --- terminal recordings ---

export interface Recording {
  id: number;
  server_id: number;
  server_name: string;
  target: string;
  ip: string;
  cols: number;
  rows: number;
  started_at: string;
  ended_at?: string;
  bytes: number;
  truncated: boolean;
}

export interface RecSettings {
  enabled: boolean;
  retention_days: number;
  max_mb: number;
}

export interface RecSettingsView extends RecSettings {
  available: boolean;
  count: number;
  disk_bytes: number;
}

export const recApi = {
  // A page of 100, newest first; before = the last id of the previous page.
  list: (server?: number, before?: number) => {
    const q = new URLSearchParams();
    if (server) q.set("server", String(server));
    if (before) q.set("before", String(before));
    return request<Recording[] | null>("GET", `/api/recordings${q.size ? `?${q}` : ""}`).then((l) => l ?? []);
  },
  servers: () =>
    request<{ id: number; name: string; count: number }[] | null>("GET", "/api/recordings/servers").then((l) => l ?? []),
  get: (id: number) => request<Recording>("GET", `/api/recordings/${id}`),
  remove: (id: number) => request<unknown>("DELETE", `/api/recordings/${id}`),
  removeAll: () => request<{ deleted: number }>("DELETE", "/api/recordings"),
  settings: () => request<RecSettingsView>("GET", "/api/recordings/settings"),
  saveSettings: (s: RecSettings) => request<RecSettingsView>("PUT", "/api/recordings/settings", s),
  castURL: (id: number, download = false) => `/api/recordings/${id}/cast${download ? "?download=1" : ""}`,
  cast: async (id: number): Promise<string> => {
    const res = await fetch(`/api/recordings/${id}/cast`, { credentials: "same-origin" });
    if (!res.ok) {
      const data = (await readBody(res)) as { error?: string };
      throw new ApiError(res.status, data.error ?? "error");
    }
    return res.text();
  },
};

// --- files over SFTP ---

export interface FileEntry {
  name: string;
  size: number;
  mode: string;
  mtime: string;
  dir: boolean;
  link?: boolean;
}

export interface FileList {
  path: string;
  entries: FileEntry[] | null;
  truncated?: boolean;
}

// Uploads go in chunks below the reverse proxy's body limit.
const UPLOAD_CHUNK = 4 << 20;

export const filesApi = {
  list: (id: number, path?: string) =>
    request<FileList>("GET", `/api/servers/${id}/files${path ? `?path=${encodeURIComponent(path)}` : ""}`).then(
      (l) => ({ ...l, entries: l.entries ?? [] }),
    ),
  downloadURL: (id: number, path: string) => `/api/servers/${id}/files/download?path=${encodeURIComponent(path)}`,
  mkdir: (id: number, path: string) => request<unknown>("POST", `/api/servers/${id}/files/mkdir`, { path }),
  rename: (id: number, from: string, to: string) =>
    request<unknown>("POST", `/api/servers/${id}/files/rename`, { from, to }),
  remove: (id: number, path: string, recursive: boolean) =>
    request<unknown>("POST", `/api/servers/${id}/files/delete`, { path, recursive }),
  upload: async (
    id: number,
    path: string,
    file: File,
    overwrite: boolean,
    onProgress: (sent: number) => void,
    signal?: AbortSignal,
  ) => {
    // A fresh id per upload keeps two uploads of the same name apart.
    const rnd = new Uint8Array(16);
    crypto.getRandomValues(rnd);
    const upload = Array.from(rnd, (b) => b.toString(16).padStart(2, "0")).join("");
    let offset = 0;
    try {
      do {
        const chunk = file.slice(offset, offset + UPLOAD_CHUNK);
        const final = offset + chunk.size >= file.size;
        const q = new URLSearchParams({ path, upload, offset: String(offset) });
        if (final) q.set("final", "1");
        if (overwrite) q.set("overwrite", "1");
        const res = await fetch(`/api/servers/${id}/files/upload?${q}`, {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/octet-stream" },
          body: chunk,
          signal,
        });
        const data = (await readBody(res)) as { error?: string; message?: string };
        if (!res.ok) {
          const err = new ApiError(res.status, data.error ?? "error");
          if (data.message) err.message = data.message;
          throw err;
        }
        offset += chunk.size;
        onProgress(offset);
      } while (offset < file.size);
    } catch (e) {
      // Drop the half-written part file on the server (best effort).
      const q = new URLSearchParams({ path, upload });
      void fetch(`/api/servers/${id}/files/upload/abort?${q}`, { method: "POST", credentials: "same-origin" }).catch(
        () => undefined,
      );
      throw e;
    }
  },
};
