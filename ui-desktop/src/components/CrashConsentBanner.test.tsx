import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { CrashConsentBanner } from "./CrashConsentBanner";
import { renderWithProviders } from "../test/renderWithProviders";

describe("CrashConsentBanner", () => {
  it("shows the consent copy and both explicit choices", () => {
    renderWithProviders(
      <CrashConsentBanner onEnable={vi.fn()} onDecline={vi.fn()} />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(
      /Nothing is ever sent automatically/,
    );
    expect(screen.getByRole("button", { name: "Enable" })).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "No thanks" }),
    ).toBeInTheDocument();
  });

  it("fires onEnable and onDecline on click", async () => {
    const onEnable = vi.fn();
    const onDecline = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(
      <CrashConsentBanner onEnable={onEnable} onDecline={onDecline} />,
    );

    await user.click(screen.getByRole("button", { name: "Enable" }));
    expect(onEnable).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole("button", { name: "No thanks" }));
    expect(onDecline).toHaveBeenCalledTimes(1);
  });

  it("localizes to Russian", () => {
    renderWithProviders(
      <CrashConsentBanner onEnable={vi.fn()} onDecline={vi.fn()} />,
      { lang: "ru" },
    );
    expect(
      screen.getByRole("button", { name: "Включить" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Не надо" })).toBeInTheDocument();
  });
});
