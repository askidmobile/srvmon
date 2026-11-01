import { ApiError, readBody } from "@/lib/api";

// Backups to S3, master key export and emergency SSH keys.

export interface S3Config {
  endpoint: string;
  region: string;
  bucket: string;
  prefix: string;
  access_key: string;
  path_style: boolean;
}

export interface BackupConfig {
  enabled: boolean;
  s3: S3Config;
  interval_hours: number;
  keep: number;
}

export interface BackupStatus {
  attempt_at?: string;
  ok_at?: string;
  object?: string;
  size?: number;
  error?: string;
  running: boolean;
}

export interface BackupView {
  config: BackupConfig;
  saved: boolean;
  has_secret: boolean;
  status: BackupStatus;
  key_exported_at?: string;
}

export interface BackupObject {
  key: string;
  size: number;
  at: string;
}

export interface EmergencyKey {
  line: string;
  type: string;
  fingerprint: string;
  comment: string;
}

export interface EmergencyServer {
  id: number;
  name: string;
  state: "ok" | "pending" | "running" | "failed" | "no_ssh";
  at?: string;
  error?: string;
}

export interface EmergencyView {
  keys: EmergencyKey[];
  servers: EmergencyServer[];
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const data = await readBody(res);
  if (!res.ok) {
    const err = new ApiError(res.status, (data as { error?: string }).error ?? "error");
    const msg = (data as { message?: string }).message;
    if (msg) err.message = msg;
    throw err;
  }
  return data as T;
}

export const backupApi = {
  get: () => req<BackupView>("GET", "/api/backup"),
  save: (config: BackupConfig, secret_key?: string) => req<BackupView>("PUT", "/api/backup", { config, secret_key }),
  test: (config: BackupConfig, secret_key?: string) =>
    req<{ ok: boolean; error?: string }>("POST", "/api/backup/test", { config, secret_key }),
  run: () => req<BackupStatus>("POST", "/api/backup/run"),
  list: () => req<{ backups: BackupObject[] | null; error?: string }>("GET", "/api/backup/list"),
  exportKey: (code: string) => req<{ key: string }>("POST", "/api/backup/master-key", { code }),
  emergency: () => req<EmergencyView>("GET", "/api/access/emergency"),
  saveEmergency: (keys: string[]) => req<EmergencyView>("PUT", "/api/access/emergency", { keys }),
  applyEmergency: () => req<EmergencyView>("POST", "/api/access/emergency/apply"),
};
