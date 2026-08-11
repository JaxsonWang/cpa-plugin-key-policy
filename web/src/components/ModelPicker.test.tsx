import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ModelRule } from "../types";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock("../api/models", async () => {
  const actual = await vi.importActual<typeof import("../api/models")>("../api/models");
  return { ...actual, fetchCatalog: vi.fn() };
});

import { fetchCatalog, normalizeCatalog } from "../api/models";
import ModelPicker from "./ModelPicker";

let container: HTMLDivElement;
let root: ReturnType<typeof createRoot>;
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

beforeEach(() => {
  container = document.createElement("div");
  document.body.appendChild(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

describe("ModelPicker", () => {
  it("does not emit before the catalog loads", async () => {
    let resolveCatalog: (value: { provider: string; model: string }[]) => void = () => {};
    vi.mocked(fetchCatalog).mockImplementation(() => new Promise((resolve) => { resolveCatalog = resolve; }));
    const initial: ModelRule[] = [{ model: "gpt-5.4", input_price_per_million: 2 }];
    const calls: ModelRule[][] = [];

    await act(async () => {
      root = createRoot(container);
      root.render(<ModelPicker initial={initial} onChange={(rules) => calls.push(rules)} />);
      await tick();
    });
    expect(calls).toEqual([]);

    await act(async () => {
      resolveCatalog([{ provider: "codex", model: "gpt-5.4" }]);
      await tick();
    });
    expect(calls).toEqual([[{ model: "gpt-5.4" }]]);
  });

  it("emits direct model rules when selection changes", async () => {
    vi.mocked(fetchCatalog).mockResolvedValue([
      { provider: "codex", model: "gpt-5.4" },
      { provider: "claude", model: "claude-sonnet-4" },
    ]);
    const calls: ModelRule[][] = [];

    await act(async () => {
      root = createRoot(container);
      root.render(<ModelPicker initial={[]} onChange={(rules) => calls.push(rules)} />);
      await tick();
    });

    const checkboxes = Array.from(container.querySelectorAll("input[type=checkbox]")) as HTMLInputElement[];
    expect(checkboxes).toHaveLength(2);
    await act(async () => {
      checkboxes[1].click();
      await tick();
    });
    expect(calls.at(-1)).toEqual([{ model: "gpt-5.4" }]);
  });

  it("shows and emits a model only once when multiple providers expose it", async () => {
    vi.mocked(fetchCatalog).mockResolvedValue(normalizeCatalog([
      { provider: "z-provider", models: ["gpt-5.4"] },
      { provider: "a-provider", models: ["GPT-5.4"] },
    ]));
    const calls: ModelRule[][] = [];

    await act(async () => {
      root = createRoot(container);
      root.render(<ModelPicker initial={[]} onChange={(rules) => calls.push(rules)} />);
      await tick();
    });

    const checkboxes = Array.from(container.querySelectorAll("input[type=checkbox]")) as HTMLInputElement[];
    expect(checkboxes).toHaveLength(1);
    await act(async () => {
      checkboxes[0].click();
      await tick();
    });
    expect(calls.at(-1)).toEqual([{ model: "gpt-5.4" }]);
  });
});
