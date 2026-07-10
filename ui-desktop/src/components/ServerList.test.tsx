import { createRef } from "react";
import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { ServerList, type ServerRow } from "./ServerList";
import { renderWithProviders } from "../test/renderWithProviders";
import { makeProfile } from "../test/fixtures";

// A small fixture spread across regions, with one dead node. The codes/cities
// are arbitrary but distinct so search assertions are unambiguous.
function makeRows(): ServerRow[] {
  return [
    {
      id: "n-fra",
      name: "DE-FRA-01",
      city: "frankfurt",
      region: "EU",
      protocol: "vless",
      rttMs: 27,
      dead: false,
      insecure: false,
    },
    {
      id: "n-ams",
      name: "NL-AMS-02",
      city: "amsterdam",
      region: "EU",
      protocol: "hysteria2",
      rttMs: 140,
      dead: false,
      insecure: false,
    },
    {
      id: "n-nyc",
      name: "US-NYC-01",
      city: "new york",
      region: "AM",
      protocol: "shadowsocks",
      rttMs: null,
      dead: true,
      insecure: false,
    },
  ];
}

function baseProps(overrides: Partial<Parameters<typeof ServerList>[0]> = {}) {
  return {
    profiles: [makeProfile()],
    selectedProfileId: "profile-1",
    onSelectProfile: vi.fn(),
    rows: makeRows(),
    activeNodeId: null as string | null,
    region: null,
    onRegion: vi.fn(),
    query: "",
    onQuery: vi.fn(),
    onSelectNode: vi.fn(),
    onAddSubscription: vi.fn(),
    pinging: false,
    ...overrides,
  };
}

