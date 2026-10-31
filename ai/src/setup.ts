// Model setup of srvmon-ai: provider, address, key and models. srvmon keeps
// it (Settings → AI in the panel) and posts it to /v1/config; at start
// srvmon-ai asks srvmon for it and falls back to SRVMON_AI_* variables.
import type { ModelRuntime } from "@earendil-works/pi-coding-agent";

export type Thinking = "off" | "minimal" | "low" | "medium" | "high";
const thinkingLevels: Thinking[] = ["off", "minimal", "low", "medium", "high"];

export interface AIConfig {
  provider: string;
  base_url?: string;
  api?: string;
  api_key?: string;
  model: string;
  thinking?: string;
  models?: string[];
}

export interface ModelInfo {
  id: string;
  name?: string;
  catalog: boolean;
  live: boolean;
  reasoning?: boolean;
  context?: number;
}

/** Custom endpoints speak one of these wire protocols. */
export const customAPIs = ["openai-completions", "openai-responses", "anthropic-messages"];
export const CUSTOM = "custom";

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AnyModel = any;

const zeroCost = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };

/** A model config for an id pi's catalog does not know. */
function modelConfig(id: string, api: string | undefined, template?: AnyModel) {
  if (template) {
    // eslint-disable-next-line @typescript-eslint/no-unused-vars
    const { provider, type, ...rest } = template;
    return { ...rest, id, name: id };
  }
  return {
    id,
    name: id,
    api,
    reasoning: false,
    input: ["text"],
    cost: zeroCost,
    contextWindow: 128_000,
    maxTokens: 16_384,
  };
}

export class Setup {
  provider = "";
  modelId = "";
  thinking: Thinking = "low";
  allowed: string[] = [];
  error: string | undefined;
  private key = "";
  private overridden = new Set<string>(); // providers registered by us
  private keyed = new Set<string>(); // providers whose runtime key we set
  /** Bumped on every apply: sessions of an older generation are reopened. */
  generation = 0;

  constructor(private rt: ModelRuntime) {}

  /** The last setup that applied cleanly; restored when a new one fails. */
  private last: AIConfig | undefined;
  /** Built-in providers as pi ships them, saved before we override them. */
  private originals = new Map<string, { baseUrl: string; catalog: readonly AnyModel[] }>();

  async apply(cfg: AIConfig): Promise<void> {
    const provider = (cfg.provider || "").trim();
    const modelId = (cfg.model || "").trim();
    // Check what can be checked before touching the runtime, so a bad setup
    // leaves the working one in place.
    if (!provider || !modelId) throw new Error("provider и model обязательны");
    if (provider === CUSTOM) {
      if (!(cfg.base_url ?? "").trim()) throw new Error("для своего провайдера нужен URL");
    } else if (!this.rt.getProvider(provider)) {
      throw new Error(`провайдер ${provider} неизвестен`);
    }
    try {
      await this.applyNow(cfg, provider, modelId);
      this.last = cfg;
    } catch (err) {
      const prev = this.last;
      if (prev) {
        await this.applyNow(prev, prev.provider.trim(), prev.model.trim()).catch(() => undefined);
      }
      throw err;
    }
  }

  private async applyNow(cfg: AIConfig, provider: string, modelId: string): Promise<void> {
    const thinking = thinkingLevels.includes(cfg.thinking as Thinking) ? (cfg.thinking as Thinking) : "low";
    const allowed = [...new Set([modelId, ...(cfg.models ?? [])].map((m) => m.trim()).filter(Boolean))];
    const key = (cfg.api_key ?? "").trim();
    const baseUrl = (cfg.base_url ?? "").trim().replace(/\/+$/, "");

    // Drop what the previous setup registered.
    for (const p of this.overridden) this.rt.unregisterProvider(p);
    this.overridden.clear();
    for (const p of this.keyed) await this.rt.removeRuntimeApiKey(p).catch(() => undefined);
    this.keyed.clear();

    let error: string | undefined;
    if (provider === CUSTOM) {
      const api = customAPIs.includes(cfg.api ?? "") ? cfg.api : "openai-completions";
      this.rt.registerProvider(CUSTOM, {
        name: "Свой провайдер",
        baseUrl,
        api: api as never,
        // Local servers often need no key; pi still wants one.
        apiKey: key || "none",
        models: allowed.map((id) => modelConfig(id, api)) as never,
      });
      this.overridden.add(CUSTOM);
    } else {
      const p = this.rt.getProvider(provider);
      if (!p) throw new Error(`провайдер ${provider} неизвестен`);
      const catalog = this.rt.getModels(provider);
      if (!this.originals.has(provider)) {
        this.originals.set(provider, { baseUrl: p.baseUrl ?? catalog[0]?.baseUrl ?? "", catalog });
      }
      const missing = allowed.filter((id) => !catalog.some((m) => m.id === id));
      if (baseUrl || missing.length) {
        // Registering models replaces the catalog: keep it and add ours.
        const template = catalog.find((m) => m.id === modelId) ?? catalog[0];
        const models = missing.length
          ? [...catalog.map((m) => modelConfig(m.id, m.api, m)), ...missing.map((id) => modelConfig(id, template?.api, template))]
          : undefined;
        if (models && baseUrl) for (const m of models) m.baseUrl = baseUrl;
        this.rt.registerProvider(provider, { ...(baseUrl ? { baseUrl } : {}), ...(models ? { models: models as never } : {}) });
        this.overridden.add(provider);
      }
      if (key) {
        await this.rt.setRuntimeApiKey(provider, key);
        this.keyed.add(provider);
      }
    }

    if (!this.rt.getModel(provider, modelId)) error = `модель ${provider}/${modelId} не найдена`;
    else {
      const available = await this.rt.getAvailable(provider).catch(() => []);
      if (!available.some((m: { id: string }) => m.id === modelId)) {
        error = `нет API-ключа для ${provider}`;
      }
    }
    Object.assign(this, { provider, modelId, thinking, allowed, key, error });
    this.generation++;
  }

