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
  onImportFiles?: (files: File[]) => Promise<void>;
  onRemove?: (id: string) => void;
  onClose?: () => void;
}

function renderPanel(over: PanelOverrides = {}) {
  // Typed as the mocks they are, so a test can assert on .mock without casting.
  const onImportFiles = vi.fn(over.onImportFiles ?? (async () => {}));
  const onRemove = vi.fn(over.onRemove ?? noop);
  const onClose = vi.fn(over.onClose ?? noop);
  const sources = over.sources ?? [];

  const utils = renderWithProviders(
    <BlocklistPanel
      sources={sources}
      onImportFiles={onImportFiles}
      onRemove={onRemove}
      onClose={onClose}
    />,
  );
  return { ...utils, props: { onImportFiles, onRemove, onClose } };
}

describe("BlocklistPanel", () => {
  it("imports a dropped archive", async () => {
    const { props, container } = renderPanel();
    const zone = container.querySelector(".bl-drop")!;
    const file = makeFile("blocklist.zip");

    fireEvent.drop(zone, { dataTransfer: { files: [file] } });

    await waitFor(() => expect(props.onImportFiles).toHaveBeenCalledTimes(1));
    expect((props.onImportFiles.mock.calls[0][0] as File[])[0].name).toBe("blocklist.zip");
  });

  it("hands every dropped file to the importer without filtering by name", async () => {
    // No extension gate: a release archive names its lists anything at all
    // (`hosts`, `list.aa`), and refusing at the door is how a folder full of
    // good lists ends up importing nothing. What is a list is decided by
    // whether it parses, which is the reader's job, not the drop zone's.
    const { props, container } = renderPanel();
    const zone = container.querySelector(".bl-drop")!;

    fireEvent.drop(zone, {
      dataTransfer: { files: [makeFile("hosts"), makeFile("list.aa")] },
    });

    await waitFor(() => expect(props.onImportFiles).toHaveBeenCalledTimes(1));
    const passed = props.onImportFiles.mock.calls[0][0] as File[];
    expect(passed.map((f) => f.name)).toEqual(["hosts", "list.aa"]);
  });

  it("passes a whole unpacked folder in one go", async () => {
    const { props, container } = renderPanel();
    const zone = container.querySelector(".bl-drop")!;

    fireEvent.drop(zone, {
      dataTransfer: {
        files: [makeFile("hosts"), makeFile("README.md"), makeFile("LICENSE")],
      },
    });

    await waitFor(() => expect(props.onImportFiles).toHaveBeenCalledTimes(1));
    expect((props.onImportFiles.mock.calls[0][0] as File[])).toHaveLength(3);
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
    const onImportFiles = vi.fn().mockRejectedValue(new Error("archive is corrupt"));
    const { container } = renderPanel({ onImportFiles });
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