describe("ServerList", () => {
  it("renders one row per ServerRow with code, city and protocol tag", () => {
    renderWithProviders(<ServerList {...baseProps()} />);

    expect(screen.getByText("DE-FRA-01")).toBeInTheDocument();
    expect(screen.getByText("frankfurt")).toBeInTheDocument();
    expect(screen.getByText("VLESS")).toBeInTheDocument();

    expect(screen.getByText("NL-AMS-02")).toBeInTheDocument();
    expect(screen.getByText("HY2")).toBeInTheDocument();

    expect(screen.getByText("US-NYC-01")).toBeInTheDocument();
    expect(screen.getByText("SS")).toBeInTheDocument();
  });

  it("reflects online and showing counts", () => {
    renderWithProviders(<ServerList {...baseProps()} />);

    // Two of three rows are live → the heading reads "Nodes · 2 online".
    expect(
      screen.getByRole("heading", { name: /Nodes · 2 online/ }),
    ).toBeInTheDocument();
    // All three rows are visible with no filter → "showing 3".
    expect(
      screen.getByText((_, el) => el?.textContent === "showing 3"),
    ).toBeInTheDocument();
  });

  it("filters rows by region chip and back to all", async () => {
    const user = userEvent.setup();
    // The region is controlled by the parent, so re-render with the value the
    // chip handler would set, mirroring how the screen wires it.
    const onRegion = vi.fn();
    const { rerender } = renderWithProviders(
      <ServerList {...baseProps({ region: null, onRegion })} />,
    );

    await user.click(screen.getByRole("button", { name: "europe" }));
    expect(onRegion).toHaveBeenCalledWith("EU");

    rerender(<ServerList {...baseProps({ region: "EU", onRegion })} />);
    // Only the two EU rows survive the filter.
    expect(screen.getByText("DE-FRA-01")).toBeInTheDocument();
    expect(screen.getByText("NL-AMS-02")).toBeInTheDocument();
    expect(screen.queryByText("US-NYC-01")).not.toBeInTheDocument();

    rerender(<ServerList {...baseProps({ region: null, onRegion })} />);
    expect(screen.getByText("US-NYC-01")).toBeInTheDocument();
  });

  it("filters by code or city, case-insensitively", () => {
    // City match.
    const { rerender } = renderWithProviders(
      <ServerList {...baseProps({ query: "AMSTERDAM" })} />,
    );
    expect(screen.getByText("NL-AMS-02")).toBeInTheDocument();
    expect(screen.queryByText("DE-FRA-01")).not.toBeInTheDocument();

    // Code match, lowercase against an upper-case node name.
    rerender(<ServerList {...baseProps({ query: "de-fra" })} />);
    expect(screen.getByText("DE-FRA-01")).toBeInTheDocument();
    expect(screen.queryByText("NL-AMS-02")).not.toBeInTheDocument();
  });

  it("types into the search input via onQuery", async () => {
    const onQuery = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(<ServerList {...baseProps({ onQuery })} />);

    await user.type(
      screen.getByRole("textbox", { name: "search node · de-fra" }),
      "x",
    );
    expect(onQuery).toHaveBeenCalledWith("x");
  });

  it("marks a dead row aria-disabled and does not select it on click", async () => {
    const onSelectNode = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(<ServerList {...baseProps({ onSelectNode })} />);

    const deadRow = screen.getByText("US-NYC-01").closest('[role="button"]')!;
    expect(deadRow).toHaveAttribute("aria-disabled", "true");
    await user.click(deadRow);
    expect(onSelectNode).not.toHaveBeenCalled();

    // A live row does select.
    const liveRow = screen.getByText("DE-FRA-01").closest('[role="button"]')!;
    await user.click(liveRow);
    expect(onSelectNode).toHaveBeenCalledWith("n-fra");
  });

  it("shows the active marker on the connected node", () => {
    renderWithProviders(<ServerList {...baseProps({ activeNodeId: "n-fra" })} />);

    const activeRow = screen.getByText("DE-FRA-01").closest('[role="button"]')!;
    // The active marker dot lives inside the active row.
    expect(activeRow.querySelector(".srv-active-dot")).toBeTruthy();
  });

  it("forwards its ref to the search input", () => {
    const ref = createRef<HTMLInputElement>();
    renderWithProviders(<ServerList {...baseProps()} ref={ref} />);
    expect(ref.current).toBe(
      screen.getByRole("textbox", { name: "search node · de-fra" }),
    );
  });

  describe("subscription tabs", () => {
    it("renders tabs with more than one profile and selects on click", async () => {
      const onSelectProfile = vi.fn();
      const user = userEvent.setup();
      const profiles = [
        makeProfile({ id: "profile-1", name: "Acme VPN" }),
        makeProfile({ id: "profile-2", name: "Other VPN" }),
      ];
      renderWithProviders(
        <ServerList
          {...baseProps({
            profiles,
            selectedProfileId: "profile-1",
            onSelectProfile,
          })}
        />,
      );

      expect(screen.getByRole("tab", { name: "Acme VPN" })).toHaveAttribute(
        "aria-selected",
        "true",
      );
      await user.click(screen.getByRole("tab", { name: "Other VPN" }));
      expect(onSelectProfile).toHaveBeenCalledWith("profile-2");
    });

    it("renders no tabs with a single profile", () => {
      renderWithProviders(<ServerList {...baseProps()} />);
      expect(screen.queryByRole("tab")).not.toBeInTheDocument();
    });
  });

  describe("insecure (skip-cert-verify) warning", () => {
    it("badges a node with TLS verification off and leaves secure nodes unmarked", () => {
      const rows = makeRows();
      rows[0].insecure = true; // DE-FRA-01 skips cert verification
      renderWithProviders(<ServerList {...baseProps({ rows })} />);

      // The insecure node carries the labelled badge...
      const badge = screen.getByLabelText(
        "TLS verification off — on-path interception possible",
      );
      expect(badge).toBeInTheDocument();
      const insecureRow = screen
        .getByText("DE-FRA-01")
        .closest('[role="button"]')!;
      expect(insecureRow).toContainElement(badge);

      // ...and it's the only one: a secure row has no badge.
      const secureRow = screen.getByText("NL-AMS-02").closest('[role="button"]')!;
      expect(
        secureRow.querySelector(".srv-insecure"),
      ).not.toBeInTheDocument();
      expect(screen.getAllByText("no-cert")).toHaveLength(1);
    });

    it("summarises how many nodes skip TLS verification, profile-wide", () => {
      const rows = makeRows();
      rows[0].insecure = true;
      rows[2].insecure = true; // 2 of 3, counted across all rows not just visible
      renderWithProviders(
        <ServerList {...baseProps({ rows, region: "EU" })} />,
      );

      const alert = screen.getByRole("alert");
      expect(alert).toHaveTextContent(
        "2 of 3 nodes skip TLS verification — on-path interception possible",
      );
    });

    it("shows no summary when every node verifies TLS", () => {
      renderWithProviders(<ServerList {...baseProps()} />);
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expect(screen.queryByText("no-cert")).not.toBeInTheDocument();
    });
  });

  it("calls onAddSubscription from the add button", async () => {
    const onAddSubscription = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(<ServerList {...baseProps({ onAddSubscription })} />);

    await user.click(screen.getByRole("button", { name: "+ add" }));
    expect(onAddSubscription).toHaveBeenCalledTimes(1);
  });
});
