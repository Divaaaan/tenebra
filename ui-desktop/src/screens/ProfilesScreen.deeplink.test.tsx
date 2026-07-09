import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";

import { ProfilesScreen } from "./ProfilesScreen";
import { renderWithProviders } from "../test/renderWithProviders";
import { makeProfile, makeTenebra } from "../test/fixtures";

// A tenebra://import deep link lands here as `initialImport`: the screen should
// open the import dialog on the subscription tab with the URL pre-filled, ready
// for the user to name and confirm.
describe("ProfilesScreen deep-link import", () => {
  it("opens the import dialog pre-filled with the subscription URL", () => {
    const onImportConsumed = vi.fn();
    const tenebra = makeTenebra({ profiles: [makeProfile()] });
    renderWithProviders(
      <ProfilesScreen
        tenebra={tenebra}
        selectedProfileId={null}
        onSelectProfile={vi.fn()}
        initialImport="https://example.invalid/deep-sub"
        onImportConsumed={onImportConsumed}
      />,
    );

    const dialog = within(screen.getByRole("dialog"));
    // Subscription tab active, URL field carrying the deep link's URL.
    expect(dialog.getByRole("tab", { name: "Subscription" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(dialog.getByPlaceholderText("https://…")).toHaveValue(
      "https://example.invalid/deep-sub",
    );
    // The caller is told the preset was consumed so it can clear it and not
    // reopen the dialog on the next render.
    expect(onImportConsumed).toHaveBeenCalledTimes(1);
  });

  it("stays closed when there is no preset", () => {
    const tenebra = makeTenebra({ profiles: [makeProfile()] });
    renderWithProviders(
      <ProfilesScreen
        tenebra={tenebra}
        selectedProfileId={null}
        onSelectProfile={vi.fn()}
      />,
    );

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
