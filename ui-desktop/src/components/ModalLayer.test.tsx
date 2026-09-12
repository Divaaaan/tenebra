import { useState } from "react";
import { fireEvent, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { UpdateConfirm } from "./UpdateConfirm";
import { ModalLayer } from "./ModalLayer";
import { renderWithProviders } from "../test/renderWithProviders";

it("skips controls hidden by responsive CSS when entering and wrapping focus", () => {
  renderWithProviders(<ModalLayer onClose={() => {}} role="dialog" aria-label="Responsive settings">
    <button style={{ display: "none" }}>Mobile close</button>
    <div style={{ display: "none" }}><button>Hidden parent</button></div>
    <button>First visible</button>
    <button>Last visible</button>
    <button style={{ visibility: "hidden" }}>Hidden last</button>
  </ModalLayer>);
  const first = screen.getByRole("button", { name: "First visible" });
  const last = screen.getByRole("button", { name: "Last visible" });
  expect(first).toHaveFocus();
  fireEvent.keyDown(first, { key: "Tab", shiftKey: true });
  expect(last).toHaveFocus();
  fireEvent.keyDown(last, { key: "Tab" });
  expect(first).toHaveFocus();
});

it("enters the dialog, contains Tab, makes the background inert and restores focus", () => {
  function Example() {
    const [open, setOpen] = useState(false);
    return <><button onClick={() => setOpen(true)}>Open update</button>
      {open && <UpdateConfirm onConfirm={() => {}} onCancel={() => setOpen(false)} />}</>;
  }
  const { container } = renderWithProviders(<Example />);
  const opener = screen.getByRole("button", { name: "Open update" });
  opener.focus();
  fireEvent.click(opener);
  const cancel = screen.getByRole("button", { name: "Cancel" });
  const install = screen.getByRole("button", { name: /install now/i });
  expect(cancel).toHaveFocus();
  expect(container).toHaveAttribute("inert");
  fireEvent.keyDown(cancel, { key: "Tab", shiftKey: true });
  expect(install).toHaveFocus();
  fireEvent.keyDown(install, { key: "Tab" });
  expect(cancel).toHaveFocus();
  fireEvent.keyDown(cancel, { key: "Escape" });
  expect(screen.queryByRole("alertdialog")).toBeNull();
  expect(container).not.toHaveAttribute("inert");
  expect(opener).toHaveFocus();
});

it("keeps the parent modal isolated while a child opens and closes", () => {
  function Example() {
    const [child, setChild] = useState(false);
    return <ModalLayer onClose={() => {}} role="dialog" aria-label="Parent">
      <button onClick={() => setChild(true)}>Open child</button>
      {child && <ModalLayer onClose={() => setChild(false)} role="dialog" aria-label="Child">
        <button onClick={() => setChild(false)}>Close child</button>
      </ModalLayer>}
    </ModalLayer>;
  }
  const { container, unmount } = renderWithProviders(<Example />);
  const opener = screen.getByRole("button", { name: "Open child" });
  opener.focus();
  fireEvent.click(opener);
  expect(screen.getByRole("dialog", { name: "Parent" })).toHaveAttribute("inert");
  fireEvent.click(screen.getByRole("button", { name: "Close child" }));
  expect(container).toHaveAttribute("inert");
  expect(opener).toHaveFocus();
  unmount();
  expect(container).not.toHaveAttribute("inert");
});
