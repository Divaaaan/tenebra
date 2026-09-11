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
