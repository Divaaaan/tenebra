import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { LogsScreen } from "./LogsScreen";
import { renderWithProviders } from "../test/renderWithProviders";
import { makeTenebra } from "../test/fixtures";
import { api, type LeakCheck } from "../api";
import { pushToast } from "../lib/toast";

// The check result is the only thing the screen fetches; mock it so each test
// pins the verdict. The toast bus is mocked so the clear confirmation is
// assertable without a mounted host.
vi.mock("../api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api")>();
  return {
    ...actual,
    api: { ...actual.api, leakCheck: vi.fn() },
  };
});

vi.mock("../lib/toast", () => ({ pushToast: vi.fn() }));

const CHECK_BUTTON = "IP / leak check";

// A connected, matched result: both findings are a genuine pass.
const CLEAN_LEAK: LeakCheck = {
  connected: true,
  ip_verdict: "ok",
  ip_message: "ok",
  public_ip: "95.217.44.101",
  exit_server: "95.217.44.101",
  dns: { status: "ok", message: "consistent", resolvers: ["tls://1.1.1.1"] },
};

// An idle / undeterminable result: neither finding is a pass, and the UI must
// never colour it as one.
const NEUTRAL_LEAK: LeakCheck = {
  connected: false,
  ip_verdict: "neutral",
  ip_message: "Not connected.",
  public_ip: "203.0.113.7",
  dns: { status: "inconclusive", message: "Not enough signal for a verdict." },
};

describe("LogsScreen leak check", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("shows the progress rail while a check is in flight, then drops it", async () => {
    let resolve!: (v: LeakCheck) => void;
    vi.mocked(api.leakCheck).mockReturnValue(
      new Promise<LeakCheck>((r) => {
        resolve = r;
      }),
    );
    const { container } = renderWithProviders(
      <LogsScreen tenebra={makeTenebra()} />,
    );
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: CHECK_BUTTON }));
    // While the check is out the rail is present (and the button reads "Checking…").
    expect(container.querySelector(".log-rail")).not.toBeNull();
    expect(
      screen.getByRole("button", { name: "Checking…" }),
    ).toBeInTheDocument();

    resolve(CLEAN_LEAK);
    await waitFor(() =>
      expect(container.querySelector(".log-rail")).toBeNull(),
    );
  });

  it("keeps an inconclusive result neutral — never painted as a pass", async () => {
    vi.mocked(api.leakCheck).mockResolvedValue(NEUTRAL_LEAK);
    const { container } = renderWithProviders(
      <LogsScreen tenebra={makeTenebra()} />,
    );
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: CHECK_BUTTON }));
    await waitFor(() =>
      expect(container.querySelectorAll(".log-spec-row")).toHaveLength(2),
    );

    // Both the IP (neutral) and DNS (inconclusive) rows carry the neutral tone,
    // and crucially neither is "good": the core is explicit that an
    // undeterminable check is not a guarantee, and the UI honours it.
    container.querySelectorAll(".log-spec-row").forEach((row) => {
      expect(row.getAttribute("data-tone")).toBe("neutral");
      expect(row.getAttribute("data-tone")).not.toBe("good");
    });
    expect(container.querySelector(".log-verdict--good")).toBeNull();
  });

  it("paints a confirmed clean check with the good tone", async () => {
    vi.mocked(api.leakCheck).mockResolvedValue(CLEAN_LEAK);
    const { container } = renderWithProviders(
      <LogsScreen tenebra={makeTenebra()} />,
    );
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: CHECK_BUTTON }));
    await waitFor(() =>
      expect(container.querySelectorAll(".log-spec-row")).toHaveLength(2),
    );

    container.querySelectorAll(".log-spec-row").forEach((row) => {
      expect(row.getAttribute("data-tone")).toBe("good");
    });
  });

  it("clears the console and confirms with a toast", async () => {
    const tenebra = makeTenebra({
      logs: [{ id: 1, at: new Date(), level: "info", msg: "core started" }],
    });
    renderWithProviders(<LogsScreen tenebra={tenebra} />);
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: "Clear" }));

    expect(tenebra.clearLogs).toHaveBeenCalledTimes(1);
    expect(pushToast).toHaveBeenCalledWith("log cleared");
  });
});
