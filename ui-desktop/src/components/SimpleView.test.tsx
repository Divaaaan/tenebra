import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";

import type { ConnectionState } from "../api";
import { SimpleView } from "./SimpleView";
import { renderWithProviders } from "../test/renderWithProviders";
import { makeNode, makeProfile } from "../test/fixtures";

const nodes = [
  makeNode({ id: "n1", name: "AMS-01" }),
  makeNode({ id: "n2", name: "FRA-02" }),
];

function setup(overrides: Partial<Parameters<typeof SimpleView>[0]> = {}) {
  const props = {
    phase: "idle" as ConnectionState,
    busy: false,
    onPrimary: vi.fn(),
    nodeName: "",
    profiles: [makeProfile({ id: "p1", name: "Acme", nodes })],
    selectedProfileId: "p1",
    onSelectProfile: vi.fn(),
    nodes,
    selectedNodeId: "",
    onSelectNode: vi.fn(),
    onSelectAuto: vi.fn(),
    ...overrides,
  };
  const utils = renderWithProviders(<SimpleView {...props} />);
  return { ...utils, props };
}

describe("SimpleView", () => {
  it("shows the idle status and a Connect control", () => {
    setup();
    expect(screen.getByText("Disconnected")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Connect" })).toBeInTheDocument();
    expect(screen.getByText("You're not connected")).toBeInTheDocument();
  });

  it("shows Disconnect and the protected line with the node when connected", () => {
    setup({ phase: "connected", nodeName: "AMS-01" });
    expect(
      screen.getByRole("button", { name: "Disconnect" }),
    ).toBeInTheDocument();
    expect(screen.getByText("You're protected · AMS-01")).toBeInTheDocument();
  });

  it("shows Abort while connecting", () => {
    setup({ phase: "connecting" });
    expect(screen.getByRole("button", { name: "ABORT" })).toBeInTheDocument();
  });

  it("invokes onPrimary when the button is clicked", () => {
    const { props } = setup();
    fireEvent.click(screen.getByRole("button", { name: "Connect" }));
    expect(props.onPrimary).toHaveBeenCalledTimes(1);
  });

  it("disables the button and shows a hint with no subscription", () => {
    setup({ profiles: [], nodes: [] });
    expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled();
    expect(
      screen.getByText("Import a subscription to get started."),
    ).toBeInTheDocument();
  });

  it("disables the button while a primary action is in flight", () => {
    setup({ busy: true });
    expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled();
  });

  it("pins a node when one is picked", () => {
    const { props } = setup();
    fireEvent.change(screen.getByRole("combobox", { name: /server/i }), {
      target: { value: "n2" },
    });
    expect(props.onSelectNode).toHaveBeenCalledWith("n2");
    expect(props.onSelectAuto).not.toHaveBeenCalled();
  });

  it("returns to automatic when the auto option is picked", () => {
    const { props } = setup({ selectedNodeId: "n1" });
    fireEvent.change(screen.getByRole("combobox", { name: /server/i }), {
      target: { value: "" },
    });
    expect(props.onSelectAuto).toHaveBeenCalledTimes(1);
    expect(props.onSelectNode).not.toHaveBeenCalled();
  });

  it("omits the profile picker for a single subscription", () => {
    setup();
    expect(
      screen.queryByRole("combobox", { name: /profile/i }),
    ).not.toBeInTheDocument();
  });

  it("offers a profile picker when more than one subscription exists", () => {
    const { props } = setup({
      profiles: [
        makeProfile({ id: "p1", name: "Acme", nodes }),
        makeProfile({ id: "p2", name: "Globex", nodes }),
      ],
    });
    const picker = screen.getByRole("combobox", { name: /profile/i });
    fireEvent.change(picker, { target: { value: "p2" } });
    expect(props.onSelectProfile).toHaveBeenCalledWith("p2");
  });
});
