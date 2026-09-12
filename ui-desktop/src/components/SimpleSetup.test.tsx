import { fireEvent, screen, waitFor } from "@testing-library/react";
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

it("allows another import after the last subscription is removed", async () => {
  const subscribe = vi.fn().mockResolvedValue(undefined);
  const view = renderWithProviders(<SimpleSetup hasProfile={false} onSubscribe={subscribe} />);
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "https://example.invalid/first" } });
  fireEvent.click(screen.getByRole("button", { name: "Import" }));
  await waitFor(() => expect(screen.getByRole("textbox")).toHaveValue(""));
  view.rerender(<SimpleSetup hasProfile={true} onSubscribe={subscribe} />);
  expect(screen.queryByRole("textbox")).toBeNull();
  view.rerender(<SimpleSetup hasProfile={false} onSubscribe={subscribe} />);
  expect(screen.getByRole("textbox")).toBeEnabled();
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "https://example.invalid/second" } });
  fireEvent.click(screen.getByRole("button", { name: "Import" }));
  await waitFor(() => expect(subscribe).toHaveBeenCalledTimes(2));
});
