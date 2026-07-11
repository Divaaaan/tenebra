import { useEffect, useRef } from "react";

import type { ConnectionState, RoutingMode } from "../api";
import type { Strings } from "../i18n/strings";
import { pushToast } from "./toast";

/** The connected node's display bits, for the "tunnel up" confirmation. */
export interface ConnectedNode {
  name: string;
  protocol: string;
}

/** The App-level state slice whose transitions raise a toast. */
export interface ActionToastState {
  ready: boolean;
  phase: ConnectionState;
  killSwitch: boolean;
  routing: RoutingMode | undefined;
}

const ROUTING_LABEL: Record<RoutingMode, keyof Strings["settings"]> = {
  smart: "routingSmart",
  global: "routingGlobal",
  direct: "routingDirect",
};

/**
 * Raises a toast on the App-level state transitions the user just caused:
 * reaching "connected", arming / disarming the kill switch, and changing the
 * routing mode. Baselines are seeded from the first settled snapshot (once
 * `ready`), so the initial status load is silent — only genuine changes speak.
 *
 * Kept out of App.tsx so the transition logic is unit-testable against a plain
 * mock state, and so App just calls one hook. It never invents toasts for other
 * zones — those are wired in after their state lands.
 */
export function useActionToasts(
  state: ActionToastState,
  connectedNode: ConnectedNode | null,
  t: Strings,
): void {
  const { ready, phase, killSwitch, routing } = state;

  const inited = useRef(false);
  const prevPhase = useRef(phase);
  const prevKill = useRef(killSwitch);
  const prevRouting = useRef(routing);
  // Track the freshest node bits without making them a trigger: only the flip to
  // "connected" reads them, so a ping-driven change must not re-run the effect.
  const nodeRef = useRef(connectedNode);
  nodeRef.current = connectedNode;

  useEffect(() => {
    if (!ready) {
      return;
    }

    // First settled snapshot after the core status loads: seed the baselines,
    // don't announce them.
    if (!inited.current) {
      inited.current = true;
      prevPhase.current = phase;
      prevKill.current = killSwitch;
      prevRouting.current = routing;
      return;
    }

    if (prevPhase.current !== "connected" && phase === "connected") {
      const node = nodeRef.current;
      const parts = [t.toast.tunnelUp, node?.name, node?.protocol].filter(
        (part): part is string => Boolean(part),
      );
      pushToast(parts.join(" · "));
    }
    prevPhase.current = phase;

    if (prevKill.current !== killSwitch) {
      pushToast(killSwitch ? t.toast.killOn : t.toast.killOff);
    }
    prevKill.current = killSwitch;

    if (routing && prevRouting.current !== routing) {
      const label = t.settings[ROUTING_LABEL[routing]];
      pushToast(t.toast.route.replace("{mode}", label.toLowerCase()));
    }
    prevRouting.current = routing;
  }, [ready, phase, killSwitch, routing, t]);
}
