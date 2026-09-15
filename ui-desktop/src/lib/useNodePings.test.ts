import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api, type PingResult } from "../api";
import { useNodePings } from "./useNodePings";

afterEach(() => {
  vi.restoreAllMocks();
});

it("reports the initial batch as checking without inventing a stale sample", () => {
  vi.spyOn(api, "ping").mockImplementation(() => new Promise(() => {}));

  const { result } = renderHook(() => useNodePings("p1"));

  expect(result.current.phase).toBe("checking");
  expect(result.current.results.size).toBe(0);
  expect(result.current.error).toBeNull();
});

it("reports an initial rejection as a failed unknown batch", async () => {
  vi.spyOn(api, "ping").mockRejectedValue(new Error("probe process failed"));

  const { result } = renderHook(() => useNodePings("p1"));

  await waitFor(() => expect(result.current.phase).toBe("failed"));
  expect(result.current.results.size).toBe(0);
  expect(result.current.error).toBe("probe process failed");
});

it("keeps the last successful batch when a refresh fails", async () => {
  const ping = vi.spyOn(api, "ping").mockResolvedValue([{ node: "n1", ok: true, rttMs: 20 }]);
  const { result } = renderHook(() => useNodePings("p1"));
  await waitFor(() => expect(result.current.phase).toBe("ready"));
  ping.mockRejectedValue(new Error("probe process failed"));
  act(() => result.current.refresh());
  expect(result.current.phase).toBe("checking");
  expect(result.current.results.get("n1")).toEqual({ node: "n1", ok: true, rttMs: 20 });
  await waitFor(() => expect(result.current.phase).toBe("failed"));
  expect(result.current.results.get("n1")).toEqual({ node: "n1", ok: true, rttMs: 20 });
  expect(result.current.error).toBe("probe process failed");
});

it("does not replace a new profile's measurements with a late refresh response", async () => {
  let finish!: (p: PingResult[]) => void;
  const ping = vi.spyOn(api, "ping").mockResolvedValue([{ node: "old", ok: true, rttMs: 10 }]);
  const { result, rerender } = renderHook(({ profile }) => useNodePings(profile), { initialProps: { profile: "p1" } });
  await waitFor(() => expect(result.current.phase).toBe("ready"));
  ping.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  act(() => result.current.refresh());
  ping.mockResolvedValue([{ node: "new", ok: true, rttMs: 30 }]);
  rerender({ profile: "p2" });
  expect(result.current.results.size).toBe(0);
  await waitFor(() => expect(result.current.results.has("new")).toBe(true));
  await act(async () => finish([{ node: "old", ok: true, rttMs: 1 }]));
  expect([...result.current.results.keys()]).toEqual(["new"]);
});

it("never exposes the previous profile's batch during the first render of a new profile", async () => {
  const frames: Array<{
    profile: string;
    phase: string;
    nodes: string[];
  }> = [];
  const ping = vi.spyOn(api, "ping")
    .mockResolvedValueOnce([{ node: "shared-node", ok: true, rttMs: 10 }]);
  const { result, rerender } = renderHook(
    ({ profile }) => {
      const current = useNodePings(profile);
      frames.push({
        profile,
        phase: current.phase,
        nodes: [...current.results.keys()],
      });
      return current;
    },
    { initialProps: { profile: "p1" } },
  );
  await waitFor(() => expect(result.current.phase).toBe("ready"));
  ping.mockImplementationOnce(() => new Promise(() => {}));
  const firstNewProfileFrame = frames.length;

  rerender({ profile: "p2" });

  expect(frames[firstNewProfileFrame]).toEqual({
    profile: "p2",
    phase: "checking",
    nodes: [],
  });
});
