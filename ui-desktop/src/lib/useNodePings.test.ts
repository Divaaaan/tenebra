import { act, renderHook, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { api, type PingResult } from "../api";
import { useNodePings } from "./useNodePings";

it("marks old measurements stale when a new ping run fails", async () => {
  const ping = vi.spyOn(api, "ping").mockResolvedValue([{ node: "n1", ok: true, rttMs: 20 }]);
  const { result } = renderHook(() => useNodePings("p1"));
  await waitFor(() => expect(result.current.results.size).toBe(1));
  ping.mockRejectedValue(new Error("probe process failed"));
  act(() => result.current.refresh());
  await waitFor(() => expect(result.current.pinging).toBe(false));
  expect(result.current.stale).toBe(true);
  expect(result.current.error).toBe("probe process failed");
});

it("does not replace a new profile's measurements with a late refresh response", async () => {
  let finish!: (p: PingResult[]) => void;
  const ping = vi.spyOn(api, "ping").mockResolvedValue([{ node: "old", ok: true, rttMs: 10 }]);
  const { result, rerender } = renderHook(({ profile }) => useNodePings(profile), { initialProps: { profile: "p1" } });
  await waitFor(() => expect(result.current.results.has("old")).toBe(true));
  ping.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  act(() => result.current.refresh());
  ping.mockResolvedValue([{ node: "new", ok: true, rttMs: 30 }]);
  rerender({ profile: "p2" });
  await waitFor(() => expect(result.current.results.has("new")).toBe(true));
  await act(async () => finish([{ node: "old", ok: true, rttMs: 1 }]));
  expect([...result.current.results.keys()]).toEqual(["new"]);
});
