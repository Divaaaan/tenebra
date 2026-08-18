import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { api, type NodeCheck } from "../api";
import { useNodeCheck } from "./useNodeCheck";

function check(best: string, nodes: string[]): NodeCheck {
  return {
    best,
    results: nodes.map((n) => ({
      node: n,
      targets: [{ target: "https://a.example/204", stage: "ok", rttMs: 40 }],
    })),
  };
}

describe("useNodeCheck", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("reports the node to connect to", async () => {
    vi.spyOn(api, "checkNodes").mockResolvedValue(check("n2", ["n1", "n2"]));
    const { result } = renderHook(() => useNodeCheck());

    let picked: string | null = null;
    await act(async () => {
      picked = await result.current.run("p1");
    });

    expect(picked).toBe("n2");
    expect(result.current.best).toBe("n2");
    expect(result.current.results.get("n1")).toBeDefined();
    expect(result.current.checked).toBe(true);
  });

  // The core answers with an empty best when every node is broken. Turning that
  // into "no node" rather than a falsy id is what stops the screen from quietly
  // connecting to whatever happened to be first.
  it("treats an empty best as no working node", async () => {
    vi.spyOn(api, "checkNodes").mockResolvedValue(check("", ["n1"]));
    const { result } = renderHook(() => useNodeCheck());

    let picked: string | null = "unset";
    await act(async () => {
      picked = await result.current.run("p1");
    });

    expect(picked).toBeNull();
    expect(result.current.best).toBeNull();
    expect(result.current.checked).toBe(true);
  });

  // A check starts a second sing-box holding loopback ports. Two runs at once
  // would have the later one fail to bind, so pressing connect twice must join
  // the run already going.
  it("collapses concurrent runs into one", async () => {
    const spy = vi
      .spyOn(api, "checkNodes")
      .mockResolvedValue(check("n1", ["n1"]));
    const { result } = renderHook(() => useNodeCheck());

    await act(async () => {
      await Promise.all([result.current.run("p1"), result.current.run("p1")]);
    });

    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("surfaces a failed run instead of pretending it measured", async () => {
    vi.spyOn(api, "checkNodes").mockRejectedValue(new Error("core is down"));
    const { result } = renderHook(() => useNodeCheck());

    await act(async () => {
      await result.current.run("p1");
    });

    await waitFor(() => expect(result.current.error).toBe("core is down"));
    expect(result.current.best).toBeNull();
    expect(result.current.checking).toBe(false);
  });
});
