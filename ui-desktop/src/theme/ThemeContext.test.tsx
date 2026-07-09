import { beforeEach, describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { ThemeProvider, useTheme } from "./ThemeContext";

// A minimal consumer: shows the current theme and exposes the setter.
function Probe() {
  const { theme, setTheme } = useTheme();
  return (
    <div>
      <span data-testid="theme">{theme}</span>
      <button type="button" onClick={() => setTheme("light")}>
        set light
      </button>
      <button type="button" onClick={() => setTheme("dark")}>
        set dark
      </button>
    </div>
  );
}

describe("ThemeProvider", () => {
  beforeEach(() => {
    localStorage.clear();
    delete document.documentElement.dataset.theme;
  });

  it("starts dark and stamps the document on mount", () => {
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>,
    );
    expect(screen.getByTestId("theme")).toHaveTextContent("dark");
    expect(document.documentElement.dataset.theme).toBe("dark");
  });

  it("boots into a previously saved light theme", () => {
    localStorage.setItem("tenebra.theme", "light");
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>,
    );
    expect(screen.getByTestId("theme")).toHaveTextContent("light");
    expect(document.documentElement.dataset.theme).toBe("light");
  });

  it("setTheme flips the document and persists the choice", async () => {
    const user = userEvent.setup();
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>,
    );

    await user.click(screen.getByRole("button", { name: "set light" }));
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(localStorage.getItem("tenebra.theme")).toBe("light");
  });

  it("setTheme round-trips dark -> light -> dark, persisting each step", async () => {
    const user = userEvent.setup();
    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>,
    );

    await user.click(screen.getByRole("button", { name: "set light" }));
    expect(localStorage.getItem("tenebra.theme")).toBe("light");
    expect(document.documentElement.dataset.theme).toBe("light");

    await user.click(screen.getByRole("button", { name: "set dark" }));
    expect(localStorage.getItem("tenebra.theme")).toBe("dark");
    expect(document.documentElement.dataset.theme).toBe("dark");
  });
});