  /** The model for a run: the chat's own if allowed, else the default. */
  resolve(id?: string): AnyModel | undefined {
    if (id && this.allowed.includes(id)) {
      const m = this.rt.getModel(this.provider, id);
      if (m) return m;
    }
    return this.rt.getModel(this.provider, this.modelId);
  }

  /** The key for listing models of the current provider. */
  currentKey(provider: string) {
    return provider === this.provider ? this.key : "";
  }

  providers() {
    const list = this.rt
      .getProviders()
      .filter((p) => p.id !== CUSTOM && p.getModels().length > 0)
      .map((p) => ({ id: p.id, name: p.name, base_url: p.baseUrl ?? p.getModels()[0]?.baseUrl ?? "", models: p.getModels().length }));
    list.sort((a, b) => a.name.localeCompare(b.name));
    return list;
  }

  /** Models of a provider: pi's catalog merged with the provider's own list. */
  async listModels(req: { provider: string; base_url?: string; api?: string; api_key?: string }) {
    const provider = req.provider;
    const out = new Map<string, ModelInfo>();
    let base = (req.base_url ?? "").trim().replace(/\/+$/, "");
    let api = req.api ?? "";
    if (provider !== CUSTOM) {
      const p = this.rt.getProvider(provider);
      if (!p) return { models: [], live: false, error: `провайдер ${provider} неизвестен` };
      // While we override the provider, the runtime holds our address and
      // models: list against pi's own ones.
      const orig = this.overridden.has(provider) ? this.originals.get(provider) : undefined;
      const catalog = orig?.catalog ?? this.rt.getModels(provider);
      for (const m of catalog) {
        out.set(m.id, { id: m.id, name: m.name, catalog: true, live: false, reasoning: m.reasoning, context: m.contextWindow });
      }
      base ||= orig?.baseUrl ?? p.baseUrl ?? catalog[0]?.baseUrl ?? "";
      api ||= catalog[0]?.api ?? "";
    }
    const key = req.api_key || this.currentKey(provider);
    let error: string | undefined;
    let live = false;
    if (!base) error = "у провайдера нет адреса API";
    else {
      try {
        for (const id of await fetchModelIds(provider, base, api, key)) {
          const cur = out.get(id);
          if (cur) cur.live = true;
          else out.set(id, { id, catalog: false, live: true });
        }
        live = true;
      } catch (err) {
        error = err instanceof Error ? err.message : String(err);
      }
    }
    const models = [...out.values()].sort((a, b) => Number(b.live) - Number(a.live) || a.id.localeCompare(b.id));
    return { models, live, error };
  }
}

/** Asks the provider for its model ids. */
export async function fetchModelIds(provider: string, base: string, api: string, key: string): Promise<string[]> {
  const signal = AbortSignal.timeout(15_000);
  const anthropic = provider === "anthropic" || api.startsWith("anthropic") && (provider === CUSTOM || base.endsWith("/anthropic"));
  const google = provider === "google" || api.startsWith("google");
  let urls: string[];
  const headers: Record<string, string> = { Accept: "application/json" };
  if (anthropic) {
    urls = [`${base}/v1/models?limit=1000`];
    if (key) headers["x-api-key"] = key;
    headers["anthropic-version"] = "2023-06-01";
  } else if (google) {
    urls = [`${base}/models?pageSize=1000${key ? `&key=${encodeURIComponent(key)}` : ""}`];
  } else {
    urls = /\/v\d+[a-z]*$/.test(base) ? [`${base}/models`] : [`${base}/v1/models`, `${base}/models`];
    if (key) headers.Authorization = `Bearer ${key}`;
  }
  let last = "";
  for (const url of urls) {
    const res = await fetch(url, { headers, signal });
    if (res.status === 404) {
      last = "провайдер не отдаёт список моделей (404)";
      continue;
    }
    if (res.status === 401 || res.status === 403) throw new Error("провайдер отклонил ключ API");
    if (!res.ok) throw new Error(`провайдер ответил ${res.status}`);
    const body = (await res.json()) as { data?: { id?: string; name?: string }[]; models?: { id?: string; name?: string }[] };
    const items = body.data ?? body.models ?? [];
    const ids = items.map((m) => m.id ?? m.name?.replace(/^models\//, "") ?? "").filter(Boolean);
    return [...new Set(ids)];
  }
  throw new Error(last || "список моделей недоступен");
}
