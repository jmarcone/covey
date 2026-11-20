export type StickySettings = {
  sticky_sessions: boolean;
  sticky_window_minutes: number;
  sticky_context_cap: number;
};

// The bounds the server checks (agents.MaxStickyWindowMinutes,
// agents.MaxStickyContextCap and the CHECKs of migration 0154): the field
// keeps a value inside them rather than sending one the server refuses.
export const STICKY_WINDOW_MAX = 1440;
export const STICKY_CAP_MAX = 1_000_000;

/** A whole number within [min, max]; fallback for an empty or unreadable entry. */
export function clampInt(raw: string, min: number, max: number, fallback: number): number {
  const n = Math.trunc(Number(raw.trim()));
  if (raw.trim() === "" || !Number.isFinite(n)) return fallback;
  return Math.min(max, Math.max(min, n));
}
