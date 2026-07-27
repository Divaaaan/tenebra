import { afterEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { DaemonSkewBanner, MACOS_DAEMON_UPDATE_COMMAND } from "./DaemonSkewBanner";
import { renderWithProviders } from "../test/renderWithProviders";

// The platform hint keys off navigator.userAgent (no os plugin in the app);
// jsdom's default UA names neither a Mac nor a real X11 session, so the Windows
// branch is the default here and the other two are exercised by overriding the
// getter per test with the string that platform's webview actually sends.
function mockUserAgent(ua: string) {
  const original = Object.getOwnPropertyDescriptor(
    Navigator.prototype,
    "userAgent",
  );
  Object.defineProperty(window.navigator, "userAgent", {
    configurable: true,
    get: () => ua,
  });
  return () => {
    delete (window.navigator as { userAgent?: unknown }).userAgent;
    if (original) {
      Object.defineProperty(Navigator.prototype, "userAgent", original);
    }
  };
}

const MAC_UA =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15";
const LINUX_UA =
  "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/605.1.15 (KHTML, like Gecko)";

function mockMacUserAgent() {
  return mockUserAgent(MAC_UA);
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("DaemonSkewBanner", () => {
  it("names both versions when the daemon reported one", () => {
    renderWithProviders(
      <DaemonSkewBanner daemonVersion="0.3.7" appVersion="0.4.4" onDismiss={vi.fn()} />,
    );

    expect(screen.getByRole("status")).toHaveTextContent(
      "The background service is v0.3.7 but the app is v0.4.4",
    );
  });

  it("says the daemon predates the app when it is too old to report", () => {
    renderWithProviders(
      <DaemonSkewBanner daemonVersion={null} appVersion="0.4.4" onDismiss={vi.fn()} />,
    );

    expect(screen.getByRole("status")).toHaveTextContent(
      "The background service predates v0.4.4",
    );
  });

  it("offers the reinstall hint off macOS", () => {
    renderWithProviders(
      <DaemonSkewBanner daemonVersion="0.3.7" appVersion="0.4.4" onDismiss={vi.fn()} />,
    );

    expect(screen.getByText("Reinstall the app to update it")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Copy update command/ }),
    ).not.toBeInTheDocument();
  });

  it("points at the package and the service on Linux", () => {
    // The daemon there is a system service the package owns: the app can
    // neither update nor restart it, so the way out is the package manager —
    // and never the macOS copy-command button, which names a repo script that
    // does not apply.
    const restore = mockUserAgent(LINUX_UA);

    renderWithProviders(
      <DaemonSkewBanner daemonVersion="0.3.7" appVersion="0.4.4" onDismiss={vi.fn()} />,
    );

    expect(
      screen.getByText("Update the Tenebra package, then restart the tenebra service"),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Reinstall the app to update it"),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Copy update command/ }),
    ).not.toBeInTheDocument();

    restore();
  });

  it("copies the daemon update command on macOS", async () => {
    const restore = mockMacUserAgent();
    // Install the clipboard spy after userEvent.setup(): setup replaces
    // navigator.clipboard with its own stub, which would swallow the call.
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });

    renderWithProviders(
      <DaemonSkewBanner daemonVersion="0.3.7" appVersion="0.4.4" onDismiss={vi.fn()} />,
    );

    await user.click(screen.getByRole("button", { name: /Copy update command/ }));
    expect(writeText).toHaveBeenCalledWith(MACOS_DAEMON_UPDATE_COMMAND);
    expect(await screen.findByRole("button", { name: /Copied/ })).toBeInTheDocument();

    restore();
  });

  it("dismisses", async () => {
    const onDismiss = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(
      <DaemonSkewBanner daemonVersion={null} appVersion="0.4.4" onDismiss={onDismiss} />,
    );

    await user.click(screen.getByRole("button", { name: /Dismiss/ }));
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("localizes the banner", () => {
    renderWithProviders(
      <DaemonSkewBanner daemonVersion="0.3.7" appVersion="0.4.4" onDismiss={vi.fn()} />,
      { lang: "ru" },
    );

    expect(screen.getByRole("status")).toHaveTextContent(
      "Фоновая служба v0.3.7, а приложение v0.4.4",
    );
  });
});
