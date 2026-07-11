import { describe, expect, it, vi, beforeEach } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";

vi.mock("../api", () => ({
  api: { checkCrashReport: vi.fn() },
}));

import { api } from "../api";
import { useCrashReport } from "./useCrashReport";

const mockedCheck = vi.mocked(api.checkCrashReport);
const report = { text: "boom report", signature: "sig-1" };

describe("useCrashReport", () => {
  beforeEach(() => {
    localStorage.clear();
    mockedCheck.mockReset();
    mockedCheck.mockResolvedValue(report);
  });

  it("surfaces a fresh report once ready and consented", async () => {
    const { result } = renderHook(() => useCrashReport(true, true));
    await waitFor(() => expect(result.current.report).toEqual(report));
  });

  it("does not check until ready", () => {
    const { result } = renderHook(() => useCrashReport(true, false));
    expect(mockedCheck).not.toHaveBeenCalled();
    expect(result.current.report).toBeNull();
  });

  it("hides the report when consent is off, even if one exists", async () => {
    const { result } = renderHook(() => useCrashReport(false, true));
    await waitFor(() => expect(mockedCheck).toHaveBeenCalled());
    expect(result.current.report).toBeNull();
  });

  it("does not re-offer a report the user already dismissed", async () => {
    localStorage.setItem("tenebra.crashAcknowledged", "sig-1");
    const { result } = renderHook(() => useCrashReport(true, true));
    await waitFor(() => expect(mockedCheck).toHaveBeenCalled());
    expect(result.current.report).toBeNull();
  });

  it("dismiss records the signature and clears the report", async () => {
    const { result } = renderHook(() => useCrashReport(true, true));
    await waitFor(() => expect(result.current.report).toEqual(report));

    act(() => result.current.dismiss());

    expect(result.current.report).toBeNull();
    expect(localStorage.getItem("tenebra.crashAcknowledged")).toBe("sig-1");
  });
});
