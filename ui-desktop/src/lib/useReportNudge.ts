import { useCallback, useEffect, useRef, useState } from "react";

import type { ConnectionState, ServiceCheck } from "../api";

/** localStorage key holding when the nudge last spoke, as epoch milliseconds. */
export const NUDGE_KEY = "tenebra.reportNudgeAt";

/**
 * Failing checks in a row before the app says anything. One failure is a flake
 * often enough — a cold cache, a slow first byte — that asking about it is
 * noise, and noise is what a prompt like this cannot afford.
 */
const FAILURES_BEFORE_NUDGE = 2;

/** Quiet period after it has spoken, whatever the user did about it. */
const QUIET_MS = 24 * 60 * 60 * 1000;

/** The one service worth interrupting over: it is what people connect for. */
const WATCHED = "video";

export interface ReportNudgeState {
  /** True while the prompt should be on screen. */
  visible: boolean;
  /** Put it away for this session. */
  dismiss: () => void;
}

function lastShown(): number {
  try {
    return Number(localStorage.getItem(NUDGE_KEY)) || 0;
  } catch {
    // No storage to consult (private mode, a locked-down profile). Treat it as
    // never having spoken: a prompt the user can dismiss beats silence.
    return 0;
  }
}

function rememberShown(now: number): void {
  try {
    localStorage.setItem(NUDGE_KEY, String(now));
  } catch {
    // Non-fatal: at worst it may ask again after a restart.
  }
}

/**
 * Offer to report when the post-connect check says video isn't getting through.
 *
 * This is the one place the app speaks first, so the bar is set high on purpose.
 * It waits for two failing checks in a row, so a single flake stays quiet; it
 * needs a live tunnel, because a verdict about a connection that ended is a
 * verdict about nothing; and it speaks at most once a day.
 *
 * The daily clock starts when the prompt is *shown*, not when it is dismissed.
 * Starting it on dismissal sounds more considerate and is worse: an ignored
 * prompt would return on every reconnect, and a nudge that appears whenever the
 * thing it warns about is still true is a nudge users learn to click away
 * without reading — which costs exactly the day it happens to be important.
 *
 * Nothing here sends anything. It offers a button; assembling and filing the
 * report remains the user's own two clicks.
 */
export function useReportNudge(
  phase: ConnectionState,
  checks: ServiceCheck[],
  runs: number,
): ReportNudgeState {
  const [visible, setVisible] = useState(false);
  // Consecutive failing verdicts, and the last run we have already judged —
  // `checks` is a fresh array on every render of the owner, so the run counter
  // is what says whether there is anything new to judge.
  const streak = useRef(0);
  const judged = useRef(runs);

  useEffect(() => {
    if (runs === judged.current) {
      return;
    }
    judged.current = runs;
    if (phase !== "connected") {
      return;
    }
    const watched = checks.find((c) => c.service === WATCHED);
    if (!watched) {
      // The run reported nothing about video. That is not a pass and not a
      // failure; leave the streak where it was.
      return;
    }
    if (watched.ok) {
      streak.current = 0;
      return;
    }
    streak.current += 1;
    if (streak.current < FAILURES_BEFORE_NUDGE) {
      return;
    }

    const now = Date.now();
    if (now - lastShown() < QUIET_MS) {
      return;
    }
    rememberShown(now);
    streak.current = 0;
    setVisible(true);
  }, [runs, phase, checks]);

  // The prompt is about a live connection. When that ends it stops being true,
  // the same reason the checks themselves are cleared rather than left on
  // screen; if the problem survives the next connect, the next check says so.
  useEffect(() => {
    if (phase !== "connected") {
      setVisible(false);
    }
  }, [phase]);

  const dismiss = useCallback(() => setVisible(false), []);

  return { visible, dismiss };
}
