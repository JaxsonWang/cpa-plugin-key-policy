import { describe, expect, it } from "vitest";
import { buildModelRules } from "./keys";

describe("buildModelRules", () => {
  it("builds direct model rules without provider routing metadata", () => {
    expect(buildModelRules([
      { provider: "Codex", model: "gpt-5-codex" },
      { provider: "Claude", model: "claude-sonnet-4" },
    ])).toEqual([
      { model: "gpt-5-codex" },
      { model: "claude-sonnet-4" },
    ]);
  });

  it("deduplicates exact models globally and case-insensitively", () => {
    expect(buildModelRules([
      { provider: "codex", model: "GPT-5" },
      { provider: "openai-compat", model: "gpt-5" },
    ])).toEqual([{ model: "GPT-5" }]);
  });

  it("ignores provider validity and skips empty models", () => {
    expect(buildModelRules([
      { provider: "", model: "x" },
      { provider: "p", model: "" },
      { provider: "p", model: "  ok  " },
    ])).toEqual([{ model: "x" }, { model: "ok" }]);
  });
});
