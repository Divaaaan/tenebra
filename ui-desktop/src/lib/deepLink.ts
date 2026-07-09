// Dispatch a routed deep-link action to the matching UI flow. The Rust side has
// already parsed and validated the tenebra:// URL (the single source of that
// grammar), so this only fans the tagged action out to the right handler. Kept
// separate from App so the routing is unit-testable without mounting the app.

import type { DeepLinkAction } from "../api";

export interface DeepLinkHandlers {
  /** Open the import flow pre-filled with a subscription URL. */
  onImport: (url: string) => void;
  /** Connect a profile by id. */
  onConnect: (profile: string) => void;
}

/**
 * Route a deep-link action to its handler. An unrecognized action is ignored
 * (the union is exhaustive today; the default keeps a future Rust-side variant
 * from throwing in an older renderer).
 */
export function dispatchDeepLink(
  action: DeepLinkAction,
  handlers: DeepLinkHandlers,
): void {
  switch (action.action) {
    case "import":
      handlers.onImport(action.url);
      break;
    case "connect":
      handlers.onConnect(action.profile);
      break;
    default:
      break;
  }
}
