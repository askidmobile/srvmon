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

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const data = await res.json().catch(() => ({}));
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
}

export const api = {
  me: () => request<Me>("GET", "/api/auth/me"),
  password: (password: string) => request<{ next: string }>("POST", "/api/auth/password", { password }),
  totp: (code: string) => request<{ authenticated: boolean }>("POST", "/api/auth/totp", { code }),
  logout: () => request<unknown>("POST", "/api/auth/logout"),
  servers: () => request<ServerView[]>("GET", "/api/servers"),
  server: (id: number) => request<ServerDetailView>("GET", `/api/servers/${id}`),
  history: (id: number, range: HistoryRange) =>
    request<History>("GET", `/api/servers/${id}/history?range=${encodeURIComponent(range)}`),
  updateCard: (id: number, card: ServerCard) => request<ServerView>("PATCH", `/api/servers/${id}`, card),
  probe: (host: string, port: number) => request<ProbeResult>("POST", "/api/servers/probe", { host, port }),
  onboard: (req: OnboardRequest) => request<{ job: string; server_id: number }>("POST", "/api/servers/onboard", req),
  onboardStatus: (job: string, from: number) => request<OnboardStatus>("GET", `/api/onboard/${job}?from=${from}`),
  cancelOnboard: (job: string) => request<unknown>("DELETE", `/api/onboard/${job}`),
  deleteServer: (id: number) => request<unknown>("DELETE", `/api/servers/${id}`),
  containerAction: (id: number, name: string, action: "restart" | "stop" | "start") =>
    request<{ ok: boolean }>("POST", `/api/servers/${id}/containers/${encodeURIComponent(name)}/${action}`),
  alerts: () => request<AlertsView>("GET", "/api/alerts"),
  alertSummary: () => request<AlertSummary>("GET", "/api/alerts/summary"),
  ackAlert: (id: number) => request<Alert>("POST", `/api/alerts/${id}/ack`),
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
};

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

export interface NotifyView {
  telegram: TelegramStatus;
  quiet: Quiet;
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
}

export interface ServerDetailView extends ServerView {
  port: number;
  plan: string;
  created_at: string;
  detail?: Detail;
  detail_at?: string;
  inventory?: Inventory;
  logs: LogLine[];
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

export interface OnboardRequest {
  server_id: number;
  card: NewServerCard;
  user: string;
  password: string;
  host_key: string;
  sudo: boolean;
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
