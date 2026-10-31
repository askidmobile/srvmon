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
    throw new ApiError(res.status, (data as { error?: string }).error ?? "error", retry);
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
  provider: string;
  cost?: number;
  currency: string;
  next_payment: string;
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
};

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
}
