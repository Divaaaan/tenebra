// Shared mapping from a round-trip time to a coarse quality bucket, used to
// colour ping readouts consistently across screens.

export type PingQuality = "good" | "fair" | "poor";

export function pingQuality(rttMs: number): PingQuality {
  if (rttMs < 120) {
    return "good";
  }
  if (rttMs < 250) {
    return "fair";
  }
  return "poor";
}
