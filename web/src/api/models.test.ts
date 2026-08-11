import { describe, expect, it } from "vitest";
import { filterByConfigured, groupByCatalog, normalizeCatalog } from "./models";

describe("normalizeCatalog", () => {
  it("normalizes strings, object rows, and object maps", () => {
    expect(normalizeCatalog([
      { provider: "OpenAI-Compat", models: ["gpt-4o", { id: "gpt-4o-mini" }] },
      { provider: "Gemini", models: { "gemini-2.5": {} } },
    ])).toEqual([
      { provider: "gemini", model: "gemini-2.5" },
      { provider: "openai-compat", model: "gpt-4o" },
      { provider: "openai-compat", model: "gpt-4o-mini" },
    ]);
  });

  it("deduplicates a model globally across providers", () => {
    expect(normalizeCatalog([
      { provider: "z-provider", models: ["GPT-5"] },
      { provider: "a-provider", models: ["gpt-5"] },
    ])).toEqual([{ provider: "a-provider", model: "GPT-5" }]);
  });

  it("skips empty providers, models, and entries without models", () => {
    expect(normalizeCatalog([
      { provider: "", models: ["x"] },
      { provider: "p", models: ["", "ok"] },
      { provider: "p" },
      { provider: "p", models: null },
    ])).toEqual([{ provider: "p", model: "ok" }]);
  });
});

describe("filterByConfigured", () => {
  it("keeps only models backed by configured providers", () => {
    expect(filterByConfigured([
      { provider: "Claude", models: ["claude-sonnet-4"] },
      { provider: "gemini", models: ["gemini-2.5"] },
    ], new Set(["claude"]))).toEqual([
      { provider: "Claude", models: ["claude-sonnet-4"] },
    ]);
  });
});

describe("groupByCatalog", () => {
  it("groups direct models by display-only provider", () => {
    expect(groupByCatalog([
      { provider: "codex", model: "gpt-5" },
      { provider: "claude", model: "claude-sonnet-4" },
      { provider: "codex", model: "gpt-5-codex" },
    ])).toEqual([
      { provider: "claude", models: ["claude-sonnet-4"] },
      { provider: "codex", models: ["gpt-5", "gpt-5-codex"] },
    ]);
  });
});
