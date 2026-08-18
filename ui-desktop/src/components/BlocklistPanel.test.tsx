import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";

import { renderWithProviders } from "../test/renderWithProviders";
import { BlocklistPanel, type BlocklistSource } from "./BlocklistPanel";

function noop() {
  /* intentionally empty */
}

function makeFile(name: string): File {
  return new File(["rule.example.com\n"], name, { type: "application/zip" });
}

interface PanelOverrides {
  sources?: BlocklistSource[];
  onImportFile?: (file: File) => Promise<void>;
  onRemove?: (id: string) => void;
  onClose?: () => void;
}

function renderPanel(over: PanelOverrides = {}) {
  // Typed as the mocks they are, so a test can assert on .mock without casting.
  const onImportFile = vi.fn(over.onImportFile ?? (async () => {}));
  const onRemove = vi.fn(over.onRemove ?? noop);
  const onClose = vi.fn(over.onClose ?? noop);
  const sources = over.sources ?? [];

  const utils = renderWithProviders(
    <BlocklistPanel
      sources={sources}
      onImportFile={onImportFile}
      onRemove={onRemove}
      onClose={onClose}
    />,
  );
  return { ...utils, props: { onImportFile, onRemove, onClose } };
}

describe("BlocklistPanel", () => {
  it("imports a dropped archive", async () => {
    const { props, container } = renderPanel();
    const zone = container.querySelector(".bl-drop")!;
    const file = makeFile("blocklist.zip");

    fireEvent.drop(zone, { dataTransfer: { files: [file] } });

    await waitFor(() => expect(props.onImportFile).toHaveBeenCalledTimes(1));
    expect(props.onImportFile.mock.calls[0][0].name).toBe("blocklist.zip");
  });

  it("refuses a file it cannot read and says so in place", async () => {
    const { props, container } = renderPanel();
    const zone = container.querySelector(".bl-drop")!;

    fireEvent.drop(zone, { dataTransfer: { files: [makeFile("holiday.jpg")] } });

    // The message belongs next to the zone the user just used, not in a toast
    // somewhere else, and the bad file must never reach the importer.
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(props.onImportFile).not.toHaveBeenCalled();
  });

  it("highlights while a file is over the zone and stops when it leaves", () => {
    const { container } = renderPanel();
    const zone = container.querySelector(".bl-drop")!;

    fireEvent.dragEnter(zone);
    expect(zone.className).toContain("is-dragging");

    fireEvent.dragLeave(zone);
    expect(zone.className).not.toContain("is-dragging");
  });

  it("keeps the highlight while the pointer crosses child elements", () => {
    // Nested drag events fire per child, so naive enter/leave handling makes the
    // highlight flicker exactly when the user is aiming.
    const { container } = renderPanel();
    const zone = container.querySelector(".bl-drop")!;

    fireEvent.dragEnter(zone);
    fireEvent.dragEnter(zone); // entered a child
    fireEvent.dragLeave(zone); // left that child, still inside the zone

    expect(zone.className).toContain("is-dragging");
  });

  it("surfaces an import failure instead of failing silently", async () => {
    const onImportFile = vi.fn().mockRejectedValue(new Error("archive is corrupt"));
    const { container } = renderPanel({ onImportFile });
    const zone = container.querySelector(".bl-drop")!;

    fireEvent.drop(zone, { dataTransfer: { files: [makeFile("bad.zip")] } });

    expect(await screen.findByText("archive is corrupt")).toBeTruthy();
  });

  it("lists imported sources with their rule counts", () => {
    renderPanel({
      sources: [
        { id: "a", label: "ru-block.zip", rules: 12400 },
        { id: "b", label: "ads.txt", rules: null },
      ],
    });

    expect(screen.getByText("ru-block.zip")).toBeTruthy();
    expect(screen.getByText(/12400/)).toBeTruthy();
    // A list still being parsed shows a placeholder rather than "0 rules",
    // which would read as an empty file.
    expect(screen.getByText("reading…")).toBeTruthy();
  });

  it("removes a source", () => {
    const onRemove = vi.fn();
    renderPanel({ sources: [{ id: "a", label: "ru-block.zip", rules: 10 }], onRemove });

    fireEvent.click(screen.getByLabelText(/Remove ru-block.zip/));
    expect(onRemove).toHaveBeenCalledWith("a");
  });

  it("closes on the scrim but not when the panel itself is clicked", () => {
    const onClose = vi.fn();
    const { container } = renderPanel({ onClose });

    fireEvent.click(container.querySelector(".bl-panel")!);
    expect(onClose).not.toHaveBeenCalled();

    fireEvent.click(container.querySelector(".bl-scrim")!);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("opens the file picker from the keyboard", () => {
    const { container } = renderPanel();
    const zone = container.querySelector(".bl-drop")!;
    const input = container.querySelector(".bl-file") as HTMLInputElement;
    const click = vi.spyOn(input, "click").mockImplementation(noop);

    fireEvent.keyDown(zone, { key: "Enter" });
    expect(click).toHaveBeenCalled();
  });
});
