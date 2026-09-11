import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { SettingsScreen } from "./SettingsScreen";
import { makeProfile, makeTenebra } from "../test/fixtures";
import { renderWithProviders } from "../test/renderWithProviders";
vi.mock("@tauri-apps/plugin-autostart", () => ({ isEnabled: vi.fn(async () => false), enable: vi.fn(), disable: vi.fn() }));
vi.mock("@tauri-apps/api/app", () => ({ getVersion: vi.fn(async () => "0.5.11") }));
vi.mock("../lib/updates", () => ({ inAppUpdatesSupported: vi.fn(async () => false), checkForUpdate: vi.fn(), installUpdate: vi.fn() }));
beforeEach(() => localStorage.clear());
function toggle(label: string) {
  fireEvent.click(screen.getByText(label).closest(".set-row")!.querySelector('[role="switch"]')!);
}

it("serializes independent DNS edits and preserves both intents", async () => {
  let finish!: () => void;
  const save = vi.fn().mockImplementationOnce(() => new Promise<void>((resolve) => { finish = resolve; })).mockResolvedValue(undefined);
  renderWithProviders(<SettingsScreen tenebra={makeTenebra({ setDns: save })} />);
  await act(async () => {});
  toggle("Block ads and trackers");
  toggle("IPv4-only DNS");
  expect(save).toHaveBeenCalledTimes(1);
  await act(async () => finish());
  await waitFor(() => expect(save).toHaveBeenNthCalledWith(2, true, "", "", true));
});

it("does not undo an unrelated routing intent when the previous save fails", async () => {
  let fail!: (reason: Error) => void;
  const save = vi.fn().mockImplementationOnce(() => new Promise<void>((_resolve, reject) => { fail = reject; })).mockResolvedValue(undefined);
  renderWithProviders(<SettingsScreen tenebra={makeTenebra({ setRules: save })} />);
  await act(async () => {});
  toggle("Russian banking sites stay direct");
  toggle("Russian government sites stay direct");
  expect(save).toHaveBeenCalledTimes(1);
  await act(async () => fail(new Error("refused")));
  await waitFor(() => expect(save).toHaveBeenNthCalledWith(2, [], [], false, true));
});

it("preserves both multihop endpoints selected before the first save returns", async () => {
  let finish!: () => void;
  const save = vi.fn().mockImplementationOnce(() => new Promise<void>((resolve) => { finish = resolve; })).mockResolvedValue(undefined);
  renderWithProviders(<SettingsScreen tenebra={makeTenebra({ profiles: [makeProfile()], setMultihop: save })} />);
  await act(async () => {});
  const selects = screen.getAllByRole("combobox");
  const entry = selects.find((s) => s.querySelector('option[value="node-1"]'))!;
  const exit = selects.find((s) => s !== entry && s.querySelector('option[value="node-2"]'))!;
  fireEvent.change(entry, { target: { value: "node-1" } });
  fireEvent.change(exit, { target: { value: "node-2" } });
  expect(save).toHaveBeenCalledTimes(1);
  await act(async () => finish());
  await waitFor(() => expect(save).toHaveBeenNthCalledWith(2, "profile-1", false, "node-1", "node-2"));
});

it("gives every settings switch a meaningful accessible name", async () => {
  renderWithProviders(<SettingsScreen tenebra={makeTenebra()} />);
  await act(async () => {});
  expect(screen.getByRole("switch", { name: "Block ads and trackers" })).toBeInTheDocument();
  expect(screen.queryAllByRole("switch", { name: /^(ON|OFF)$/ })).toEqual([]);
});

it("preserves a split-mode edit while a pending application edit finishes", async () => {
  let finish!: () => void;
  const save = vi.fn().mockImplementationOnce(() => new Promise<void>((resolve) => { finish = resolve; })).mockResolvedValue(undefined);
  renderWithProviders(<SettingsScreen tenebra={makeTenebra({ state: { state: "idle", split: "exclude" }, setSplit: save })} />);
  await act(async () => {});
  const field = screen.getByRole("textbox", { name: "Apps" });
  fireEvent.change(field, { target: { value: "chrome.exe" } });
  fireEvent.keyDown(field, { key: "Enter" });
  fireEvent.click(screen.getByRole("radio", { name: /^only these/i }));
  expect(save).toHaveBeenCalledTimes(1);
  await act(async () => finish());
  await waitFor(() => expect(save).toHaveBeenNthCalledWith(2, "include", ["chrome.exe"]));
});
