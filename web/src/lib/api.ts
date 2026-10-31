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
    uptime_sec: number;
    disks?: { mount: string; total: number; used: number }[];
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
  probe: (host: string, port: number) => request<ProbeResult>("POST", "/api/servers/probe", { host, port }),
  onboard: (req: OnboardRequest) => request<{ job: string; server_id: number }>("POST", "/api/servers/onboard", req),
  onboardStatus: (job: string, from: number) => request<OnboardStatus>("GET", `/api/onboard/${job}?from=${from}`),
  cancelOnboard: (job: string) => request<unknown>("DELETE", `/api/onboard/${job}`),
  deleteServer: (id: number) => request<unknown>("DELETE", `/api/servers/${id}`),
  containerAction: (id: number, name: string, action: "restart" | "stop" | "start") =>
    request<{ ok: boolean }>("POST", `/api/servers/${id}/containers/${encodeURIComponent(name)}/${action}`),
};

// --- stage 3: add wizard and terminal ---

export interface ServerCard {
  name: string;
  host: string;
  port: number;
  project: string;
  group: string;
  tags: string[];
  description: string;
  provider: string;
  plan: string;
  cost: number | null;
  currency: string;
  period: string;
  next_payment: string;
  panel_url: string;
}

export interface ProbeResult {
  host_key: string;
  fingerprint: string;
  type: string;
}

export interface OnboardRequest {
  server_id: number;
  card: ServerCard;
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
