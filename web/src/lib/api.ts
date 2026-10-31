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
};
