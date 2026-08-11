import { apiClient } from "./client";
import type { CatalogModel } from "../types";

const STATIC_CHANNELS = [
  "claude",
  "gemini",
  "vertex",
  "aistudio",
  "codex",
  "kimi",
  "antigravity",
  "xai",
] as const;

const API_KEY_CHANNELS: Record<string, string> = {
  "gemini-api-key": "gemini",
  "claude-api-key": "claude",
  "codex-api-key": "codex",
  "vertex-api-key": "vertex",
};

interface RawEntry {
  provider?: string;
  models?: unknown;
}

function toStrings(value: unknown): string[] {
  if (value == null) return [];
  if (typeof value === "string") return [value];
  if (Array.isArray(value)) {
    return value
      .map((item) => {
        if (typeof item === "string") return item;
        if (!item || typeof item !== "object") return "";
        const object = item as Record<string, unknown>;
        for (const field of ["model", "id", "name"]) {
          if (typeof object[field] === "string") return object[field] as string;
        }
        return "";
      })
      .filter(Boolean);
  }
  if (typeof value === "object") {
    return Object.keys(value as Record<string, unknown>);
  }
  return [];
}

// CPA may expose the same native model through multiple credentials/providers.
// A key policy can only allow the exact model name; it does not select a
// provider. Collapse duplicates globally and keep one deterministic provider
// label solely to organize the picker.
export function normalizeCatalog(entries: RawEntry[]): CatalogModel[] {
  const byModel = new Map<string, CatalogModel>();
  for (const entry of entries) {
    const provider = String(entry.provider ?? "").trim().toLowerCase();
    if (!provider) continue;
    for (const rawModel of toStrings(entry.models)) {
      const model = rawModel.trim();
      if (!model) continue;
      const key = model.toLowerCase();
      const current = byModel.get(key);
      if (!current || provider.localeCompare(current.provider) < 0) {
        byModel.set(key, { provider, model: current?.model ?? model });
      }
    }
  }
  return Array.from(byModel.values()).sort((a, b) => {
    if (a.provider !== b.provider) return a.provider.localeCompare(b.provider);
    return a.model.toLowerCase().localeCompare(b.model.toLowerCase());
  });
}

function fromOpenAICompat(payload: unknown): RawEntry[] {
  const root = payload as Record<string, unknown> | null;
  const list = root?.["openai-compatibility"];
  if (!Array.isArray(list)) return [];
  return list.map((item) => {
    const object = item as Record<string, unknown> | null;
    return {
      provider: String(object?.name ?? object?.provider ?? object?.id ?? "openai-compat"),
      models: object?.models,
    };
  });
}

interface AuthFileMeta {
  name: string;
  provider: string;
}

function fromAuthFiles(payload: unknown): AuthFileMeta[] {
  const root = payload as Record<string, unknown> | null;
  const list = root?.["auth-files"] ?? root?.files;
  if (!Array.isArray(list)) return [];
  const out: AuthFileMeta[] = [];
  for (const item of list) {
    const object = (item ?? {}) as Record<string, unknown>;
    const name = String(object.name ?? object.id ?? "").trim();
    const provider = String(object.provider ?? object.type ?? "").trim().toLowerCase();
    if (name && provider) out.push({ name, provider });
  }
  return out;
}

function fromAuthFileModels(provider: string, payload: unknown): RawEntry {
  const root = payload as Record<string, unknown> | null;
  return { provider, models: root?.models ?? root?.available_models };
}

function fromModelDefinitions(provider: string, payload: unknown): RawEntry {
  const root = payload as Record<string, unknown> | null;
  return { provider, models: root?.models ?? root?.definitions };
}

function configuredAPIKeyProvider(endpoint: string, payload: unknown): string {
  const provider = API_KEY_CHANNELS[endpoint];
  const root = payload as Record<string, unknown> | null;
  return provider && Array.isArray(root?.[endpoint]) && (root?.[endpoint] as unknown[]).length > 0
    ? provider
    : "";
}

export function filterByConfigured(
  entries: RawEntry[],
  configuredProviders: Set<string>,
): RawEntry[] {
  return entries.filter((entry) => configuredProviders.has(String(entry.provider ?? "").trim().toLowerCase()));
}

export async function fetchCatalog(): Promise<CatalogModel[]> {
  const client = apiClient();
  const entries: RawEntry[] = [];
  const configuredProviders = new Set<string>();

  const safe = async <T>(promise: Promise<{ data: T }>, apply: (data: T) => void | Promise<void>) => {
    try {
      const { data } = await promise;
      await apply(data);
    } catch {
      // One unavailable CPA management source must not blank the whole picker.
    }
  };

  await safe(client.get("/v0/management/openai-compatibility"), (data) => {
    const compatible = fromOpenAICompat(data);
    for (const entry of compatible) {
      const provider = String(entry.provider ?? "").toLowerCase();
      if (provider) configuredProviders.add(provider);
    }
    entries.push(...compatible);
  });

  for (const endpoint of Object.keys(API_KEY_CHANNELS)) {
    await safe(client.get("/v0/management/" + endpoint), (data) => {
      const provider = configuredAPIKeyProvider(endpoint, data);
      if (provider) configuredProviders.add(provider);
    });
  }

  await safe(client.get("/v0/management/auth-files"), async (data) => {
    const files = fromAuthFiles(data);
    for (const file of files) configuredProviders.add(file.provider);
    const models = await Promise.all(
      files.map((file) =>
        client
          .get("/v0/management/auth-files/models", { params: { name: file.name } })
          .then((response) => fromAuthFileModels(file.provider, response.data))
          .catch(() => null),
      ),
    );
    for (const entry of models) if (entry) entries.push(entry);
  });

  for (const provider of STATIC_CHANNELS) {
    await safe(client.get("/v0/management/model-definitions/" + provider), (data) => {
      entries.push(fromModelDefinitions(provider, data));
    });
  }

  return normalizeCatalog(filterByConfigured(entries, configuredProviders));
}

export interface CatalogGroup {
  provider: string;
  models: string[];
}

export function groupByCatalog(catalog: CatalogModel[]): CatalogGroup[] {
  const groups = new Map<string, CatalogGroup>();
  for (const item of catalog) {
    const group = groups.get(item.provider) ?? { provider: item.provider, models: [] };
    group.models.push(item.model);
    groups.set(item.provider, group);
  }
  return Array.from(groups.values()).sort((a, b) => a.provider.localeCompare(b.provider));
}
