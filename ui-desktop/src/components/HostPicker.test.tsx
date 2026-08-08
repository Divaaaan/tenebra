import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { HostPicker, type HostPickerProps, type HostPickerStrings } from "./HostPicker";
import type { LiveHost } from "../api";

// The picker takes every word from props, so the fixture doubles as the check
// that nothing is baked in: no assertion below reads a string the component
// could have supplied itself. Hosts follow RFC 2606 / TEST-NET-3 so the suite
// names no real service.
const STRINGS: HostPickerStrings = {
  title: "Live connections",
  hint: "A snapshot of what the tunnel is carrying right now. It never refreshes on its own, and with the tunnel off there is nothing to list.",
  count: "{n} in this snapshot",
  refresh: "Take a snapshot",
  refreshing: "Reading…",
  loading: "Reading the connection table…",
  empty: "Nothing in this snapshot.",
  emptyHint:
    "Connect, open the site you are after, then take another snapshot.",
  ipBadge: "IP",
  ipHint: "An address, not a domain name — this connection never carried one.",
  unknownProcess: "unknown process",
  routeHint: "Where this connection goes right now.",
  pick: "Add {host} to the rules",
};

// Deliberately out of order, so the sort has something to prove. Totals:
// video 12.4 MB, the address 12.0 MB, mail 0.5 MB.
function makeHosts(): LiveHost[] {
  return [
    {
      host: "mail.example.net",
      process: "thunderbird.exe",
      up: 200_000,
      down: 300_000,
      outbound: "direct",
    },
    {
      host: "video.example.com",
      process: "chrome.exe",
      up: 1_000_000,
      down: 11_400_000,
      outbound: "proxy",
    },
    {
      host: "203.0.113.9",
      up: 4_000_000,
      down: 8_000_000,
      outbound: "proxy",
      is_ip: true,
    },
  ];
}

function baseProps(overrides: Partial<HostPickerProps> = {}): HostPickerProps {
  return {
    hosts: makeHosts(),
    loading: false,
    onPick: vi.fn(),
    onRefresh: vi.fn(),
    strings: STRINGS,
    ...overrides,
  };
}

/** The host of each row, top to bottom — the order the reader actually sees. */
function renderedHosts(container: HTMLElement): string[] {
  return [...container.querySelectorAll(".set-host-id")].map(
    (el) => el.textContent ?? "",
  );
}

