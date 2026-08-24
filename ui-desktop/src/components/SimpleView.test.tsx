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
    bypassInstalled: false,
    bypassOn: false,
    bypassStrategy: "",
    coreUnreachable: false,
    onSubscribe: vi.fn(async () => {}),
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

  it("never asks for a bypass archive", () => {
    // The core downloads and installs a bundle on the first connect. Asking the
    // user to find a release page and drag the right asset in was work the
    // program already does — and the drop zone kept being offered over a bundle
    // that was installed and running, because it read a session flag.
    setup({ profiles: [], nodes: [] });
    expect(
      screen.getByLabelText("Paste your subscription link"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/archive/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/zapret/i)).not.toBeInTheDocument();
  });

  it("drops the setup step once it is satisfied", () => {
    // A finished step left on screen is clutter, and this view exists to avoid
    // exactly that: once set up, it is a status word and one control.
    setup();
    expect(
      screen.queryByLabelText("Paste your subscription link"),
    ).not.toBeInTheDocument();
  });

  // Part of what this screen exists to say. The bypass state comes from the
  // core's snapshot, so a bundle installed on a previous run — by the core, with
  // no import in this session — reads as installed and running.
  it("names the running bypass from the core's snapshot", () => {
    setup({
      bypassInstalled: true,
      bypassOn: true,
      bypassStrategy: "general (FAKE TLS AUTO)",
    });
    expect(screen.getByText(/bypass on/)).toBeInTheDocument();
    expect(screen.getByText(/general \(FAKE TLS AUTO\)/)).toBeInTheDocument();
  });

  it("says a bundle is installed but idle rather than showing nothing", () => {
    setup({
      bypassInstalled: true,
      bypassOn: false,
      bypassStrategy: "general (FAKE TLS AUTO)",
    });
    expect(screen.getByText("bypass off")).toBeInTheDocument();
    // Not the strategy: nothing is running it.
    expect(
      screen.queryByText(/general \(FAKE TLS AUTO\)/),
    ).not.toBeInTheDocument();
  });

  it("stays silent about the bypass before a bundle exists", () => {
    const { container } = setup({ bypassInstalled: false, bypassOn: false });
    expect(container.querySelector(".simple-bypass")).toBeNull();
  });

  // A core that never answered leaves this screen drawn over nothing. The calm
  // status word on its own reads as "everything is fine, you are just not
  // connected", which is the opposite of what happened.
  it("says the core is unreachable instead of showing a calm status", () => {
    setup({ coreUnreachable: true });
    expect(screen.getByRole("alert")).toHaveTextContent(
      /no connection to the background service/i,
    );
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
