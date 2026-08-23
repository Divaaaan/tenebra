import { createRef } from "react";
import { describe, expect, it, vi } from "vitest";
import { act, screen } from "@testing-library/react";
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

  describe("AUTO row", () => {
    it("names the lowest-ping live node with its ping and a dashed tag", () => {
      renderWithProviders(<ServerList {...baseProps()} />);

      // Best live node is DE-FRA-01 at 27 ms (NL-AMS-02 is slower, US-NYC-01 is
      // dead), named lowercased in the subtitle.
      const autoRow = screen
        .getByText("lowest ping · now de-fra-01")
        .closest('[role="button"]')!;
      expect(autoRow).toHaveClass("srv-auto");
      expect(autoRow.querySelector(".srv-auto-rtt")).toHaveTextContent("27 ms");
      expect(autoRow.querySelector(".srv-auto-tag")).toHaveTextContent("AUTO");
    });

    it("is active by default and calls onSelectAuto on click", async () => {
      const onSelectAuto = vi.fn();
      const user = userEvent.setup();
      renderWithProviders(<ServerList {...baseProps({ onSelectAuto })} />);

      const autoRow = screen
        .getByText("lowest ping · now de-fra-01")
        .closest('[role="button"]')!;
      // No node is pinned (activeNodeId null) → AUTO carries the active state.
      expect(autoRow).toHaveClass("srv-auto", "on");
      expect(autoRow).toHaveAttribute("aria-pressed", "true");
      expect(autoRow.querySelector(".srv-auto-dot")).toBeTruthy();

      await user.click(autoRow);
      expect(onSelectAuto).toHaveBeenCalledTimes(1);
    });

    it("stays neutral when no node has a ping, inventing nothing", () => {
      const rows = makeRows().map((r) => ({
        ...r,
        rttMs: null,
        dead: false,
      }));
      renderWithProviders(<ServerList {...baseProps({ rows })} />);

      // No best node → the subtitle drops the name and the ping shows a
      // placeholder rather than a guessed value.
      const autoRow = screen
        .getByText("lowest ping")
        .closest('[role="button"]')!;
      expect(autoRow.querySelector(".srv-auto-rtt")).toHaveTextContent("·");
    });

    it("deactivates when a node is pinned (auto=false)", () => {
      renderWithProviders(
        <ServerList {...baseProps({ auto: false, activeNodeId: "n-fra" })} />,
      );

      const autoRow = screen
        .getByText("lowest ping · now de-fra-01")
        .closest('[role="button"]')!;
      expect(autoRow).not.toHaveClass("on");
      expect(autoRow.querySelector(".srv-auto-dot")).toBeFalsy();

      // The pinned node row carries the active marker instead.
      const nodeRow = screen.getByText("DE-FRA-01").closest('[role="button"]')!;
      expect(nodeRow).toHaveClass("active");
      expect(nodeRow.querySelector(".srv-active-dot")).toBeTruthy();
    });
  });

  describe("ping meter", () => {
    it("lights a PingScale per live node and blanks it for a dead one", () => {
      renderWithProviders(<ServerList {...baseProps()} />);

      // DE-FRA-01 @ 27 ms → all five bars lit, in the "good" tone.
      const liveRow = screen.getByText("DE-FRA-01").closest('[role="button"]')!;
      expect(liveRow.querySelectorAll(".ping-scale-bar.on")).toHaveLength(5);
      expect(liveRow.querySelector(".ping-scale-bar.on.good")).toBeTruthy();

      // NL-AMS-02 @ 140 ms → two bars, in the "signal" (slow) tone.
      const midRow = screen.getByText("NL-AMS-02").closest('[role="button"]')!;
      expect(midRow.querySelectorAll(".ping-scale-bar.on")).toHaveLength(2);
      expect(midRow.querySelector(".ping-scale-bar.on.signal")).toBeTruthy();

      // US-NYC-01 dead → a blank meter still holds the column, nothing lit.
      const deadRow = screen.getByText("US-NYC-01").closest('[role="button"]')!;
      expect(deadRow.querySelectorAll(".ping-scale-bar")).toHaveLength(5);
      expect(deadRow.querySelectorAll(".ping-scale-bar.on")).toHaveLength(0);
    });
  });

  describe("focus-search event", () => {
    it("focuses the search input when tenebra:focus-search fires", () => {
      renderWithProviders(<ServerList {...baseProps()} />);
      const input = screen.getByRole("textbox", {
        name: "search node · de-fra",
      });
      expect(input).not.toHaveFocus();

      act(() => {
        window.dispatchEvent(new CustomEvent("tenebra:focus-search"));
      });
      expect(input).toHaveFocus();
    });
  });

  describe("empty states", () => {
    it("offers an import CTA when there is no subscription", async () => {
      const onAddSubscription = vi.fn();
      const user = userEvent.setup();
      const { container } = renderWithProviders(
        <ServerList
          {...baseProps({ profiles: [], rows: [], onAddSubscription })}
        />,
      );

      expect(container.querySelector(".srv-empty")).toHaveTextContent(
        "no subscription",
      );
      // No nodes → no AUTO row to select.
      expect(screen.queryByText(/lowest ping/)).not.toBeInTheDocument();

      await user.click(
        screen.getByRole("button", { name: "import subscription" }),
      );
      expect(onAddSubscription).toHaveBeenCalledTimes(1);
    });

    it("offers a reset CTA when a filter hides every node", async () => {
      const onRegion = vi.fn();
      const onQuery = vi.fn();
      const user = userEvent.setup();
      renderWithProviders(
        <ServerList
          {...baseProps({ query: "zzz-no-match", onRegion, onQuery })}
        />,
      );

      expect(
        screen.getByText("no nodes match this filter"),
      ).toBeInTheDocument();
      // AUTO is not subject to the filter — it stays put above the message.
      expect(screen.getByText(/lowest ping/)).toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: "reset filter" }));
      expect(onRegion).toHaveBeenCalledWith(null);
      expect(onQuery).toHaveBeenCalledWith("");
    });

    it("names an empty subscription and offers import, not reset", () => {
      renderWithProviders(<ServerList {...baseProps({ rows: [] })} />);

      expect(
        screen.getByText("this subscription has no nodes"),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: "import subscription" }),
      ).toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: "reset filter" }),
      ).not.toBeInTheDocument();
    });
  });

  describe("row cascade", () => {
    it("staggers row entrances by a growing animation delay", () => {
      const { container } = renderWithProviders(
        <ServerList {...baseProps()} />,
      );
      const rows = Array.from(
        container.querySelectorAll<HTMLElement>(".srv-row"),
      );
      expect(rows).toHaveLength(3);
      expect(rows[0].style.animationDelay).toBe("0ms");
      expect(rows[1].style.animationDelay).toBe("24ms");
      expect(rows[2].style.animationDelay).toBe("48ms");
    });

    it("stops growing the delay past the cap, so a long list still settles fast", () => {
      // Twenty nodes is an ordinary subscription. Uncapped, the last row would
      // wait almost half a second to appear and a sixty-node list well over a
      // second — the rows nobody has scrolled to yet paying for the cascade the
      // first screenful already finished. Past the cap every row shares the last
      // delay, so the tail arrives as one block.
      const many: ServerRow[] = Array.from({ length: 20 }, (_, i) => ({
        id: `n-${i}`,
        name: `DE-FRA-${i}`,
        city: "frankfurt",
        region: "EU" as const,
        protocol: "vless" as const,
        rttMs: 20 + i,
        dead: false,
        insecure: false,
      }));
      const { container } = renderWithProviders(
        <ServerList {...baseProps({ rows: many })} />,
      );
      const rows = Array.from(
        container.querySelectorAll<HTMLElement>(".srv-row"),
      );
      expect(rows).toHaveLength(20);
      expect(rows[12].style.animationDelay).toBe("288ms");
      expect(rows[19].style.animationDelay).toBe("288ms");
    });
  });
});
