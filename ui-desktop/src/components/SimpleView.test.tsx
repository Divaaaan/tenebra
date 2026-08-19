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
    hasBypass: false,
    bypassActive: "",
    onSubscribe: vi.fn(async () => {}),
    onBypassFiles: vi.fn(async () => {}),
    onBypassPaths: vi.fn(async () => {}),
    serviceChecks: [],
    serviceChecking: false,
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

  it("asks for the subscription link right where the button is", () => {
    // A first-run user has a link in hand, so the screen offers the field
    // instead of telling them to go and import one elsewhere — the setup step
    // IS the empty state.
    setup({ profiles: [], nodes: [] });
    expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled();
    expect(
      screen.getByLabelText("Paste your subscription link"),
    ).toBeInTheDocument();
  });

  it("offers the bypass drop zone until a bundle is installed", () => {
    setup({ profiles: [], nodes: [], hasBypass: false });
    expect(screen.getByText("Drop the bypass archive here")).toBeInTheDocument();
  });

  it("drops both setup steps once they are satisfied", () => {
    // A finished step left on screen is clutter, and this view exists to avoid
    // exactly that: once set up, it is a status word and one control.
    setup({ hasBypass: true });
    expect(
      screen.queryByLabelText("Paste your subscription link"),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByText("Drop the bypass archive here"),
    ).not.toBeInTheDocument();
  });

  it("names the running bypass rather than just flagging it on", () => {
    // Which strategy is running is the useful fact: they behave differently,
    // and the app picked this one by measurement.
    setup({ hasBypass: true, bypassActive: "general (FAKE TLS AUTO)" });
    expect(screen.getByText(/general \(FAKE TLS AUTO\)/)).toBeInTheDocument();
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

  it("exits simple mode from the advanced-view escape hatch", () => {
    const dispatch = vi.spyOn(window, "dispatchEvent");
    setup();

    fireEvent.click(screen.getByRole("button", { name: "Advanced view" }));

    // The shared flag is flipped off, in the encoding App/Settings agree on.
    expect(localStorage.getItem("tenebra.simpleMode")).toBe("false");
    // And the app shell is nudged both ways App listens: a `storage` event and
    // the same-document custom event.
    const types = dispatch.mock.calls.map(([e]) => (e as Event).type);
    expect(types).toContain("storage");
    expect(types).toContain("tenebra:simple-mode");

    dispatch.mockRestore();
    localStorage.clear();
  });
});
