import { describe, expect, it } from "vitest";

import { SCRAMBLE_POOL, scrambleFrame } from "./scramble";

describe("scrambleFrame", () => {
  // Deterministic rng: always picks the first glyph in the pool.
  const first = () => 0;

  it("returns the exact target on and past the final frame", () => {
    expect(scrambleFrame("HELLO", 12, 12, SCRAMBLE_POOL, first)).toBe("HELLO");
    expect(scrambleFrame("HELLO", 99, 12, SCRAMBLE_POOL, first)).toBe("HELLO");
  });

  it("reveals nothing at frame 0 — every character is a pool glyph", () => {
    expect(scrambleFrame("HELLO", 0, 12, "X", first)).toBe("XXXXX");
  });

  it("reveals a left-to-right prefix proportional to the frame", () => {
    // reveal = floor((6/12) * 5) = 2 → first two characters are real.
    expect(scrambleFrame("HELLO", 6, 12, "X", first)).toBe("HEXXX");
    // reveal = floor((3/12) * 8) = 2
    expect(scrambleFrame("NEGOTIAT", 3, 12, "X", first)).toBe("NEXXXXXX");
  });

  it("always preserves the target's length", () => {
    const word = "Connecting…";
    expect(scrambleFrame(word, 4, 12).length).toBe(word.length);
  });

  it("draws scrambled characters only from the given pool", () => {
    expect(scrambleFrame("AB", 0, 12, "#", first)).toBe("##");
  });
});
