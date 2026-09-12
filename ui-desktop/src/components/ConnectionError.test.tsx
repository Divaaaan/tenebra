import { fireEvent, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { ConnectionError } from "./ConnectionError";
import { renderWithProviders } from "../test/renderWithProviders";

it("explains protocol failure in Russian and preserves the technical detail before reporting", () => {
  const report = vi.fn();
  const error = "all protocols failed: vless handshake rejected";
  renderWithProviders(<ConnectionError error={error} onReport={report} />, { lang: "ru" });
  expect(screen.getByRole("alert")).toHaveTextContent("Обновите подписку");
  expect(screen.getByText(error)).toBeInTheDocument();
  expect(report).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button"));
  expect(report).toHaveBeenCalledTimes(1);
});

it.each(["dial tcp 192.0.2.1:443: i/o timeout", "engine executable not found"])(
  "does not blame the service or selected profile without evidence: %s", (error) => {
    renderWithProviders(<ConnectionError error={error} onReport={() => {}} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Connection failed. Try another node");
    expect(screen.getByText(error)).toBeInTheDocument();
  },
);
