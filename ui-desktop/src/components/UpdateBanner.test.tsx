import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { UpdateBanner } from "./UpdateBanner";
import { renderWithProviders } from "../test/renderWithProviders";

describe("UpdateBanner", () => {
  it("offers the found version with install and later actions", async () => {
    const onInstall = vi.fn();
    const onDismiss = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(
      <UpdateBanner
        version="9.9.9"
        installing={false}
        progress={null}
        onInstall={onInstall}
        onDismiss={onDismiss}
      />,
    );

    expect(screen.getByRole("status")).toHaveTextContent(
      "Version 9.9.9 is available",
    );

    await user.click(screen.getByRole("button", { name: /Update/ }));
    expect(onInstall).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("button", { name: /Later/ }));
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("shows indeterminate progress and locks the actions while installing", () => {
    renderWithProviders(
      <UpdateBanner
        version="9.9.9"
        installing
        progress={null}
        onInstall={vi.fn()}
        onDismiss={vi.fn()}
      />,
    );

    expect(screen.getByRole("status")).toHaveTextContent("Downloading update…");
    expect(screen.getByRole("button", { name: /Update/ })).toBeDisabled();
    // No dismissing mid-install: the restart is already on its way.
    expect(
      screen.queryByRole("button", { name: /Later/ }),
    ).not.toBeInTheDocument();
  });

  it("appends the percent once the download size is known", () => {
    renderWithProviders(
      <UpdateBanner
        version="9.9.9"
        installing
        progress={42}
        onInstall={vi.fn()}
        onDismiss={vi.fn()}
      />,
    );

    expect(screen.getByRole("status")).toHaveTextContent(
      "Downloading update… 42%",
    );
  });

  it("localizes the banner", () => {
    renderWithProviders(
      <UpdateBanner
        version="9.9.9"
        installing={false}
        progress={null}
        onInstall={vi.fn()}
        onDismiss={vi.fn()}
      />,
      { lang: "ru" },
    );

    expect(screen.getByRole("status")).toHaveTextContent(
      "Доступна версия 9.9.9",
    );
    expect(
      screen.getByRole("button", { name: /Обновить/ }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Позже/ })).toBeInTheDocument();
  });
});
