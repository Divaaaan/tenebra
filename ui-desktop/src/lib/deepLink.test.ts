import { describe, expect, it, vi } from "vitest";

import { dispatchDeepLink } from "./deepLink";
import type { DeepLinkAction } from "../api";

describe("dispatchDeepLink", () => {
  it("routes an import action to onImport with the URL", () => {
    const onImport = vi.fn();
    const onConnect = vi.fn();
    const action: DeepLinkAction = {
      action: "import",
      url: "https://example.invalid/sub",
    };

    dispatchDeepLink(action, { onImport, onConnect });

    expect(onImport).toHaveBeenCalledWith("https://example.invalid/sub");
    expect(onConnect).not.toHaveBeenCalled();
  });

  it("routes a connect action to onConnect with the profile id", () => {
    const onImport = vi.fn();
    const onConnect = vi.fn();
    const action: DeepLinkAction = { action: "connect", profile: "demo-sub" };

    dispatchDeepLink(action, { onImport, onConnect });

    expect(onConnect).toHaveBeenCalledWith("demo-sub");
    expect(onImport).not.toHaveBeenCalled();
  });

  it("ignores an unrecognized action without throwing", () => {
    const onImport = vi.fn();
    const onConnect = vi.fn();
    // A shape the current union doesn't include — e.g. a future Rust-side variant
    // reaching an older renderer — must be a no-op, not a crash.
    dispatchDeepLink({ action: "reboot" } as unknown as DeepLinkAction, {
      onImport,
      onConnect,
    });

    expect(onImport).not.toHaveBeenCalled();
    expect(onConnect).not.toHaveBeenCalled();
  });
});
