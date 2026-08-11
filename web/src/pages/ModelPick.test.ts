import { describe, expect, it } from "vitest";
import { buildPickedModels } from "./ModelPick";

describe("buildPickedModels", () => {
  it("preserves pricing for models that were already selected", () => {
    expect(buildPickedModels(
      new Set(["gpt-5.4", "new-model"]),
      new Map([["gpt-5.4", "gpt-5.4"], ["new-model", "New-Model"]]),
      [{ model: "gpt-5.4", billing_mode: "tokens", input_price_per_million: 2 }],
    )).toEqual([
      { model: "gpt-5.4", billing_mode: "tokens", input_price_per_million: 2 },
      { model: "New-Model" },
    ]);
  });
});
