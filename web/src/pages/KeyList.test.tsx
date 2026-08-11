import { act } from "react";
import { createRoot } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { KeyPublic } from "../types";
import { _resetLocale } from "../i18n";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock("../api/keys", () => ({
  listKeys: vi.fn(),
  deleteKey: vi.fn(),
  rotateKey: vi.fn(),
  resetRPM: vi.fn(),
  resetAllUsage: vi.fn(),
  setKeyEnabled: vi.fn(),
}));

import { listKeys, resetAllUsage, setKeyEnabled } from "../api/keys";
import KeyList from "./KeyList";

let container: HTMLDivElement;
let root: ReturnType<typeof createRoot>;
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

function key(id: string, enabled = true): KeyPublic {
  return {
    id,
    name: `Key ${id}`,
    enabled,
    key_preview: `cpa_${id}...`,
    rpm: 60,
    models: [{ model: "gpt-5.4" }],
    daily_limit_usd: 10,
    weekly_limit_usd: 50,
    usage: {
      daily_usd: 1,
      weekly_usd: 3,
      daily_limit_usd: 10,
      weekly_limit_usd: 50,
    },
  };
}

async function renderList(keys: KeyPublic[]) {
  vi.mocked(listKeys).mockResolvedValue(keys);
  vi.mocked(setKeyEnabled).mockImplementation(async (id, enabled) => ({
    ...keys.find((item) => item.id === id)!,
    enabled,
  }));
  await act(async () => {
    root = createRoot(container);
    root.render(<MemoryRouter><KeyList /></MemoryRouter>);
    await tick();
  });
}

beforeEach(() => {
  _resetLocale("zh-CN");
  container = document.createElement("div");
  document.body.appendChild(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

describe("KeyList", () => {
  it("renders keys in a desktop table with direct status switches", async () => {
    await renderList([key("team-a"), key("team-b", false)]);

    expect(container.querySelectorAll(".key-table tbody tr")).toHaveLength(2);
    expect(container.textContent).toContain("Key team-a");
    expect(container.textContent).toContain("Key team-b");

    const toggle = container.querySelector<HTMLInputElement>(
      'input[aria-label="切换 Key team-a 状态"]',
    )!;
    await act(async () => {
      toggle.click();
      await tick();
    });

    expect(setKeyEnabled).toHaveBeenCalledWith("team-a", false);
    expect(toggle.checked).toBe(false);
  });

  it("disables selected keys in one batch action and clears successful selections", async () => {
    await renderList([key("team-a"), key("team-b")]);

    const teamA = container.querySelector<HTMLInputElement>('input[aria-label="选择 Key team-a"]')!;
    const teamB = container.querySelector<HTMLInputElement>('input[aria-label="选择 Key team-b"]')!;
    await act(async () => {
      teamA.click();
      teamB.click();
    });

    const batchDisable = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent === "批量禁用")!;
    await act(async () => {
      batchDisable.click();
      await tick();
    });

    expect(setKeyEnabled).toHaveBeenCalledWith("team-a", false);
    expect(setKeyEnabled).toHaveBeenCalledWith("team-b", false);
    expect(teamA.checked).toBe(false);
    expect(teamB.checked).toBe(false);
  });

  it("keeps failed batch items selected for retry", async () => {
    const keys = [key("team-a"), key("team-b")];
    await renderList(keys);
    vi.mocked(setKeyEnabled)
      .mockResolvedValueOnce({ ...keys[0], enabled: false })
      .mockRejectedValueOnce(new Error("persist failed"));

    const selectAll = container.querySelector<HTMLInputElement>('input[aria-label="全选"]')!;
    await act(async () => selectAll.click());
    const batchDisable = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent === "批量禁用")!;
    await act(async () => {
      batchDisable.click();
      await tick();
    });

    expect(container.querySelector<HTMLInputElement>('input[aria-label="选择 Key team-a"]')!.checked).toBe(false);
    expect(container.querySelector<HTMLInputElement>('input[aria-label="选择 Key team-b"]')!.checked).toBe(true);
    expect(container.textContent).toContain("1 个 Key 状态更新失败");
  });

  it("resets daily and weekly usage for all keys from the list header", async () => {
    const confirmMock = vi.spyOn(globalThis, "confirm").mockReturnValue(true);
    vi.mocked(resetAllUsage).mockResolvedValue();
    await renderList([key("team-a"), key("team-b")]);

    const resetUsage = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent === "重置额度")!;
    await act(async () => {
      resetUsage.click();
      await tick();
    });

    expect(confirmMock).toHaveBeenCalledWith(
      "确认将所有 Key 的每日和每周已用额度归零？此操作不可撤销。",
    );
    expect(resetAllUsage).toHaveBeenCalledTimes(1);
    expect(listKeys).toHaveBeenCalledTimes(2);
  });
});
