import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { UpdateStaleBanner } from "./UpdateStaleBanner";
import { renderWithProviders } from "../test/renderWithProviders";

describe("UpdateStaleBanner", () => {
  it("says the client has stopped hearing about releases", () => {
    renderWithProviders(
      <UpdateStaleBanner onCheckNow={vi.fn()} onDismiss={vi.fn()} />,
    );

    // A status, not an alert: the usual cause is the network, and the update
    // flow is not an incident surface.
    expect(screen.getByRole("status")).toHaveTextContent(
      "Updates haven't been checked in a while — this copy may be out of date",
    );
  });

  it("checks again on demand", async () => {
    const onCheckNow = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(
      <UpdateStaleBanner onCheckNow={onCheckNow} onDismiss={vi.fn()} />,
    );

    await user.click(screen.getByRole("button", { name: /Check now/ }));
    expect(onCheckNow).toHaveBeenCalledTimes(1);
  });

  it("hides on the other action", async () => {
    const onDismiss = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(
      <UpdateStaleBanner onCheckNow={vi.fn()} onDismiss={onDismiss} />,
    );

    await user.click(screen.getByRole("button", { name: /Hide/ }));
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("localizes the strip", () => {
    renderWithProviders(
      <UpdateStaleBanner onCheckNow={vi.fn()} onDismiss={vi.fn()} />,
      { lang: "ru" },
    );

    expect(screen.getByRole("status")).toHaveTextContent(
      "Обновления давно не проверялись — версия может быть устаревшей",
    );
    expect(
      screen.getByRole("button", { name: /Проверить сейчас/ }),
    ).toBeInTheDocument();
  });
});