describe("HostPicker", () => {
  it("renders one row per connection with host, volume, process and route", () => {
    render(<HostPicker {...baseProps()} />);

    const rows = screen.getAllByRole("listitem");
    expect(rows).toHaveLength(3);

    const video = rows.find((r) => within(r).queryByText("video.example.com"));
    expect(video).toBeDefined();
    expect(video).toHaveTextContent("11.8 MB");
    expect(video).toHaveTextContent("chrome.exe");
    expect(video).toHaveTextContent("proxy");

    const mail = rows.find((r) => within(r).queryByText("mail.example.net"));
    expect(mail).toHaveTextContent("488 KB");
    expect(mail).toHaveTextContent("thunderbird.exe");
    expect(mail).toHaveTextContent("direct");
  });

  it("puts the heaviest connection first", () => {
    const { container } = render(<HostPicker {...baseProps()} />);

    expect(renderedHosts(container)).toEqual([
      "video.example.com",
      "203.0.113.9",
      "mail.example.net",
    ]);
  });

  it("leaves the caller's snapshot untouched while sorting", () => {
    const hosts = makeHosts();
    render(<HostPicker {...baseProps({ hosts })} />);

    expect(hosts.map((h) => h.host)).toEqual([
      "mail.example.net",
      "video.example.com",
      "203.0.113.9",
    ]);
  });

  it("shows volumes in human units rather than raw byte counts", () => {
    const hosts: LiveHost[] = [
      { host: "a.example.com", up: 0, down: 0, outbound: "direct" },
      { host: "b.example.com", up: 0, down: 999, outbound: "direct" },
      { host: "c.example.com", up: 512, down: 1024, outbound: "direct" },
      { host: "d.example.com", up: 0, down: 1_500_000_000, outbound: "proxy" },
    ];
    const { container } = render(<HostPicker {...baseProps({ hosts })} />);

    expect(screen.getByText("1.4 GB")).toBeInTheDocument();
    expect(screen.getByText("1.5 KB")).toBeInTheDocument();
    expect(screen.getByText("999 B")).toBeInTheDocument();
    expect(screen.getByText("0 B")).toBeInTheDocument();
    // Both directions count toward the figure the rows are ranked by.
    expect(renderedHosts(container)).toEqual([
      "d.example.com",
      "c.example.com",
      "b.example.com",
      "a.example.com",
    ]);
  });

  it("hands the exact host to the caller when a row is clicked", async () => {
    const user = userEvent.setup();
    const onPick = vi.fn();
    render(<HostPicker {...baseProps({ onPick })} />);

    await user.click(screen.getByRole("button", { name: /mail\.example\.net/ }));

    expect(onPick).toHaveBeenCalledTimes(1);
    expect(onPick).toHaveBeenCalledWith("mail.example.net");
  });

  it("says plainly when a row is an address and not a domain", async () => {
    const user = userEvent.setup();
    const onPick = vi.fn();
    render(<HostPicker {...baseProps({ onPick })} />);

    const address = screen
      .getAllByRole("listitem")
      .find((r) => within(r).queryByText("203.0.113.9"));
    expect(address).toBeDefined();
    // Short badge on screen, the whole sentence for assistive tech — an address
    // must never read as a domain either way.
    expect(within(address!).getByText(STRINGS.ipBadge)).toBeInTheDocument();
    expect(within(address!).getByText(STRINGS.ipHint)).toBeInTheDocument();

    // Named rows carry no such badge.
    const video = screen
      .getAllByRole("listitem")
      .find((r) => within(r).queryByText("video.example.com"));
    expect(within(video!).queryByText(STRINGS.ipBadge)).not.toBeInTheDocument();

    await user.click(within(address!).getByRole("button"));
    expect(onPick).toHaveBeenCalledWith("203.0.113.9");
  });

  it("names the missing owner rather than leaving the process blank", () => {
    const hosts: LiveHost[] = [
      { host: "a.example.com", up: 10, down: 10, outbound: "direct" },
    ];
    render(<HostPicker {...baseProps({ hosts })} />);

    expect(screen.getByText(STRINGS.unknownProcess)).toBeInTheDocument();
  });

  it("always states that the list is a manual snapshot", () => {
    render(<HostPicker {...baseProps()} />);

    expect(screen.getByText(STRINGS.hint)).toBeInTheDocument();
  });

  it("counts what the snapshot holds", () => {
    render(<HostPicker {...baseProps()} />);

    expect(screen.getByText("3 in this snapshot")).toBeInTheDocument();
  });

  describe("reading a snapshot", () => {
    it("reads one only when the button is pressed", async () => {
      const user = userEvent.setup();
      const onRefresh = vi.fn();
      const { rerender } = render(
        <HostPicker {...baseProps({ onRefresh })} />,
      );

      // Nothing on mount, and nothing on a re-render either.
      expect(onRefresh).not.toHaveBeenCalled();
      rerender(<HostPicker {...baseProps({ onRefresh })} />);
      expect(onRefresh).not.toHaveBeenCalled();

      await user.click(screen.getByRole("button", { name: STRINGS.refresh }));
      expect(onRefresh).toHaveBeenCalledTimes(1);
    });

    describe("with time moved on", () => {
      afterEach(() => {
        vi.useRealTimers();
      });

      it("never re-reads on a timer", () => {
        vi.useFakeTimers();
        const onRefresh = vi.fn();
        render(<HostPicker {...baseProps({ onRefresh })} />);

        // A list that re-sorts itself under the pointer cannot be clicked; the
        // only thing that may move it is the button.
        vi.advanceTimersByTime(120_000);
        expect(onRefresh).not.toHaveBeenCalled();
      });
    });

    it("relabels and blocks the button while a read is in flight", async () => {
      const user = userEvent.setup();
      const onRefresh = vi.fn();
      render(<HostPicker {...baseProps({ loading: true, onRefresh })} />);

      const button = screen.getByRole("button", { name: STRINGS.refreshing });
      expect(button).toBeDisabled();
      expect(
        screen.queryByRole("button", { name: STRINGS.refresh }),
      ).not.toBeInTheDocument();

      // The button is disabled, but prove the guard holds even if a click lands.
      await user.click(button);
      expect(onRefresh).not.toHaveBeenCalled();
    });
  });

  describe("states", () => {
    it("says a first read is running, with no rows to show yet", () => {
      render(<HostPicker {...baseProps({ hosts: [], loading: true })} />);

      expect(screen.getByRole("status")).toHaveTextContent(STRINGS.loading);
      expect(screen.queryByRole("list")).not.toBeInTheDocument();
      expect(screen.queryByText(STRINGS.empty)).not.toBeInTheDocument();
    });

    it("keeps the previous rows in place while the next read runs", () => {
      render(<HostPicker {...baseProps({ loading: true })} />);

      // Yanking rows out from under the pointer is exactly what the manual
      // refresh exists to avoid — the old frame stays, marked as stale.
      expect(screen.getAllByRole("listitem")).toHaveLength(3);
      expect(screen.getByRole("list")).toHaveAttribute("aria-busy", "true");
      expect(screen.queryByText(STRINGS.loading)).not.toBeInTheDocument();
    });

    it("explains an empty snapshot instead of showing a blank pane", () => {
      render(<HostPicker {...baseProps({ hosts: [] })} />);

      expect(screen.getByText(STRINGS.empty)).toBeInTheDocument();
      expect(screen.getByText(STRINGS.emptyHint)).toBeInTheDocument();
      expect(screen.queryByRole("list")).not.toBeInTheDocument();
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });

    it("surfaces a failure as an alert and drops the empty-state copy", () => {
      render(
        <HostPicker
          {...baseProps({ hosts: [], error: "The engine is not running." })}
        />,
      );

      expect(screen.getByRole("alert")).toHaveTextContent(
        "The engine is not running.",
      );
      // "Nothing is connected" and "we could not look" are different claims;
      // only the true one is on screen.
      expect(screen.queryByText(STRINGS.empty)).not.toBeInTheDocument();
      expect(screen.queryByRole("list")).not.toBeInTheDocument();
    });

    it("keeps a stale snapshot visible beside a failed re-read", () => {
      render(<HostPicker {...baseProps({ error: "Connection refused." })} />);

      expect(screen.getByRole("alert")).toHaveTextContent("Connection refused.");
      expect(screen.getAllByRole("listitem")).toHaveLength(3);
    });
  });

  describe("keyboard", () => {
    it("reaches the button and every row, and picks with Enter or Space", async () => {
      const user = userEvent.setup();
      const onPick = vi.fn();
      render(<HostPicker {...baseProps({ onPick })} />);

      await user.tab();
      expect(screen.getByRole("button", { name: STRINGS.refresh })).toHaveFocus();

      await user.tab();
      await user.keyboard("{Enter}");
      expect(onPick).toHaveBeenCalledWith("video.example.com");

      await user.tab();
      await user.keyboard(" ");
      expect(onPick).toHaveBeenLastCalledWith("203.0.113.9");
      expect(onPick).toHaveBeenCalledTimes(2);
    });

    it("names the list for assistive tech and describes each row's action", () => {
      render(<HostPicker {...baseProps()} />);

      expect(
        screen.getByRole("list", { name: STRINGS.title }),
      ).toBeInTheDocument();
      expect(
        screen.getByTitle("Add mail.example.net to the rules"),
      ).toBeInTheDocument();
    });
  });

  it("takes all of its copy from props", () => {
    const ru: HostPickerStrings = {
      ...STRINGS,
      title: "Живые соединения",
      refresh: "Снять срез",
      empty: "В срезе пусто.",
      emptyHint: "При выключенном туннеле список пуст.",
    };
    render(<HostPicker {...baseProps({ hosts: [], strings: ru })} />);

    expect(screen.getByText("Живые соединения")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Снять срез" })).toBeInTheDocument();
    expect(screen.getByText("В срезе пусто.")).toBeInTheDocument();
    expect(screen.queryByText(STRINGS.empty)).not.toBeInTheDocument();
  });
});
