import { describe, expect, it } from "vitest";
import { renderHook } from "@testing-library/react";

import { useDaemonSkew } from "./useDaemonSkew";
import type { State } from "../api/types";

const APP = "0.4.4";

function idle(extra: Partial<State> = {}): State {
  return { state: "idle", ...extra };
}

describe("useDaemonSkew", () => {
  it("stays silent before any stable snapshot arrives", () => {
    const { result } = renderHook(() => useDaemonSkew(idle({ state: "connecting" }), APP));
    expect(result.current.stale).toBe(false);
  });

  it("reports current when the daemon matches the app", () => {
    const { result } = renderHook(() =>
      useDaemonSkew(idle({ daemon_version: APP }), APP),
    );
    expect(result.current.stale).toBe(false);
  });

  it("flags a daemon that reports an older version", () => {
    const { result } = renderHook(() =>
      useDaemonSkew(idle({ daemon_version: "0.4.3" }), APP),
    );
    expect(result.current.stale).toBe(true);
    expect(result.current.daemonVersion).toBe("0.4.3");
  });

  it("flags a pre-field daemon from a stable snapshot without the version", () => {
    // idle with no daemon_version = a real snapshot from a daemon predating the
    // field (≤0.4.3), not a synthetic one — those are connecting/error only.
    const { result } = renderHook(() => useDaemonSkew(idle(), APP));
    expect(result.current.stale).toBe(true);
    expect(result.current.daemonVersion).toBeNull();
  });

  it("does not read the synthetic reconnecting/lost states as an old daemon", () => {
    const { result, rerender } = renderHook(
      ({ state }: { state: State }) => useDaemonSkew(state, APP),
      { initialProps: { state: idle({ daemon_version: APP }) } },
    );
    expect(result.current.stale).toBe(false);

    // The backend lost the daemon: synthetic states carry no version. The
    // last trusted verdict must hold — no false "stale" banner mid-reconnect.
    rerender({ state: { state: "connecting", error: "Reconnecting…" } });
    expect(result.current.stale).toBe(false);
    rerender({ state: { state: "error", error: "Lost the connection…" } });
    expect(result.current.stale).toBe(false);
  });

  it("clears the flag once an updated daemon reports back", () => {
    const { result, rerender } = renderHook(
      ({ state }: { state: State }) => useDaemonSkew(state, APP),
      { initialProps: { state: idle() } },
    );
    expect(result.current.stale).toBe(true);

    // The user reinstalled the daemon; the reattached session reports the
    // matching build — the banner must drop without an app restart.
    rerender({ state: { state: "connected", daemon_version: APP } });
    expect(result.current.stale).toBe(false);
  });
});
