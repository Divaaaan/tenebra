import { afterEach, describe, expect, it } from "vitest";
import { renderHook } from "@testing-library/react";

import {
  useActionToasts,
  type ActionToastState,
  type ConnectedNode,
} from "./useActionToasts";
import { subscribeToast } from "./toast";
import { dictionaries } from "../i18n/strings";

// Real English copy so the assertions read like the shipped strings.
const t = dictionaries.en;

interface Props {
  state: ActionToastState;
  node: ConnectedNode | null;
}

let unsubscribe: (() => void) | null = null;

afterEach(() => {
  unsubscribe?.();
  unsubscribe = null;
});

function setup(initial: Props) {
  const messages: string[] = [];
  unsubscribe = subscribeToast((m) => messages.push(m));
  const { rerender } = renderHook(
    ({ state, node }: Props) => useActionToasts(state, node, t),
    { initialProps: initial },
  );
  return { messages, rerender };
}

describe("useActionToasts", () => {
  it("stays silent on the initial snapshot", () => {
    const { messages } = setup({
      state: { ready: true, phase: "connected", killSwitch: true, routing: "global" },
      node: { name: "EX-01", protocol: "hysteria2" },
    });

    expect(messages).toEqual([]);
  });

  it("announces tunnel-up when the phase reaches connected", () => {
    const { messages, rerender } = setup({
      state: { ready: true, phase: "connecting", killSwitch: false, routing: "smart" },
      node: null,
    });

    rerender({
      state: { ready: true, phase: "connected", killSwitch: false, routing: "smart" },
      node: { name: "EX-01", protocol: "hysteria2" },
    });

    expect(messages).toEqual(["tunnel up · EX-01 · hysteria2"]);
  });

  it("announces the kill switch arming and disarming", () => {
    const base: ConnectedNode = { name: "EX-01", protocol: "vless" };
    const { messages, rerender } = setup({
      state: { ready: true, phase: "connected", killSwitch: false, routing: "smart" },
      node: base,
    });

    rerender({
      state: { ready: true, phase: "connected", killSwitch: true, routing: "smart" },
      node: base,
    });
    rerender({
      state: { ready: true, phase: "connected", killSwitch: false, routing: "smart" },
      node: base,
    });

    expect(messages).toEqual(["kill-switch · on", "kill-switch · off"]);
  });

  it("announces a routing change with a lowercased mode", () => {
    const { messages, rerender } = setup({
      state: { ready: true, phase: "idle", killSwitch: false, routing: "smart" },
      node: null,
    });

    rerender({
      state: { ready: true, phase: "idle", killSwitch: false, routing: "global" },
      node: null,
    });

    expect(messages).toEqual(["route · global"]);
  });

  it("stays silent until ready, then treats the first ready snapshot as the baseline", () => {
    const node: ConnectedNode = { name: "EX-01", protocol: "vless" };
    const { messages, rerender } = setup({
      state: { ready: false, phase: "connecting", killSwitch: false, routing: "smart" },
      node: null,
    });

    // Not ready: even a connected transition is ignored and not baselined.
    rerender({
      state: { ready: false, phase: "connected", killSwitch: false, routing: "smart" },
      node,
    });
    expect(messages).toEqual([]);

    // First ready snapshot seeds the baseline (already connected) — still silent.
    rerender({
      state: { ready: true, phase: "connected", killSwitch: false, routing: "smart" },
      node,
    });
    expect(messages).toEqual([]);
  });
});
