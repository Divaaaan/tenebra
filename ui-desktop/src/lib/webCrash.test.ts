import { describe, expect, it, vi, beforeEach } from "vitest";

vi.mock("../api", () => ({
  api: { recordWebCrash: vi.fn() },
}));

import { api } from "../api";
import { reportWebCrash, __resetWebCrashGuard } from "./webCrash";

const mockedRecord = vi.mocked(api.recordWebCrash);

describe("reportWebCrash", () => {
  beforeEach(() => {
    __resetWebCrashGuard();
    localStorage.clear();
    // restoreMocks wipes the implementation between tests; re-arm it so the
    // internal `.catch` has a promise to chain.
    mockedRecord.mockResolvedValue(undefined);
  });

  it("writes a localStorage breadcrumb and forwards to the core", () => {
    reportWebCrash("boom", "stack trace here");

    const marker = JSON.parse(localStorage.getItem("tenebra.webCrash") ?? "{}");
    expect(marker.message).toBe("boom");
    expect(typeof marker.ts).toBe("number");
    expect(mockedRecord).toHaveBeenCalledWith("boom", "stack trace here");
  });

  it("debounces to the first report per session", () => {
    reportWebCrash("first", "s1");
    reportWebCrash("second", "s2");

    expect(mockedRecord).toHaveBeenCalledTimes(1);
    expect(mockedRecord).toHaveBeenCalledWith("first", "s1");
  });

  it("caps the stack excerpt handed to the core", () => {
    reportWebCrash("boom", "x".repeat(9000));

    const [, stack] = mockedRecord.mock.calls[0];
    expect(stack.length).toBe(4000);
  });
});
