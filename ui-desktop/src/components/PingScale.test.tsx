import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";

import { PingScale, pingScaleLevel, pingScaleTone } from "./PingScale";

describe("pingScaleLevel", () => {
  it("maps latency to a 1–5 fill, with exclusive upper bounds", () => {
    expect(pingScaleLevel(20)).toBe(5);
    expect(pingScaleLevel(39)).toBe(5);
    expect(pingScaleLevel(40)).toBe(4);
    expect(pingScaleLevel(69)).toBe(4);
    expect(pingScaleLevel(90)).toBe(3);
    expect(pingScaleLevel(140)).toBe(2);
    expect(pingScaleLevel(300)).toBe(1);
  });
});

describe("pingScaleTone", () => {
  it("steps good → warn → signal as the fill drops", () => {
    expect(pingScaleTone(5)).toBe("good");
    expect(pingScaleTone(4)).toBe("good");
    expect(pingScaleTone(3)).toBe("warn");
    expect(pingScaleTone(2)).toBe("signal");
    expect(pingScaleTone(1)).toBe("signal");
  });
});

describe("PingScale", () => {
  it("always renders five bars", () => {
    const { container } = render(<PingScale rttMs={23} />);
    expect(container.querySelectorAll(".ping-scale-bar")).toHaveLength(5);
  });

  it("lights every bar in the good tone for a fast node", () => {
    const { container } = render(<PingScale rttMs={23} />);
    expect(container.querySelectorAll(".ping-scale-bar.on.good")).toHaveLength(
      5,
    );
  });

  it("lights three bars in the warn tone for a middling node", () => {
    const { container } = render(<PingScale rttMs={90} />);
    expect(container.querySelectorAll(".ping-scale-bar.on")).toHaveLength(3);
    expect(container.querySelectorAll(".ping-scale-bar.on.warn")).toHaveLength(
      3,
    );
  });

  it("lights a single signal-tone bar for a slow node", () => {
    const { container } = render(<PingScale rttMs={300} />);
    expect(container.querySelectorAll(".ping-scale-bar.on.signal")).toHaveLength(
      1,
    );
  });

  it("is decorative by default but labelable for assistive tech", () => {
    const { container, rerender, getByRole } = render(<PingScale rttMs={23} />);
    expect(container.querySelector(".ping-scale")).toHaveAttribute(
      "aria-hidden",
      "true",
    );
    rerender(<PingScale rttMs={23} ariaLabel="signal strong" />);
    expect(getByRole("img", { name: "signal strong" })).toBeInTheDocument();
  });
});
