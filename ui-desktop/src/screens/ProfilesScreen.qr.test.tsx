import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { ProfilesScreen } from "./ProfilesScreen";
import { renderWithProviders } from "../test/renderWithProviders";
import { makeProfile, makeTenebra } from "../test/fixtures";
import { api } from "../api";

// The QR tab decodes an image to a string and then routes it through the right
// import path: a web URL becomes a subscription, anything else a server link.
// We mock the decoder (the decode itself is covered in lib/qr.test.ts) and the
// import calls so the test asserts only the routing the dialog owns.
vi.mock("../lib/qr", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/qr")>();
  return {
    ...actual,
    isQrDecodingSupported: () => true,
    decodeQrFromBlob: vi.fn(),
  };
});

vi.mock("../api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      importSubscription: vi
        .fn()
        .mockResolvedValue({ id: "x", name: "x", source: "subscription", nodes: [] }),
      importLink: vi
        .fn()
        .mockResolvedValue({ id: "x", name: "x", source: "manual", nodes: [] }),
    },
  };
});

import { decodeQrFromBlob } from "../lib/qr";

async function openQrTab() {
  const tenebra = makeTenebra({ profiles: [makeProfile()] });
  renderWithProviders(
    <ProfilesScreen
      tenebra={tenebra}
      selectedProfileId={null}
      onSelectProfile={vi.fn()}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Import" }));
  const dialog = within(screen.getByRole("dialog"));
  await user.click(dialog.getByRole("tab", { name: "QR code" }));
  return { user, dialog };
}

/** The hidden image <input> behind the "Choose image…" button. */
function qrInput(dialog: ReturnType<typeof within>): HTMLInputElement {
  return dialog
    .getByText("Choose image…")
    .closest(".prof-field")!
    .querySelector('input[type="file"]') as HTMLInputElement;
}

const image = new File(["fake-bytes"], "code.png", { type: "image/png" });

describe("ProfilesScreen QR import routing", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("routes a decoded subscription URL to import_subscription", async () => {
    vi.mocked(decodeQrFromBlob).mockResolvedValue("https://example.invalid/sub");
    const { user, dialog } = await openQrTab();

    await user.upload(qrInput(dialog), image);

    expect(api.importSubscription).toHaveBeenCalledTimes(1);
    expect(api.importSubscription).toHaveBeenCalledWith(
      "https://example.invalid/sub",
      "https://example.invalid/sub",
    );
    expect(api.importLink).not.toHaveBeenCalled();
  });

  it("routes a decoded share link to import_link", async () => {
    vi.mocked(decodeQrFromBlob).mockResolvedValue(
      "vless://abc@a.example.com:443#node",
    );
    const { user, dialog } = await openQrTab();

    await user.upload(qrInput(dialog), image);

    expect(api.importLink).toHaveBeenCalledTimes(1);
    expect(api.importLink).toHaveBeenCalledWith(
      "vless://abc@a.example.com:443#node",
      undefined,
    );
    expect(api.importSubscription).not.toHaveBeenCalled();
  });

  it("shows the QR error copy when decoding fails", async () => {
    const { QrError } = await import("../lib/qr");
    vi.mocked(decodeQrFromBlob).mockRejectedValue(new QrError("notFound"));
    const { user, dialog } = await openQrTab();

    await user.upload(qrInput(dialog), image);

    expect(
      await dialog.findByText("No QR code found in that image."),
    ).toBeInTheDocument();
    expect(api.importSubscription).not.toHaveBeenCalled();
    expect(api.importLink).not.toHaveBeenCalled();
  });
});
