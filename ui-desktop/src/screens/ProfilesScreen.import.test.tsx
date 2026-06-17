import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { ProfilesScreen } from "./ProfilesScreen";
import { renderWithProviders } from "../test/renderWithProviders";
import { makeProfile, makeTenebra } from "../test/fixtures";
import { api } from "../api";

// Only the import calls matter here; keep every other real export (types,
// error classes, QR/clipboard helpers) intact so the dialog mounts normally.
vi.mock("../api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      importSubscription: vi.fn().mockResolvedValue(makeProfileStub()),
      importLink: vi.fn().mockResolvedValue(makeProfileStub()),
    },
  };
});

// A plain object stands in for the resolved Profile; the dialog only awaits it.
function makeProfileStub() {
  return { id: "imported", name: "Imported", source: "manual", nodes: [] };
}

/** Open the import dialog and hand back its scoped query root. */
async function openDialog() {
  const tenebra = makeTenebra({ profiles: [makeProfile()] });
  renderWithProviders(
    <ProfilesScreen
      tenebra={tenebra}
      selectedProfileId={null}
      onSelectProfile={vi.fn()}
    />,
  );
  const user = userEvent.setup();
  // The page header's "Import" button opens the modal.
  await user.click(screen.getByRole("button", { name: "Import" }));
  const dialog = within(screen.getByRole("dialog"));
  return { user, dialog, tenebra };
}

describe("ProfilesScreen import dialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("subscription tab", () => {
    it("requires a name before importing", async () => {
      const { user, dialog } = await openDialog();

      // Submit with both fields blank: the name check fires first.
      await user.click(dialog.getByRole("button", { name: "Import" }));
      expect(dialog.getByText("Enter a name.")).toBeInTheDocument();
      expect(api.importSubscription).not.toHaveBeenCalled();
    });

    it("requires a URL once a name is given", async () => {
      const { user, dialog } = await openDialog();

      await user.type(dialog.getByPlaceholderText("My provider"), "Provider");
      await user.click(dialog.getByRole("button", { name: "Import" }));
      expect(dialog.getByText("Enter a subscription URL.")).toBeInTheDocument();
      expect(api.importSubscription).not.toHaveBeenCalled();
    });

    it("imports a subscription with trimmed values", async () => {
      const { user, dialog } = await openDialog();

      await user.type(dialog.getByPlaceholderText("My provider"), "  Provider  ");
      await user.type(
        dialog.getByPlaceholderText("https://…"),
        "  https://example.invalid/sub  ",
      );
      await user.click(dialog.getByRole("button", { name: "Import" }));

      expect(api.importSubscription).toHaveBeenCalledWith(
        "https://example.invalid/sub",
        "Provider",
      );
    });
  });

  describe("link tab", () => {
    async function goToLinkTab() {
      const ctx = await openDialog();
      await ctx.user.click(ctx.dialog.getByRole("tab", { name: "Link" }));
      return ctx;
    }

    it("requires a link", async () => {
      const { user, dialog } = await goToLinkTab();

      await user.click(dialog.getByRole("button", { name: "Import" }));
      expect(dialog.getByText("Paste a server link.")).toBeInTheDocument();
      expect(api.importLink).not.toHaveBeenCalled();
    });

    it("imports a server link with no name", async () => {
      const { user, dialog } = await goToLinkTab();

      const link = "vless://11111111-2222-3333-4444-555555555555@a.example.com:443";
      await user.type(
        dialog.getByPlaceholderText(/vless:\/\//),
        link,
      );
      await user.click(dialog.getByRole("button", { name: "Import" }));

      // Name left blank collapses to undefined, not an empty string.
      expect(api.importLink).toHaveBeenCalledWith(link, undefined);
    });

    it("passes a typed name through to the link import", async () => {
      const { user, dialog } = await goToLinkTab();

      await user.type(dialog.getByPlaceholderText("My provider"), "My node");
      const link = "ss://example@203.0.113.5:8388";
      await user.type(dialog.getByPlaceholderText(/vless:\/\//), link);
      await user.click(dialog.getByRole("button", { name: "Import" }));

      expect(api.importLink).toHaveBeenCalledWith(link, "My node");
    });
  });
});
