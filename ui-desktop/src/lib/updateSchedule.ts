// When the update check should run, as plain functions over a clock reading.
// No React, no timers: the hook owns the heartbeat, this owns the question of
// whether a given beat should do anything.
//
// Why a stored timestamp rather than one long setInterval: nothing keeps a
// long timer honest on a desktop client. A machine that suspends stops every
// timer it holds and the slept hours are simply gone, so a six-hour interval
// armed before an eight-hour sleep fires six hours *after* the wake rather
// than on it. And WebView2 coalesces background timers in a hidden window into
// one-minute buckets — which is exactly the window a client parked in the tray
// sits in, the one case this whole schedule exists for. So the timer is only a
// heartbeat: every beat asks the wall clock how long it has actually been
// since the last check, and a machine that wakes up overdue checks on the next
// beat instead of waiting out an interval that never ran.

/** How long a check's answer stands before another one is due. */
export const UPDATE_CHECK_INTERVAL_MS = 6 * 60 * 60 * 1000;

/**
 * How often the heartbeat asks. Short enough that a machine waking from sleep
 * reaches an overdue check in minutes rather than hours, and cheap enough to
 * be spent on two comparisons: the beat itself never touches the network.
 */
export const UPDATE_PULSE_MS = 15 * 60 * 1000;

/**
 * How many checks must fail in a row before the UI stops keeping quiet about
 * it. At the interval above that is roughly eighteen hours without a single
 * answer — well past a flaky minute or a laptop lid closed over lunch, and far
 * enough for "you are not receiving updates" to be a fact rather than a guess.
 */
export const UPDATE_FAILURE_LIMIT = 3;

/**
 * Whether a check is due: never checked, or the interval has elapsed on the
 * wall clock since the last one.
 *
 * `lastAt` in the future is deliberately treated as due. It means the clock
 * moved backwards — a corrected NTP sync, a machine that came up with a dead
 * RTC, a timezone-confused restore, or simply a junk value in storage — and
 * the alternative is waiting for `now` to climb back to a stamp that may be
 * years ahead, which would wedge the client on its installed version forever.
 * Checking rewrites the stamp and the schedule heals itself.
 */
export function isCheckDue(
  now: number,
  lastAt: number | null,
  interval = UPDATE_CHECK_INTERVAL_MS,
): boolean {
  if (lastAt === null || !Number.isFinite(lastAt)) {
    return true;
  }
  if (lastAt > now) {
    return true;
  }
  return now - lastAt >= interval;
}

/**
 * Whether the run of consecutive failures is long enough to say so. Kept
 * separate from the counter itself so the threshold is one named thing the UI
 * and its tests agree on rather than a bare `>= 3` in a component.
 */
export function isCheckStalled(
  failures: number,
  limit = UPDATE_FAILURE_LIMIT,
): boolean {
  return failures >= limit;
}
