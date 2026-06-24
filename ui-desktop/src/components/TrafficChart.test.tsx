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
});
