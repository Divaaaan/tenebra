import { describe, expect, it, vi, afterEach } from "vitest";
import { act, fireEvent, screen } from "@testing-library/react";

import { CreditsToast, CREDITS_MS } from "./CreditsToast";
import { renderWithProviders } from "../test/renderWithProviders";

describe("CreditsToast", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders the quiet gratitude", () => {
    renderWithProviders(<CreditsToast onDismiss={vi.fn()} />);
    expect(screen.getByText("made in the dark")).toBeInTheDocument();
    expect(
      screen.getByText("thank you for looking closer"),
    ).toBeInTheDocument();
  });

  it("exposes a localized dismiss control", () => {
    renderWithProviders(<CreditsToast onDismiss={vi.fn()} />, { lang: "ru" });
    // The close affordance carries a translated label, not a hardcoded one.
    expect(
      screen.getByRole("button", { name: "Закрыть" }),
    ).toBeInTheDocument();
  });

  it("self-dismisses after its lifetime", () => {
    vi.useFakeTimers();
    const onDismiss = vi.fn();
    renderWithProviders(<CreditsToast onDismiss={onDismiss} />);

    expect(onDismiss).not.toHaveBeenCalled();
    act(() => {
      vi.advanceTimersByTime(CREDITS_MS);
    });
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("dismisses on Escape", () => {
    const onDismiss = vi.fn();
    renderWithProviders(<CreditsToast onDismiss={onDismiss} />);
    fireEvent.keyDown(window, { key: "Escape" });
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });
});
