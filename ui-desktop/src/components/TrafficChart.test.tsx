import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";

import { TrafficChart } from "./TrafficChart";

describe("TrafficChart", () => {
  it("renders an svg", () => {
    const { container } = render(
      <TrafficChart down={[1, 2, 3]} up={[0, 1, 2]} active />,
    );
    expect(container.querySelector("svg")).toBeInTheDocument();
  });

  it("renders without throwing when there are fewer than 2 samples", () => {
    const { container } = render(
      <TrafficChart down={[5]} up={[2]} active={false} />,
    );
    // The short-sample branch still emits the empty placeholder svg.
    expect(container.querySelector("svg")).toBeInTheDocument();
    expect(container.querySelectorAll("polyline")).toHaveLength(0);
  });

  it("draws the down and up polylines with samples while active", () => {
    const { container } = render(
      <TrafficChart down={[1, 2, 3, 4]} up={[0, 1, 0, 2]} active />,
    );
    // One polyline for upload, one for download.
    expect(container.querySelectorAll("polyline")).toHaveLength(2);
  });

  it("adds the gradient area fill and the pulsing head dot only while active", () => {
    const { container, rerender } = render(
      <TrafficChart down={[1, 2, 3, 4]} up={[0, 1, 0, 2]} active />,
    );
    // A per-instance gradient backs the area fill, drawn under the download line.
    expect(container.querySelector("linearGradient")).toBeInTheDocument();
    expect(container.querySelector("polygon")).toBeInTheDocument();
    // The head is a static dot plus a pulse ring.
    expect(container.querySelectorAll("circle")).toHaveLength(2);

    rerender(<TrafficChart down={[1, 2, 3, 4]} up={[0, 1, 0, 2]} active={false} />);
    // Idle drops the fill and the head entirely.
    expect(container.querySelector("polygon")).not.toBeInTheDocument();
    expect(container.querySelectorAll("circle")).toHaveLength(0);
  });

  it("gives the gradient a collision-free id usable in a url() reference", () => {
    const { container } = render(
      <TrafficChart down={[1, 2, 3, 4]} up={[0, 1, 0, 2]} active />,
    );
    const gradient = container.querySelector("linearGradient");
    const polygon = container.querySelector("polygon");
    const id = gradient?.getAttribute("id") ?? "";
    // No colons (React's useId output) that would break a url(#…) fragment.
    expect(id).not.toContain(":");
    expect(polygon?.getAttribute("fill")).toBe(`url(#${id})`);
  });
});
