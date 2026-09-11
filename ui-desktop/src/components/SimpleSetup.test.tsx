import { fireEvent, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { SimpleSetup } from "./SimpleSetup";
import { renderWithProviders } from "../test/renderWithProviders";

it("explains a subscription failure without exposing its private URL", async () => {
  const subscribe = vi.fn().mockRejectedValue(new Error('Get "https://example.invalid/private-token": dial tcp: refused'));
  renderWithProviders(<SimpleSetup hasProfile={false} onSubscribe={subscribe} />, { lang: "ru" });
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "https://example.invalid/private-token" } });
  fireEvent.click(screen.getByRole("button"));
  const alert = await screen.findByRole("alert");
  expect(alert).toHaveTextContent("Не удалось скачать подписку");
  expect(alert).not.toHaveTextContent("private-token");
});
