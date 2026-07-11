import { vi } from "vitest";

import type { Node, Profile, State } from "../api";
import type { Tenebra } from "../state/useTenebra";

/** A node with documentation-range defaults, overridable per field. */
export function makeNode(overrides: Partial<Node> = {}): Node {
  return {
    id: "node-1",
    name: "Amsterdam",
    protocol: "vless",
    server: "198.51.100.10",
    port: 443,
    ...overrides,
  };
}

/** A subscription profile with two nodes, overridable per field. */
export function makeProfile(overrides: Partial<Profile> = {}): Profile {
  return {
    id: "profile-1",
    name: "Demo subscription",
    source: "subscription",
    url: "https://example.invalid/sub",
    nodes: [
      makeNode({ id: "node-1", name: "Amsterdam" }),
      makeNode({ id: "node-2", name: "Frankfurt", protocol: "hysteria2" }),
    ],
    updatedAt: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

/**
 * A fully stubbed {@link Tenebra} for component tests. Every action is a
 * vi.fn resolving to undefined; pass overrides to set state/profiles or to
 * assert on specific actions.
 */
export function makeTenebra(overrides: Partial<Tenebra> = {}): Tenebra {
  return {
    ready: true,
    state: { state: "idle" } as State,
    traffic: { up: 0, down: 0, upRate: 0, downRate: 0 },
    profiles: [],
    logs: [],
    attempts: null,
    connect: vi.fn().mockResolvedValue(undefined),
    disconnect: vi.fn().mockResolvedValue(undefined),
    setRouting: vi.fn().mockResolvedValue(undefined),
    setSplit: vi.fn().mockResolvedValue(undefined),
    setKillSwitch: vi.fn().mockResolvedValue(undefined),
    setTun: vi.fn().mockResolvedValue(undefined),
    setAutoconnect: vi.fn().mockResolvedValue(undefined),
    setDns: vi.fn().mockResolvedValue(undefined),
    refreshProfiles: vi.fn().mockResolvedValue(undefined),
    clearLogs: vi.fn(),
    ...overrides,
  };
}
