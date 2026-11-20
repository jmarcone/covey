import type {
  BilledPoint,
  BilledReport,
  BookingBasis,
  CellAgent,
  CostCell,
  CostBasisKind,
  CostCells,
  CredentialBasis,
  CredentialPlan,
  ExternalMeterStep,
  ExternalMeterTotal,
  ExternalUsage,
  MeterCoverage,
  MeterStep,
  UsageLimit,
  UsageReading,
  WindowCharge,
} from "../api";
import type { Source } from "./costSources";
import { beforeRange, cellLead, isOperatorSource, listedSource, perUseSource, outsideKpis, sourceLead, sourceRealParts, type OutsideKpis } from "./outsideFigures";

/* The figures of the Costs page, as pure functions — no React, no fetch.
 *
 * Per credential (the secret value that paid) four figures, never mixed
 * across credentials:
 *
 *   reported    — what Claude Code priced its own lines at (engine_usd);
 *   recomputed  — the same lines' tokens priced per type with covey's list.
 *                 It checks the price table, not the tokens: both come from
 *                 the same lines;
 *   real        — the money. On a subscription seat the weekly meter's
 *                 movement × the plan's price per point, an ESTIMATE with a
 *                 range (the meter shows whole percents, it is account-wide,
 *                 and a point only costs its price if the week's allowance is
 *                 used up). On an API key what Anthropic billed over whole
 *                 billed buckets, beside the reported and recomputed figures
 *                 of exactly that window, complete only up to its
 *                 data_refreshed_at — and no dollar figure at all where no
 *                 bill could be read (no admin key, no mapping, a failed or
 *                 not yet landed sync);
 *   multiple    — recomputed list $ per real $, only from 3 points of
 *                 movement, always with its range. It is NOT a bound in
 *                 either direction: other use of the account moves the meter
 *                 too (the true multiple is higher), and a week whose
 *                 allowance is not used up makes each used point dearer than
 *                 its price here (the true multiple is lower).
 *
 * Nothing here reads the booked figure (cost_entries) except the
 * booking-check, which says when bookings are an older daemon's running
 * totals. */

/** A credential's id on the page: the secret value, or "" for runs no credential is known for. */
export function credId(key?: string | null, slot?: number | null): string {
  return key == null || slot == null ? "" : `${key}#${slot}`;
}

/** Cost types that are recomputed from tokens (or, for an unknown model, the
 *  engine's own figure for its lines). "unattributed" is a booking without
 *  lines: booked, not measured, so it is not part of the figure. */
export const RECOMPUTED_TYPES = ["cache_read", "cache_write", "input", "output", "web_search", "unpriced"];

export function recomputedOf(c: Pick<CostCell, "measured">, types: readonly string[] = RECOMPUTED_TYPES): number {
  let s = 0;
  for (const k of types) if (k !== "unattributed") s += c.measured[k] ?? 0;
  return s;
}

// --- The lead figure and its price-table check (#120, the human's rule) ----------
//
// One list-price figure leads the page: "At token prices (as reported by
// Claude Code)". Per call it is the price Claude Code reported for it where
// it reported one — every line of covey's agents (engine_usd), and an
// outside call that carried reported_usd — and covey's list price for the
// call's tokens where it reported none (an upload of token counts only),
// marked wherever such a call is counted. A call with neither is in no
// figure and said apart, never $0. covey's own price table (Recomputed) is
// a check of it, not a second figure: one line says where the two differ
// noticeably (PRICE_CHECK), nothing where they agree, and the full
// Recomputed view is one toggle away.

/** When covey's price table differs noticeably from the lead figure: by
 *  more than 1 % of it AND by more than $0.50. Both must hold, so cent-level
 *  noise in a small hour and rounding in a large total stay silent. One
 *  constant for every place the check is said: the tiles, the headline, a
 *  period's details, a table's rows. */
export const PRICE_CHECK = { pct: 1, usd: 0.5 } as const;

/** covey's price table against the lead figure: its amount and the
 *  difference in % of the lead ((table − lead) ÷ lead × 100), or null where
 *  they agree within PRICE_CHECK or there is no lead to compare with. */
export function priceTableDifference(lead: number, table: number): { usd: number; pct: number } | null {
  if (!(lead > 0) || !Number.isFinite(table)) return null;
  const d = table - lead;
  if (!(Math.abs(d) > (PRICE_CHECK.pct / 100) * lead) || !(Math.abs(d) > PRICE_CHECK.usd)) return null;
  return { usd: table, pct: (d / lead) * 100 };
}

/** A covey cell at the lead price: Claude Code's own figure for its lines. */
export const leadOf = (c: Pick<CostCell, "engine_usd">): number => c.engine_usd ?? 0;

// --- The meter -----------------------------------------------------------------

export type Range = { low: number; high: number };

/** The meter shows whole percents: n points of reported movement are a true
 *  movement somewhere in (n−1, n+1), never below 0. n = 0 gives [0, 1). */
export function meterRange(n: number): Range {
  return { low: Math.max(n - 1, 0), high: n + 1 };
}

export type RealCost = {
  value: number;
  low: number;
  high: number;
  points: number;
  /** No movement at all: the real cost is below one point's price. */
  below: boolean;
};

/** Real cost of n points at one price per point, with the range the whole
 *  percents leave. */
export function realCost(points: number, pricePerPoint: number): RealCost {
  const r = meterRange(points);
  return { value: points * pricePerPoint, low: r.low * pricePerPoint, high: r.high * pricePerPoint, points, below: points === 0 };
}

/** The price of one point of the weekly meter: the plan's price per week
 *  (a month is 52/12 weeks) over 100 points, converted with the fx rate stored
 *  with the plan. Null when a foreign-currency plan has no rate. */
export function pricePerPoint(plan: Pick<CredentialPlan, "amount" | "fx_to_usd" | "period"> & { currency?: string }): number | null {
  let fx = plan.fx_to_usd;
  if (!(fx > 0)) {
    if (plan.currency && plan.currency.toUpperCase() !== "USD") return null;
    fx = 1;
  }
  const usd = plan.amount * fx;
  const weekly = plan.period === "year" ? usd / 52 : (usd * 12) / 52;
  return weekly / 100;
}

/** The plan of a credential valid at t. */
export function planAt(plans: readonly CredentialPlan[], key: string, slot: number, t: Date): CredentialPlan | null {
  const ms = t.getTime();
  for (const p of plans) {
    if (p.secret_key !== key || p.secret_slot !== slot) continue;
    if (ms < Date.parse(p.valid_from)) continue;
    if (p.valid_to && ms >= Date.parse(p.valid_to)) continue;
    return p;
  }
  return null;
}

/** One week of the meter (a reset starts a new one). The dollars are the
 *  server's: each reading priced at the plan valid when it was read. */
export type MeterWeek = {
  points: number;
  /** The priced points' dollars, as the server priced them. */
  usd: number;
  /** Points read while no plan was valid: not in usd. */
  unpricedPoints: number;
  /** The price of one point in this week, for its ±1 point: the dearest
   *  rate the server priced a step at, else the plan valid at the week's
   *  last reading. Null when no plan was valid at all. */
  rate: number | null;
  /** Points read while the basis was usage (0151): their money is the usage
   *  charges, so they are neither in usd nor unpriced. */
  usagePoints: number;
  /** The whole week is the charges': no step of it was priced by the seat
   *  and it ended under the usage basis. It leaves no ±1 point of the seat. */
  usage: boolean;
};

export type RealFigure = RealCost & {
  /** Points read while no plan was valid: not in the value. */
  unpricedPoints: number;
  /** The weeks the window spans that the seat priced: each leaves ±1 point. */
  weeks: number;
  /** Points read under the usage basis (0151): the charges' money, not in
   *  the value and not in `points` — the value is the seat's part only. */
  usagePoints: number;
  /** Weeks that were the charges' altogether: in neither figure. */
  usageWeeks: number;
  /** Every point and every week's ±1 point has a plan price. Otherwise the
   *  value is only part of the movement, and no real figure (and no
   *  multiple) may be shown — "no plan price for N of M points" instead. */
  priced: boolean;
};

/** Real cost over the weeks of a window: every point as the server priced
 *  it, every week ±1 point (whole percents) at the dearest rate of that
 *  week, so the range is never narrower than the truth. */
export function realFromWeeks(weeks: readonly MeterWeek[]): RealFigure {
  let value = 0, low = 0, high = 0, points = 0, unpriced = 0, priced = true, usagePoints = 0, usageWeeks = 0;
  for (const w of weeks) {
    // Points under the usage basis (0151) are the charges' money: not the
    // seat's, so neither in its value nor in its points.
    usagePoints += w.usagePoints;
    unpriced += w.unpricedPoints;
    if (w.usage) {
      usageWeeks += 1;
      value += w.usd;
      points += Math.max(w.points - w.usagePoints, 0);
      continue;
    }
    const top = w.rate ?? 0;
    if (w.rate == null) priced = false;
    value += w.usd;
    points += Math.max(w.points - w.usagePoints, 0);
    // A week without movement still leaves "less than one point".
    low += w.points >= 1 ? Math.max(w.usd - top, 0) : 0;
    high += w.usd + top;
  }
  if (unpriced > 0) priced = false;
  const seatWeeks = weeks.length - usageWeeks;
  return { value, low, high, points, below: points === 0, unpricedPoints: unpriced, weeks: seatWeeks, priced, usagePoints, usageWeeks };
}

/** The coverage a credential's real cost is read from: the weekly all-models
 *  window, the source with the most readings (usage_cmd before manual before
 *  inband on a tie). Two sources read the same meter: never both. */
export function pickMeter(meter: readonly MeterCoverage[], key: string, slot: number): MeterCoverage | null {
  const order = ["usage_cmd", "manual", "inband"];
  const rank = (s: string) => (order.indexOf(s) < 0 ? order.length : order.indexOf(s));
  const own = meter.filter((m) => m.secret_key === key && m.secret_slot === slot && m.window_kind === "seven_day");
  own.sort((a, b) => b.readings - a.readings || rank(a.source) - rank(b.source));
  return own[0] ?? null;
}

/** A seat's weekly (or five-hour) window: the plan's meter. Any other window
 *  (overage, 0152) is a monthly meter of its own, never mixed into it. */
export const isWeeklyWindow = (w: string): boolean => w === "" || w.startsWith("seven_day") || w.startsWith("five_hour");

/** A credential's monthly meter (0152): its coverage of a window other than
 *  the weekly ones, the limit valid at its last reading (else its latest),
 *  and the money the server made of its movement (meter charges). */
export type MonthlyMeter = {
  coverage: MeterCoverage;
  limit: UsageLimit | null;
  charges: WindowCharge[];
};

/** The monthly meters of a credential, one per window: the source with the
 *  most readings, as pickMeter takes it. */
export function monthlyMeters(meter: readonly MeterCoverage[], limits: readonly UsageLimit[], charges: readonly WindowCharge[],
  key: string, slot: number): MonthlyMeter[] {
  const order = ["usage_cmd", "manual", "inband"];
  const rank = (s: string) => (order.indexOf(s) < 0 ? order.length : order.indexOf(s));
  const best = new Map<string, MeterCoverage>();
  for (const m of meter) {
    if (m.secret_key !== key || m.secret_slot !== slot || isWeeklyWindow(m.window_kind)) continue;
    const cur = best.get(m.window_kind);
    if (!cur || m.readings > cur.readings || (m.readings === cur.readings && rank(m.source) < rank(cur.source))) best.set(m.window_kind, m);
  }
  return [...best.values()].sort((a, b) => a.window_kind.localeCompare(b.window_kind)).map((cov) => {
    const own = limits.filter((l) => l.secret_key === key && l.secret_slot === slot && l.window_kind === cov.window_kind)
      .sort((a, b) => Date.parse(a.valid_from) - Date.parse(b.valid_from));
    const at = Date.parse(cov.last_at);
    const limit = own.find((l) => at >= Date.parse(l.valid_from) && (l.valid_to == null || at < Date.parse(l.valid_to))) ?? own.at(-1) ?? null;
    return { coverage: cov, limit,
      charges: charges.filter((c) => isMeterCharge(c) && c.secret_key === key && c.secret_slot === slot && c.window_kind === cov.window_kind) };
  });
}

/** The reset the latest reading of a credential's window names (resets_at), or null. */
export function windowResetsAt(readings: readonly UsageReading[], key: string, slot: number, window: string): string | null {
  let at = NaN, out: string | null = null;
  for (const r of readings) {
    if (r.secret_key !== key || r.secret_slot !== slot || r.window_kind !== window || !r.resets_at) continue;
    const t = Date.parse(r.fetched_at);
    if (!(t <= at)) { at = t; out = r.resets_at; }
  }
  return out;
}

/** A step's points that have no plan price: what the server says, and all of
 *  them when it priced none (usd null) — but for the points read under the
 *  usage basis (0151), whose money is the usage charges, not a plan. */
export function stepUnpriced(st: Pick<MeterStep, "points" | "usd" | "unpriced_points"> & { usage_points?: number }): number {
  return st.usd == null ? Math.max(st.points - (st.usage_points ?? 0), 0) : st.unpriced_points;
}

/** What the priced points of these steps were priced at: the one rate when
 *  they all agree (within rounding), `mixed` when two plans priced the span,
 *  null without a priced point. The server prices each reading at the plan
 *  valid when it was read, so the span's average belongs to no bar. */
export function stepRate(steps: readonly Pick<MeterStep, "points" | "usd" | "unpriced_points">[]): { rate: number | null; mixed: boolean } {
  let rate: number | null = null;
  for (const st of steps) {
    const priced = st.points - stepUnpriced(st);
    if (st.usd == null || priced <= 0) continue;
    const r = st.usd / priced;
    if (rate == null) rate = r;
    else if (Math.abs(r - rate) > rate * 1e-3) return { rate: null, mixed: true };
  }
  return { rate, mixed: false };
}

/** The weeks of a coverage with their steps' dollars as the server priced
 *  them — each reading at the plan valid when it was read, never at the
 *  bucket's start. A step belongs to the last week that began before the
 *  step's period ended. Movement no step accounts for has no price. */
export function meterWeeks(cov: MeterCoverage, steps: readonly MeterStep[], plans: readonly CredentialPlan[], bucketMs: number,
  bases?: readonly CredentialBasis[] | null): MeterWeek[] {
  const segs = cov.segments && cov.segments.length > 0
    ? cov.segments
    : [{ start_at: cov.baseline_at, end_at: cov.last_at, from_percent: cov.baseline_percent, to_percent: cov.last_percent, reset: false, points: cov.points }];
  const own = steps.filter((s) => s.secret_key === cov.secret_key && s.secret_slot === cov.secret_slot
    && s.source === cov.source && s.window_kind === cov.window_kind);
  const weeks: MeterWeek[] = segs.map((s) => ({ points: s.points, usd: 0, unpricedPoints: 0, rate: null, usagePoints: 0, usage: false }));
  let stepped = 0;
  for (const st of own) {
    if (st.points === 0) continue;
    const start = Date.parse(st.period), end = start + bucketMs;
    let wi = 0;
    segs.forEach((s, i) => { if (Date.parse(s.start_at) < end) wi = i; });
    const w = weeks[wi];
    const unpriced = stepUnpriced(st);
    stepped += st.points;
    w.unpricedPoints += unpriced;
    w.usagePoints += st.usage_points ?? 0;
    w.usd += st.usd ?? 0;
    if (st.points - unpriced > 0 && st.usd != null) w.rate = Math.max(w.rate ?? 0, st.usd / (st.points - unpriced));
  }
  // Movement that no step carries is movement without a price.
  const missing = segs.reduce((a, s) => a + s.points, 0) - stepped;
  if (missing > 0) weeks[weeks.length - 1].unpricedPoints += missing;
  segs.forEach((s, i) => {
    const w = weeks[i];
    // A week without a priced step still leaves its ±1 point, at the plan
    // valid at its last reading — unless the basis was usage then (0151):
    // that week is the charges', the seat priced none of it.
    if (w.rate == null) {
      if (basisAt(bases, cov.secret_key, cov.secret_slot, new Date(s.end_at)) === "usage") {
        w.usage = true;
        return;
      }
      const p = planAt(plans, cov.secret_key, cov.secret_slot, new Date(s.end_at));
      w.rate = p ? pricePerPoint(p) : null;
    }
  });
  return weeks;
}

/** list $ per real $: M/(Δ·pp), range M/((Δ+1)·pp) to M/((Δ−1)·pp).
 *  Δ ≥ 3: an estimate with its range. 1 ≤ Δ ≤ 2: the range only (at Δ = 1
 *  without an upper bound). Δ = 0: no figure — the real cost is only known to
 *  be below one point's price. */
export type Multiple =
  | { kind: "estimate"; value: number; low: number; high: number }
  | { kind: "range"; low: number; high: number }
  /** A lower bound only: covey's list over a meter that may also carry use
   *  outside covey nobody reported, so covey's real cost is at most the
   *  whole movement's (#120). */
  | { kind: "floor"; low: number };

/** A multiple with both ends of its range. */
export type RangedMultiple = Exclude<Multiple, { kind: "floor" }>;

export function multiple(M: number, delta: number, pp: number): RangedMultiple | null {
  return multipleOf(M, realCost(delta, pp));
}

/** The multiple from a real-cost figure (several weeks, several plans). */
export function multipleOf(M: number, real: Pick<RealCost, "value" | "low" | "high" | "points">): RangedMultiple | null {
  if (!(M > 0) || real.points < 1 || !(real.high > 0)) return null;
  const low = M / real.high;
  const high = real.low > 0 ? M / real.low : Infinity;
  if (real.points < 3) return { kind: "range", low, high };
  return { kind: "estimate", value: M / real.value, low, high };
}

/** The multiple of one period of the chart, by the tile's rule: a period is
 *  only a few points (an hour 1–3), each step's points as the server priced
 *  them, every run of readings ±1 point at the dearest rate in the period —
 *  so below 3 points there is no figure, as the tile shows none. No figure
 *  either where the list price is under a cent, or where the estimate would
 *  read 0.0×: neither says anything about the period. */
export function periodMultiple(list: number, steps: readonly Pick<MeterStep, "points" | "usd" | "unpriced_points" | "resets">[]): RangedMultiple | null {
  if (!(list >= 0.01)) return null;
  let points = 0, value = 0, rate = 0, runs = 0;
  for (const st of steps) {
    // Points without a plan price: the period's real cost is not known.
    if (st.points > 0 && (st.usd == null || stepUnpriced(st) > 0)) return null;
    points += st.points;
    value += st.usd ?? 0;
    runs += 1 + Math.max(st.resets ?? 0, 0);
    if (st.points > 0) rate = Math.max(rate, (st.usd ?? 0) / st.points);
  }
  if (points < 3) return null;
  const m = multipleOf(list, { value, low: Math.max(value - runs * rate, 0), high: value + runs * rate, points });
  if (!m || m.kind !== "estimate" || m.value < 0.05) return null;
  return m;
}

// --- The bill --------------------------------------------------------------------

export type BilledStatus = { state: "complete" | "pending"; completeTo: Date | null };

/** Anthropic's data lands 4–24 h late; data_refreshed_at marks how far it is
 *  complete. A window ending after it is pending (complete up to that mark). */
export function billedStatus(windowEnd: Date, dataRefreshedAt: string | null | undefined): BilledStatus {
  if (!dataRefreshedAt) return { state: "pending", completeTo: null };
  const r = new Date(dataRefreshedAt);
  return r.getTime() >= windowEnd.getTime() ? { state: "complete", completeTo: r } : { state: "pending", completeTo: r };
}

/** What can be said about an API key's bill:
 *  - unavailable:  the bill could not be read from covey at all;
 *  - unconfigured: no anthropic_admin_key is stored;
 *  - unmapped:     the credential is not mapped to an Anthropic API key;
 *  - failed:       nothing billed in the window and the last sync failed;
 *  - not_landed:   nothing billed yet and the window is not complete;
 *  - no_bucket:    the range holds no whole billed hour or day (the window is
 *                  empty): there is nothing to sum, which is not "$0.00";
 *  - measured:     a figure — $0.00 only when configured, mapped and complete.
 *  Only "measured" carries a dollar figure; every other state is "no figure",
 *  never "$0.00". */
export type BilledState = "unavailable" | "unconfigured" | "unmapped" | "failed" | "not_landed" | "no_bucket" | "measured";

export type BilledFigure = {
  state: BilledState;
  usd: number;
  listUsd: number;
  ratio: number | null;
  /** Claude Code's figure and covey's recomputed list for the SAME window as the bill. */
  reported: number;
  recomputed: number;
  /** The window the bill covers: whole billed buckets (1h or 1d) from the
   *  first one at or after the page's start. */
  window: { start: Date; end: Date; comparedUntil: Date; width: string } | null;
  status: BilledStatus;
  /** The last sync's error when it failed (it carries no secret). */
  error: string;
};

/** The bill of one API-key credential, from the server's window totals —
 *  never from the chart's points, which cover whole buckets back to the
 *  midnight before the page's start. */
export function billedFigure(report: BilledReport | null | undefined, key: string, slot: number, windowEnd: Date): BilledFigure {
  const sync = report?.sync;
  const w = report?.window;
  const window = w
    ? { start: new Date(w.start), end: new Date(w.end), comparedUntil: new Date(w.compared_until), width: w.width }
    : null;
  const base: BilledFigure = {
    state: "measured", usd: 0, listUsd: 0, ratio: null, reported: 0, recomputed: 0, window,
    status: billedStatus(window?.end ?? windowEnd, sync?.data_refreshed_at),
    error: sync?.last_status === "failed" ? sync.last_error ?? "" : "",
  };
  if (!report || !sync || !w) return { ...base, state: "unavailable" };
  if (!sync.configured) return { ...base, state: "unconfigured" };
  const tot = (w.totals ?? []).find((t) => t.secret_key === key && t.secret_slot === slot);
  if (!tot) return { ...base, state: "unmapped" };
  // A range inside one hour, or one without a whole UTC day beyond the hourly
  // reach, ends where it starts (the server's BilledWindowFor): its totals
  // are empty, and the sync being complete says nothing about them.
  if (window && window.end.getTime() <= window.start.getTime()) return { ...base, state: "no_bucket" };
  const usd = Number(tot.billed_usd) || 0, list = Number(tot.billed_list_usd) || 0;
  const fig: BilledFigure = { ...base, usd, listUsd: list, ratio: list > 0 ? usd / list : null,
    reported: tot.reported_usd, recomputed: tot.list_usd };
  if (!tot.has_billed && base.status.state !== "complete") {
    return { ...fig, state: sync.last_status === "failed" ? "failed" : "not_landed" };
  }
  return fig;
}

/** The start of the UTC bucket an instant falls in, cut as the server cuts
 *  its periods (date_trunc in UTC: hour, day, the week from Monday, month;
 *  date_bin from the epoch: 10 minutes, 10 hours). */
export function bucketStart(ms: number, bucket: string): number {
  const d = new Date(ms);
  const day = Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate());
  switch (bucket) {
    case "10min":
      return Math.floor(ms / 600_000) * 600_000;
    case "10hour":
      return Math.floor(ms / 36_000_000) * 36_000_000;
    case "hour":
      return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate(), d.getUTCHours());
    case "week":
      return day - ((d.getUTCDay() + 6) % 7) * 86400_000;
    case "month":
      return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), 1);
    default:
      return day;
  }
}

/** The bill's granularity to ask the server for: hours for an hourly chart
 *  and for a window the bill knows per hour (within 48 h), else UTC days —
 *  both cut cleanly at the billed window's start and add up into any
 *  coarser chart bucket. Weeks are never asked for: a billed week can start
 *  before the window. A 10-hour (or 10-minute) chart takes hours at any
 *  range: a UTC day does not fit into epoch-aligned 10-hour buckets, so each
 *  day's bill would land whole in one of them and the others show $0. */
export function billedFetchBucket(days: number, bucket: string): "hour" | "day" {
  return bucket === "hour" || bucket === "10hour" || bucket === "10min" || days <= 2 ? "hour" : "day";
}

/** Chart buckets shorter than a UTC day: a day of the bill read whole
 *  (whole_days) fits none of them. */
function subDay(bucket: string): boolean {
  return bucket === "hour" || bucket === "10hour" || bucket === "10min";
}

/** The points of one credential inside the window the billed tile covers. */
function inWindow(report: BilledReport | null | undefined, points: readonly BilledPoint[] | null | undefined,
  key: string, slot: number): BilledPoint[] {
  const w = report?.window;
  if (!w) return [];
  const start = Date.parse(w.start), end = Date.parse(w.end);
  return (points ?? []).filter((p) => {
    if (p.secret_key !== key || p.secret_slot !== slot) return false;
    const at = Date.parse(p.bucket);
    return at >= start && at < end;
  });
}

/** The bill of one credential per chart period (keyed by the period's start
 *  in ms): only the points inside the window the billed tile covers, summed
 *  into the chart's buckets — so the bars cover the same window and buckets
 *  as the rest of the chart. A day the server read whole (whole_days) goes
 *  into a day, week or month bar; at hour, 10 h and 10 min it fits no bar
 *  and is kept apart (billedWholeDays). Bars and that part together are the
 *  tile's figure. */
export function billedPerPeriod(report: BilledReport | null | undefined, key: string, slot: number, bucket: string): Map<number, number> {
  const out = new Map<number, number>();
  const points = [...inWindow(report, report?.points, key, slot),
    ...(subDay(bucket) ? [] : inWindow(report, report?.whole_days, key, slot))];
  for (const p of points) {
    const k = bucketStart(Date.parse(p.bucket), bucket);
    out.set(k, (out.get(k) ?? 0) + (Number(p.billed_usd) || 0));
  }
  return out;
}

/** The part of one credential's tile that no bar of a chart finer than a
 *  day holds: the days in the window the bill is read for as a whole. 0 at
 *  a day bucket or coarser, where those days are in the bars. */
export function billedWholeDays(report: BilledReport | null | undefined, key: string, slot: number, bucket: string): number {
  if (!subDay(bucket)) return 0;
  return inWindow(report, report?.whole_days, key, slot).reduce((a, p) => a + (Number(p.billed_usd) || 0), 0);
}

// --- The basis: seat, usage, or both (0151) --------------------------------------

/** The basis of a credential valid at t; null where none was set (the
 *  behaviour from before 0151: a seat by its plan, an API key by its bill). */
export function basisAt(bases: readonly CredentialBasis[] | null | undefined, key: string, slot: number, t: Date): CostBasisKind | null {
  const ms = t.getTime();
  for (const b of bases ?? []) {
    if (b.secret_key !== key || b.secret_slot !== slot) continue;
    if (ms < Date.parse(b.valid_from)) continue;
    if (b.valid_to && ms >= Date.parse(b.valid_to)) continue;
    return b.basis;
  }
  return null;
}

/** The counted usage charges of one currency, in that currency. usd: their
 *  dollar figure, only when every one of them has one (in USD, or a rate with
 *  its date); never a conversion of the page's own. */
export type UsageCurrency = {
  currency: string; counted: number; usd: number | null;
  /** The charges entered by hand among them (0152: not the meter's). */
  charges: number;
  /** Of `counted`, what the monthly meter counted (0152); absent without one. */
  meter?: number;
};

// --- Usage charge kinds (0152) ---------------------------------------------------

/** A charge entered by hand: money, counted under a usage basis. */
export const isHandCharge = (c: Pick<WindowCharge, "kind">): boolean => c.kind == null || (c.kind as string) === "" || (c.kind as string) === "charge";
/** A usage export: what the provider said, entered to check — never money. */
export const isExportCharge = (c: Pick<WindowCharge, "kind">): boolean => c.kind === "export";
/** The money a monthly usage limit makes of its meter's movement: no id of its own. */
export const isMeterCharge = (c: Pick<WindowCharge, "kind">): boolean => c.kind === "meter";
/** Only what is stored can be deleted: a meter charge is computed. */
export const chargeDeletable = (c: Pick<WindowCharge, "kind">): boolean => !isMeterCharge(c);
/** A key for a charge in a list: its id, but a meter charge has the zero id
 *  (several of them share it), so its window, span and limit name it. */
export function chargeKey(c: Pick<WindowCharge, "id" | "kind" | "window_kind" | "from" | "to" | "limit_amount" | "currency">): string {
  return isMeterCharge(c) ? `meter:${c.window_kind ?? ""}:${c.from}:${c.to}:${c.limit_amount ?? ""}${c.currency}` : c.id;
}

/** What the basis was over the window: seat stands for "none set" too (the
 *  plan prices the meter, as before 0151). */
export type Regime = CostBasisKind;

export type UsageFigure = {
  /** The basis in effect at the window's end; null: none (none set, or only
   *  one that starts later). Never a basis that has not begun. */
  basis: CostBasisKind | null;
  /** The bases that held within the window, in order (seat also for the
   *  time before the first one). */
  regimes: Regime[];
  /** More than one basis held within the window. */
  changed: boolean;
  /** Bases entered to start after the window's end: listed "from <date>",
   *  in effect nowhere yet. */
  upcoming: CredentialBasis[];
  /** The charges that reach into the window, as the server cut them. */
  charges: WindowCharge[];
  /** What the charges count, per currency, never added across currencies. */
  currencies: UsageCurrency[];
  /** The counted total in USD: only when every currency has a dollar figure. */
  usd: number | null;
  /** Charges in the window that count nowhere: no usage basis at their time.
   *  Not an export (a check, never counted) and not a charge the monthly
   *  meter superseded (0152). */
  uncounted: WindowCharge[];
  /** The usage exports in the window (0152), each beside the counted figure
   *  over its span. */
  exports: ExportCheck[];
};

/** A usage export beside the counted figure (0152). */
export type ExportCheck = {
  charge: WindowCharge;
  /** The counted charges (by hand and the meter's) of its currency over the
   *  export's span within the window, pro rata by time; null where none of
   *  its currency counts there. */
  counted: number | null;
  /** counted − the export's part in the window; null without a counted figure. */
  diff: number | null;
  /** Only charges of another currency count over its span: no difference. */
  otherCurrency: boolean;
};

/** Each export of a credential beside what its counted charges say over the
 *  same span within [windowStart, windowEnd), in its own currency only. */
export function exportChecks(charges: readonly WindowCharge[], windowStart: number, windowEnd: number): ExportCheck[] {
  const part = (c: WindowCharge): [number, number] => [Math.max(Date.parse(c.from), windowStart), Math.min(Date.parse(c.to), windowEnd)];
  const counted = charges.filter((c) => !isExportCharge(c) && c.counted_amount > 0);
  return charges.filter(isExportCharge).map((e) => {
    const [ef, et] = part(e);
    let sum: number | null = null, other = false;
    for (const c of counted) {
      const [cf, ct] = part(c);
      const over = Math.min(ct, et) - Math.max(cf, ef);
      if (!(over > 0) || !(ct > cf)) continue;
      if (c.currency.toUpperCase() !== e.currency.toUpperCase()) { other = true; continue; }
      sum = (sum ?? 0) + c.counted_amount * Math.min(over / (ct - cf), 1);
    }
    return { charge: e, counted: sum, diff: sum == null ? null : sum - e.window_amount, otherCurrency: sum == null && other };
  });
}

/** The counted parts of charges, per currency in that currency: a dollar
 *  figure only where every one of them has one; never added across
 *  currencies. */
export function usageCurrencies(charges: readonly WindowCharge[]): UsageCurrency[] {
  const by = new Map<string, UsageCurrency>();
  for (const c of charges) {
    // An export is a check, never money (0152), whatever it carries.
    if (!(c.counted_amount > 0) || isExportCharge(c)) continue;
    const cur = c.currency.toUpperCase();
    const u: UsageCurrency = by.get(cur) ?? { currency: cur, counted: 0, usd: 0, charges: 0 };
    u.counted += c.counted_amount;
    u.usd = u.usd == null || c.counted_usd == null ? null : u.usd + c.counted_usd;
    if (isMeterCharge(c)) u.meter = (u.meter ?? 0) + c.counted_amount;
    else u.charges += 1;
    by.set(cur, u);
  }
  return [...by.values()].sort((a, b) => a.currency.localeCompare(b.currency));
}

/** The window's start the server cut the charges at (cells.since, sent with
 *  any basis or charge), else the first period's. */
export function windowStartOf(cells: Pick<CostCells, "since" | "periods">): Date | null {
  const s = cells.since ?? cells.periods?.[0];
  return s ? new Date(s) : null;
}

/** A credential's basis and usage charges over the window; null when it has
 *  neither, and then nothing on the page changes. */
export function usageFigure(cells: Pick<CostCells, "cost_bases" | "usage_charges"> & Partial<Pick<CostCells, "since" | "periods">>,
  key: string, slot: number, windowEnd: Date): UsageFigure | null {
  const bases = (cells.cost_bases ?? []).filter((b) => b.secret_key === key && b.secret_slot === slot)
    .sort((a, b) => Date.parse(a.valid_from) - Date.parse(b.valid_from));
  const charges = (cells.usage_charges ?? []).filter((c) => c.secret_key === key && c.secret_slot === slot);
  if (bases.length === 0 && charges.length === 0) return null;
  const end = windowEnd.getTime();
  const start = cells.since ? Date.parse(cells.since) : null;
  // Only what is in effect: a basis that starts later is listed, not used.
  const basis = basisAt(bases, key, slot, windowEnd);
  const held = bases.filter((b) => Date.parse(b.valid_from) <= end && (b.valid_to == null || start == null || Date.parse(b.valid_to) > start));
  const regimes: Regime[] = [];
  // Before the first basis the plan priced the meter (none set = seat).
  if (held.length === 0 || (start != null && Date.parse(held[0].valid_from) > start)) regimes.push("seat");
  for (const b of held) if (regimes.at(-1) !== b.basis) regimes.push(b.basis);
  const currencies = usageCurrencies(charges);
  const usd = currencies.every((c) => c.usd != null) ? currencies.reduce((a, c) => a + c.usd!, 0) : null;
  return {
    basis, regimes, changed: regimes.length > 1, upcoming: bases.filter((b) => Date.parse(b.valid_from) > end),
    charges, currencies, usd,
    uncounted: charges.filter((c) => !isExportCharge(c) && c.window_amount > 0 && !(c.counted_amount > 0) && !((c.superseded_amount ?? 0) > 0)),
    exports: exportChecks(charges, start ?? -Infinity, end),
  };
}

/** The spans [from, to) in ms the counted charges cover within the window,
 *  cut to the times a usage basis held: what their money is for. */
export function countedSpans(u: UsageFigure, bases: readonly CredentialBasis[] | null | undefined, key: string, slot: number,
  windowStart: number, windowEnd: number): [number, number][] {
  const usage = (bases ?? []).filter((b) => b.secret_key === key && b.secret_slot === slot && b.basis !== "seat")
    .map((b): [number, number] => [Date.parse(b.valid_from), b.valid_to ? Date.parse(b.valid_to) : Infinity]);
  const out: [number, number][] = [];
  for (const c of u.charges) {
    if (!(c.counted_amount > 0)) continue;
    const from = Math.max(Date.parse(c.from), windowStart), to = Math.min(Date.parse(c.to), windowEnd);
    for (const [bf, bt] of usage) {
      const a = Math.max(from, bf), b = Math.min(to, bt);
      if (b > a) out.push([a, b]);
    }
  }
  return out.sort((x, y) => x[0] - y[0]);
}

/** Covey's recomputed list $ of a credential's cells over the given spans:
 *  each cell's period [p, p + bucket), cut to the window, counts by the part
 *  of it the spans cover — pro rata by time at the periods the spans begin
 *  or end in, as the charges themselves are cut. */
export function listOverSpans<T extends Pick<CostCell, "period">>(cells: readonly T[], spans: readonly [number, number][], bucketMs: number,
  windowStart: number, windowEnd: number, value: (c: T) => number = (c) => recomputedOf(c as unknown as CostCell)): number {
  let list = 0;
  for (const c of cells) {
    const p = Date.parse(c.period);
    const from = Math.max(p, windowStart), to = Math.min(p + bucketMs, windowEnd);
    if (!(to > from)) continue;
    let covered = 0;
    for (const [a, b] of spans) covered += Math.max(0, Math.min(b, to) - Math.max(a, from));
    if (covered > 0) list += value(c) * Math.min(covered / (to - from), 1);
  }
  return list;
}

// --- Per credential --------------------------------------------------------------

export type AgentFigure = {
  id: string;
  label: string;
  reported: number;
  recomputed: number;
  /** (recomputed − reported) / reported, in %. */
  gapPct: number | null;
  /** The agent's share of the credential's recomputed list $. */
  share: number | null;
  /** share × the credential's real cost: an ESTIMATE (the meter is account-wide).
   *  Once sessions outside covey report on the meter (#120), the server's
   *  figure: the agent's part of covey's share of each split step — never
   *  the movement of a step they did not report. */
  realShare: number | null;
  /** "upTo": realShare is a bound (≤): the meter may carry use outside
   *  covey that nothing reports; "estimate" (≈) otherwise — on a split seat
   *  too, where covey's part is the server's per-chunk estimate (F3c). */
  realBound: "estimate" | "upTo";
  /** Why realShare is null although the seat is priced: "unsplit", the
   *  agent ran only in steps that were not split. */
  realWhy: "unsplit" | null;
  /** Its lines on the seat fall only in a reading's span that began before
   *  the range: a real share, no list price in the range. */
  beforeWindow: boolean;
};

/** Why a credential billed by usage has no multiple: "rate" (a counted
 *  charge has no dollar figure: no rate with its date), "noCharge" (nothing
 *  counted in the window), "spanList" (covey ran nothing at list price over
 *  the spans the counted charges cover), "basisChanged" (the basis changed
 *  within the window: the seat's part and the charges' part are different
 *  money over different spans), "seatAndUsage" (the seat's meter and the
 *  charges cover different spans). */
export type NoUsageMultiple = "rate" | "noCharge" | "spanList" | "basisChanged" | "seatAndUsage";

export type CredentialFigures = {
  id: string;
  secretKey: string | null;
  slot: number | null;
  /** subscription | api_key | "" (unknown) */
  kind: string;
  /** The plan valid at the window's end (else the last one in it). */
  plan: CredentialPlan | null;
  reported: number;
  recomputed: number;
  /** Price-table check: (recomputed − reported) / reported, in %. */
  check: number | null;
  /** Booked without a line (the cells' "unattributed" cost type): not in
   *  reported or recomputed. Not the meter's unattributed part (F3c). */
  bookedWithoutLine: number;
  meter: MeterCoverage | null;
  real: RealFigure | null;
  /** Recomputed list $ of the credential's lines between the meter's baseline
   *  and its last reading, by the lines' own timestamps (the server's figure:
   *  the cells hold only part of a bucket at both ends of the window, and the
   *  baseline can lie before it). Null without one. */
  aligned: number | null;
  multiple: Multiple | null;
  /** Why there is no multiple: "agents" (filtered or per agent), "movement" (< 3 points), "meter" (no reading),
   *  "unpriced" (points without a plan price), "outside" (sessions outside covey report on this meter: the
   *  headline's multiples split it, covey's list over the whole movement would not). */
  noMultiple: "agents" | "movement" | "meter" | "unpriced" | "outside" | NoUsageMultiple | null;
  /** How the credential's real money is formed (0151): null where none was
   *  set — a seat by its plan, an API key by its bill, as before. */
  basis: CostBasisKind | null;
  /** Its basis history and usage charges over the window; null with neither. */
  usage: UsageFigure | null;
  /** On the usage basis: covey's recomputed list $ over the spans the
   *  counted charges cover (usageList) per counted usage $. Only with a
   *  dollar figure for every counted charge. */
  usageMultiple: number | null;
  /** Covey's recomputed list $ over the spans the counted charges cover,
   *  where the multiple is offered; null otherwise. */
  usageList: number | null;
  /** The outside sources that run on this credential while its usage
   *  multiple is offered: the charges pay for their use too, so the
   *  multiple is a lower bound ("at least"); null when none does. */
  usageShared: string[] | null;
  /** Every reading of its meter in the window was under the usage basis
   *  (0151): nothing of it is split, and nothing waits for a report. */
  meterOnUsage: boolean;
  /** The meter's real cost split between covey and the sessions outside it
   *  (#120), per chunk of readings by the chunk's list-price share; null
   *  until a source names a meter, and for a seat with no split step. */
  split: ExternalMeterTotal | null;
  /** No source names this seat: split (when it is) between covey's agents
   *  and the unattributed part only, and its meter may carry use outside
   *  covey nothing reports. */
  unlinked: boolean;
  /** Sources name a meter, but no source names this seat, or its machines
   *  have not reported through any of its readings: its meter may carry use
   *  outside covey, so covey's figures on it are bounds. */
  outsideUnreported: boolean;
  billed: BilledFigure | null;
  /** Bookings of an older daemon's running totals (legacy, cumulative). */
  bookingCheck: BookingBasis[];
  agents: AgentFigure[];
  /** Every agent's recomputed list $ on the credential, the agent filter aside. */
  listAll: number;
  /** With the outside sessions included (the page's default, #120): the
   *  multiple of everything on this meter, on a seat a source names — org-wide,
   *  under an agent filter too; null otherwise. */
  everything: EverythingMultiple | null;
  /** Its monthly meters (0152): windows other than the weekly one, each on
   *  its own, never in `meter` or `real`. Absent: none. */
  monthly?: MonthlyMeter[];
};

/** List $ per real $ of everything on one meter (#120): covey's agents, the
 *  outside sessions and the unattributed movement — the list price of covey
 *  and the outside sessions within the meter's priced readings in the range
 *  (seatLists), over its whole real cost in them. A lower bound: list /
 *  (real + slack), the slack the whole percents and the lag leave
 *  (meterSlack); unpriced outside calls are not in the list, and use nobody
 *  reported is in the real cost. List price after the last reading has no
 *  real cost yet: it is in the page's totals and stands beside the multiple
 *  (unmetered). */
export type EverythingMultiple = {
  /** Within the meter's priced readings, in the range, at the lead price
   *  (reported where a call carries it, else list). */
  coveyList: number;
  outsideList: number;
  /** Some listed source on the seat had a list price: otherwise the outside
   *  part is "no list price", never $0. */
  outsidePriced: boolean;
  /** The seat's outside calls in the range, and their tokens of models with no list price. */
  outsideCalls: number;
  outsideUnpricedTokens: number;
  real: number;
  slack: number;
  points: number;
  /** The floor's numerator: the server's floor_list_usd where it sends one, else coveyList + outsideList. */
  floorList: number;
  multiple: FloorMultiple | null;
  /** list / real: the estimate beside the floor; null with no multiple. */
  estimate: number | null;
  /** Why there is none: "movement" (< 3 points), "unpriced" (points without a plan price), "list" (no list price),
   *  "usage" (a basis other than the seat held in the range, 0151: the meter is not, or not all, the money; the
   *  credential's usage multiple and its rules stand instead). */
  why: "movement" | "unpriced" | "list" | "usage" | null;
  unpricedPoints: number;
  /** Points read while the seat was billed by usage (0151): in neither the real cost nor the list it divides. */
  usagePoints: number;
  /** List price in no reading's span (or with no reading): no real share. Null where the server does not say. */
  unmeteredCovey: number | null;
  unmeteredOutside: number;
  /** Of it, covey's and the outside sessions' part before the meter's first
   *  reading in the range (no meter data for that time: never a real share)
   *  and after its last one (pending until it is read again). */
  before: { covey: number | null; outside: number };
  after: { covey: number | null; outside: number };
  /** The lists at covey's price table (Recomputed), for the price-table check. */
  table: { coveyList: number; outsideList: number };
  /** Outside calls priced at the list for want of a reported cost. */
  outsideRecomputed: number;
};

/** The price of a point for a seat's slack: the dearest the server priced a
 *  step of it at, else the plan valid now (or its last one), else the whole
 *  meter's dearest. */
function slackRate(plans: readonly CredentialPlan[], fallback: number) {
  return (c: ExternalMeterTotal): number => {
    if (c.point_rate > 0) return c.point_rate;
    const own = plans.filter((p) => p.secret_key === c.secret_key && p.secret_slot === c.secret_slot);
    const p = planAt(plans, c.secret_key, c.secret_slot, new Date()) ?? own.at(-1) ?? null;
    return (p ? pricePerPoint(p) : null) ?? fallback;
  };
}

/** The slack of the one real cost over these seats (#120 review, M1): the
 *  meter's movement is known to ±2 points per seat that has list price or
 *  movement in the range — the whole percents at both ends and a point of
 *  lag, whether or not it moved — and 2 more per weekly reset, which loses
 *  the closed week's end truncation and its lag. At each seat's price per
 *  point. The headline and each seat's multiple of everything use this one
 *  rule, so they agree. A reading under the usage basis (0151) is in no
 *  real cost, so it brings no slack: a seat read only under it has none. */
export function meterSlack(seats: readonly ExternalMeterTotal[], steps: readonly ExternalMeterStep[],
  rateOf: (c: ExternalMeterTotal) => number, hasList: (c: ExternalMeterTotal) => boolean = () => false): number {
  let slack = 0;
  for (const c of seats) {
    if (c.step_count > 0 && (c.usage_steps ?? 0) === c.step_count) continue;
    let resets = 0, list = false;
    for (const st of steps) {
      if (st.secret_key !== c.secret_key || st.secret_slot !== c.secret_slot || st.reason === "usage") continue;
      if (st.reset) resets += 1;
      if (st.covey_list_usd + st.external_list_usd > 0) list = true;
    }
    if (!(c.points > 0 || list || resets > 0 || hasList(c))) continue;
    slack += 2 * (1 + resets) * rateOf(c);
  }
  return slack;
}

/** A step's share of the range: its span (from, at] cut at since, by time —
 *  only the first reported step of a seat can straddle it. */
export function inRange(st: Pick<ExternalMeterStep, "from" | "at">, since: number | null): number {
  const from = Date.parse(st.from), at = Date.parse(st.at);
  if (since == null || !(from < since)) return 1;
  if (!(at > since) || !(at > from)) return 0;
  return (at - since) / (at - from);
}

/** One seat's list price in the range, in its parts (#120 review, M2): the
 *  range's figures (covey's lines on the seat, the listed sources that name
 *  it), the part in no reading's span (the server's, no real cost) — before
 *  the meter's first reading in the range (no meter data for that time,
 *  never a real share) or after its last one (pending) —, the part in
 *  readings with no plan price (those steps' lists, the one straddling the
 *  range's start by time), and the rest, `metered`: the list within the
 *  priced readings in the range — what the multiple divides. The readings
 *  taken while the seat was billed by usage (0151) are a part of their own,
 *  `usage`: no real cost of the meter, their money is the usage charges. So
 *  range = metered + before + after + unpriced + usage (unmetered = before
 *  + after), in each party; a reading's span that began before the range
 *  brings none of its earlier list in. `lead` holds the same parts at the
 *  lead price (reported where a call carries it, else list), over the same
 *  calls and spans. */
export type SeatParts = { range: number; metered: number; unmetered: number; before: number; after: number; unpriced: number; usage: number };
type OutsideParts = Omit<SeatParts, "range"> & {
  /** null: the listed sources on the seat had calls, none with a price. */
  range: number | null; calls: number; unpricedTokens: number;
};
export type SeatLists = {
  covey: SeatParts;
  outside: OutsideParts;
  lead: { covey: SeatParts; outside: OutsideParts & { recomputed: number } };
};

export function seatLists(cells: CostCells, seat: ExternalMeterTotal): SeatLists {
  const ext = cells.external;
  const id = credId(seat.secret_key, seat.secret_slot);
  let covey = 0, coveyLead = 0;
  for (const c of cells.cells) {
    if (credId(c.secret_key, c.secret_slot) !== id) continue;
    covey += recomputedOf(c);
    coveyLead += leadOf(c);
  }
  const real = sourceRealParts(ext);
  let outside: number | null = null, calls = 0, unpricedTokens = 0;
  let outsideLead: number | null = null, recomputed = 0, leadUnpricedTokens = 0;
  for (const s of ext?.sources ?? []) {
    if (!listedSource(s, real) || s.meter_credential?.secret_key !== seat.secret_key || s.meter_credential.secret_slot !== seat.secret_slot) continue;
    if (s.list_usd != null) outside = (outside ?? 0) + s.list_usd;
    calls += s.calls;
    unpricedTokens += s.unpriced_tokens;
    const l = sourceLead(s, ext!.cells);
    if (l.lead != null) outsideLead = (outsideLead ?? 0) + l.lead;
    recomputed += l.recomputed;
    leadUnpricedTokens += l.unpricedTokens;
  }
  if (outside == null && calls === 0) outside = 0;
  if (outsideLead == null && calls === 0) outsideLead = 0;
  const since = cells.since ? Date.parse(cells.since) : null;
  // [covey, outside] of the readings with no plan price and under usage, at both prices.
  const unpriced = [0, 0], usage = [0, 0], unpricedLead = [0, 0], usageLead = [0, 0];
  for (const st of ext?.meter.steps ?? []) {
    if (st.secret_key !== seat.secret_key || st.secret_slot !== seat.secret_slot || st.real_usd != null) continue;
    const f = inRange(st, since);
    const [list, lead] = st.reason === "usage" ? [usage, usageLead] : [unpriced, unpricedLead];
    list[0] += st.covey_list_usd * f;
    list[1] += st.external_list_usd * f;
    lead[0] += (st.covey_lead_usd ?? st.covey_list_usd) * f;
    lead[1] += (st.external_lead_usd ?? st.external_list_usd) * f;
  }
  const parts = (range: number, unmetered: number, before: number, unp: number, use: number) => ({
    metered: Math.max(0, range - unmetered - unp - use), unmetered, before, after: Math.max(0, unmetered - before), unpriced: unp, usage: use,
  });
  // An older server sends no lead and no before parts: the list stands in
  // for the lead, and everything unmetered is after the last reading.
  const uc = seat.unmetered_covey_list_usd ?? 0, uo = seat.unmetered_list_usd;
  const ucLead = seat.unmetered_covey_lead_usd ?? uc, uoLead = seat.unmetered_lead_usd ?? uo;
  return {
    covey: { range: covey, ...parts(covey, uc, seat.unmetered_covey_before_list_usd ?? 0, unpriced[0], usage[0]) },
    outside: { range: outside, ...parts(outside ?? 0, uo, seat.unmetered_before_list_usd ?? 0, unpriced[1], usage[1]), calls, unpricedTokens },
    lead: {
      covey: { range: coveyLead, ...parts(coveyLead, ucLead, seat.unmetered_covey_before_lead_usd ?? 0, unpricedLead[0], usageLead[0]) },
      outside: { range: outsideLead, ...parts(outsideLead ?? 0, uoLead, seat.unmetered_before_lead_usd ?? 0, unpricedLead[1], usageLead[1]),
        calls, unpricedTokens: leadUnpricedTokens, recomputed },
    },
  };
}

const seatHasList = (cells: CostCells) => (c: ExternalMeterTotal) => {
  const l = seatLists(cells, c);
  return l.covey.range > 0 || (l.outside.range ?? 0) > 0;
};

/** The multiple of everything on a seat's meter; null for a seat no source names. */
export function everythingOnMeter(cells: CostCells | null | undefined, key: string, slot: number): EverythingMultiple | null {
  const ext = cells?.external;
  const seat = meterSplit(ext, key, slot);
  if (!cells || !seat || seat.sources.length === 0) return null;
  const all = seatLists(cells, seat);
  // The lead price leads (#120): the multiple divides it; the list at
  // covey's price table stands beside it for the check.
  const l = all.lead;
  const coveyList = l.covey.metered, outsideList = l.outside.metered;
  const slack = meterSlack([seat], ext!.meter.steps, slackRate(cells.plans ?? [], ext!.meter.point_rate), seatHasList(cells));
  const real = seat.real_usd;
  let why: EverythingMultiple["why"] = null;
  // Every reading under the usage basis (0151): the meter is not the money.
  if (seat.step_count > 0 && (seat.usage_steps ?? 0) === seat.step_count) why = "usage";
  else if (seat.unpriced_points > 0) why = "unpriced";
  else if (seat.points < 3) why = "movement";
  else if (!(coveyList + outsideList >= 0.01) || !(real > 0)) why = "list";
  // The floor's numerator: the server's lead without what began before
  // since, where it says (floor_lead_usd), else the metered lead.
  const floorList = seat.floor_lead_usd ?? coveyList + outsideList;
  const known = seat.unmetered_covey_list_usd != null;
  return {
    coveyList, outsideList, outsidePriced: l.outside.range != null, outsideCalls: l.outside.calls, outsideUnpricedTokens: l.outside.unpricedTokens,
    real, slack, points: seat.points, floorList,
    multiple: why == null ? { kind: "floor", low: floorList / (real + slack) } : null,
    estimate: why == null ? (coveyList + outsideList) / real : null,
    why, unpricedPoints: seat.unpriced_points, usagePoints: seat.usage_points ?? 0,
    unmeteredCovey: known ? l.covey.unmetered : null,
    unmeteredOutside: l.outside.unmetered,
    before: { covey: known ? l.covey.before : null, outside: l.outside.before },
    after: { covey: known ? l.covey.after : null, outside: l.outside.after },
    table: { coveyList: all.covey.metered, outsideList: all.outside.metered },
    outsideRecomputed: l.outside.recomputed,
  };
}

export type FigureInput = {
  cells: CostCells;
  billed?: BilledReport | null;
  /** The bucket width of the cells, in ms. */
  bucketMs: number;
  /** The end of the window (now). */
  windowEnd: Date;
  /** Selected agents; empty: all. */
  agents?: ReadonlySet<string>;
  /** "agents": the per-agent layout, no multiple. */
  layout?: "combined" | "agents";
  /** The outside sessions are included (the page's default): a seat a source
   *  names gets the multiple of everything on its meter. */
  includeOutside?: boolean;
};

export const SUSPECT_BASES = ["legacy", "cumulative"];

/** The figures per credential: one block each, costliest (recomputed) first,
 *  the not-attributable runs last. */
export function credentialFigures(input: FigureInput): CredentialFigures[] {
  const { cells: data, bucketMs, windowEnd } = input;
  const sel = input.agents ?? new Set<string>();
  const inScope = (id: string) => sel.size === 0 || sel.has(id);
  const meter = data.meter ?? [];
  const steps = data.subscription ?? [];
  const plans = data.plans ?? [];
  const labels = new Map<string, CellAgent>(data.agents.map((a) => [a.id, a]));

  type Acc = { key: string | null; slot: number | null; kind: string; cells: CostCell[] };
  const by = new Map<string, Acc>();
  const touch = (key: string | null | undefined, slot: number | null | undefined, kind?: string) => {
    const id = credId(key, slot);
    let a = by.get(id);
    if (!a) {
      a = { key: id ? key! : null, slot: id ? slot! : null, kind: "", cells: [] };
      by.set(id, a);
    }
    if (!a.kind && kind) a.kind = kind;
    return a;
  };
  for (const c of data.cells) touch(c.secret_key, c.secret_slot, c.credential_kind).cells.push(c);
  for (const m of meter) touch(m.secret_key, m.secret_slot, m.credential_kind);
  for (const p of input.billed?.points ?? []) if (p.secret_key != null) touch(p.secret_key, p.secret_slot, "api_key");
  for (const b of input.billed?.window?.totals ?? []) if (b.has_billed) touch(b.secret_key, b.secret_slot, "api_key");
  // A credential charged by hand is money even where covey ran nothing on it.
  for (const c of data.usage_charges ?? []) touch(c.secret_key, c.secret_slot);

  const out: CredentialFigures[] = [];
  for (const [id, a] of by) {
    let reported = 0, recomputed = 0, bookedWithoutLine = 0, all = 0;
    const per = new Map<string, { reported: number; recomputed: number }>();
    for (const c of a.cells) {
      const rec = recomputedOf(c);
      all += rec;
      if (!inScope(c.agent_id)) continue;
      reported += c.engine_usd ?? 0;
      recomputed += rec;
      bookedWithoutLine += c.measured.unattributed ?? 0;
      const p = per.get(c.agent_id) ?? { reported: 0, recomputed: 0 };
      p.reported += c.engine_usd ?? 0;
      p.recomputed += rec;
      per.set(c.agent_id, p);
    }

    const ownPlans = a.key == null ? [] : plans.filter((p) => p.secret_key === a.key && p.secret_slot === a.slot);
    const plan = a.key == null ? null : planAt(plans, a.key, a.slot!, windowEnd) ?? ownPlans.at(-1) ?? null;
    const kind = a.kind || (plan ? "subscription" : "");

    const seatSplit = a.key == null ? null : meterSplit(data.external, a.key, a.slot!);
    // One rule for the headline and the seat (#120 review): a seat with a
    // split reading or unattributed movement is split as the server split it
    // (F3c) — a linked seat between covey's agents, the outside sessions and
    // the unattributed part, a seat no source names between covey's agents
    // and the unattributed part (L_out = 0). The latter is `unlinked`: its
    // meter may still carry use outside covey nothing reports, and says so.
    const split = seatSplit && (seatSplit.split_steps > 0 || (seatSplit.unattributed_steps ?? 0) > 0) ? seatSplit : null;
    const unlinked = seatSplit != null && seatSplit.sources.length === 0;
    // A seat billed by usage over the whole window has no meter split to
    // wait for: its readings are the charges' (0151), not unreported ones.
    const meterOnUsage = seatSplit != null && seatSplit.step_count > 0 && seatSplit.step_count === (seatSplit.usage_steps ?? 0);
    const outsideUnreported = seatSplit != null && split == null && !meterOnUsage;
    let cov: MeterCoverage | null = null, real: RealFigure | null = null, aligned: number | null = null;
    let mult: Multiple | null = null;
    let noMultiple: CredentialFigures["noMultiple"] = null;
    if (a.key != null && kind !== "api_key") {
      cov = pickMeter(meter, a.key, a.slot!);
      if (cov) {
        real = realFromWeeks(meterWeeks(cov, steps, plans, bucketMs, data.cost_bases));
        aligned = cov.measured_usd ?? null;
        // Every week of it the charges' (billed by usage all through): the
        // seat priced nothing here, so it has no real figure of its own —
        // never "< $x" or $0.00.
        if (real.weeks === 0 && real.usageWeeks > 0) real = null;
      }
      if (sel.size > 0 || input.layout === "agents") noMultiple = "agents";
      else if (split && !unlinked) noMultiple = "outside";
      else if (!cov || !real || aligned == null) noMultiple = "meter";
      else if (!real.priced) noMultiple = "unpriced";
      else if (real.points < 3) noMultiple = "movement";
      else mult = multipleOf(aligned, real);
      // covey's list over a movement that may hold unreported outside use:
      // covey's real is at most the whole, so its multiple at least this.
      if (mult && (outsideUnreported || unlinked)) mult = { kind: "floor", low: mult.low };
    }

    // The basis (0151). Without one, nothing above changes. The real figure
    // stays what the server priced, step by step, whatever the basis: under
    // usage it priced none of the steps, so a seat that changed basis within
    // the window keeps the dollars of the steps before (or after) the change.
    const usage = a.key == null ? null : usageFigure(data, a.key, a.slot!, windowEnd);
    const basis = usage?.basis ?? null;
    let usageMultiple: number | null = null, usageList: number | null = null;
    let usageShared: string[] | null = null;
    const agentsView = sel.size > 0 || input.layout === "agents";
    if (usage && kind !== "api_key" && !agentsView && usage.regimes.some((r) => r !== "seat")) {
      mult = null;
      if (usage.changed) {
        // The seat's part and the charges' part are different money over
        // different spans: no one list covers both.
        noMultiple = "basisChanged";
      } else if (usage.regimes[0] === "seat_and_usage") {
        // The meter's multiple would leave the charges out.
        noMultiple = "seatAndUsage";
      } else if (usage.currencies.length === 0) {
        noMultiple = "noCharge";
      } else if (usage.usd == null) {
        noMultiple = "rate";
      } else {
        // Billed by usage all through: covey's list over exactly the spans
        // the counted charges cover, not over the whole window.
        const from = windowStartOf(data)?.getTime() ?? -Infinity, to = windowEnd.getTime();
        const list = listOverSpans(a.cells, countedSpans(usage, data.cost_bases, a.key!, a.slot!, from, to), bucketMs, from, to);
        if (!(usage.usd > 0)) noMultiple = "noCharge";
        else if (!(list >= 0.01)) noMultiple = "spanList";
        else { usageList = list; usageMultiple = list / usage.usd; noMultiple = null; }
        // Operator sessions outside covey run on this credential too: the
        // charges pay for their use as well, so covey's list over them is
        // a lower bound of covey's own multiple.
        if (usageMultiple != null && seatSplit != null && seatSplit.sources.length > 0) usageShared = [...seatSplit.sources];
      }
    }

    const billed = a.key != null && kind === "api_key" ? billedFigure(input.billed, a.key, a.slot!, windowEnd) : null;

    const agents: AgentFigure[] = [...per.entries()].map(([aid, p]): AgentFigure => {
      const share = all > 0 ? p.recomputed / all : null;
      let realShare = share != null && real?.priced ? share * real.value : null;
      let realWhy: AgentFigure["realWhy"] = null;
      if (split) {
        // The server's figure, over the split steps the agent ran in; one
        // that ran only in unsplit steps has none ("–"), never $0.00.
        const sa = split.agents.find((x) => x.agent_id === aid);
        realShare = sa && sa.split_list_usd > 0 ? sa.real_usd : null;
        if (realShare == null) realWhy = "unsplit";
      }
      return {
        id: aid,
        label: labels.get(aid)?.label ?? aid,
        reported: p.reported,
        recomputed: p.recomputed,
        gapPct: p.reported > 0 ? ((p.recomputed - p.reported) / p.reported) * 100 : null,
        share,
        realShare,
        realBound: outsideUnreported ? "upTo" : "estimate",
        realWhy,
        beforeWindow: false,
      };
    }).sort((x, y) => y.recomputed - x.recomputed);
    // An agent whose lines on a split seat fall only in a reading's span
    // that began before the range has a real share and no line here: a row
    // of its own, so the agents add up to covey's part (#120 review).
    if (split) {
      for (const sa of split.agents) {
        if (per.has(sa.agent_id) || !inScope(sa.agent_id) || !(sa.split_list_usd > 0)) continue;
        agents.push({
          id: sa.agent_id, label: labels.get(sa.agent_id)?.label ?? sa.agent_id, reported: 0, recomputed: 0, gapPct: null,
          share: null, realShare: sa.real_usd, realBound: "estimate", realWhy: null, beforeWindow: true,
        });
      }
    }

    const bookingCheck = (data.bookings ?? []).filter((b) => credId(b.secret_key, b.secret_slot) === id && SUSPECT_BASES.includes(b.basis));
    // Everything on the meter: org-wide whatever the agent filter or the
    // layout (the page says so under a filter, #120 review L2).
    let everything = input.includeOutside && a.key != null && kind !== "api_key"
      ? everythingOnMeter(data, a.key, a.slot!) : null;
    // A seat billed by usage within the range (0151): the meter's movement
    // is not, or not all, its money, so no multiple of the meter stands for
    // everything on it. The credential's usage multiple stands instead, by
    // its own rules: covey's list over the counted charges, "at least" where
    // the outside sessions on it share the charges, or why there is none.
    if (everything && usage && usage.regimes.some((r) => r !== "seat")) {
      everything = { ...everything, multiple: null, estimate: null, why: "usage" };
    }
    out.push({
      id, secretKey: a.key, slot: a.slot, kind, plan,
      reported, recomputed,
      check: reported > 0 ? ((recomputed - reported) / reported) * 100 : null,
      bookedWithoutLine, meter: cov, real, aligned, multiple: mult, noMultiple, split, unlinked, outsideUnreported, billed, bookingCheck, agents,
      listAll: all, everything,
      basis, usage, usageMultiple, usageList, usageShared, meterOnUsage,
      monthly: a.key == null ? [] : monthlyMeters(meter, data.usage_limits ?? [], data.usage_charges ?? [], a.key, a.slot!),
    });
  }
  out.sort((x, y) => (x.id === "" ? 1 : 0) - (y.id === "" ? 1 : 0) || y.recomputed - x.recomputed);
  return out;
}

// --- The meter card ------------------------------------------------------------

/** The readings of one credential and source on the weekly window that can be
 *  used: supported, fresh, with a figure; oldest first. */
export function usableReadings(rs: readonly UsageReading[], key: string, slot: number, source: string): UsageReading[] {
  return rs
    .filter((r) => r.secret_key === key && r.secret_slot === slot && r.source === source && r.supported && !r.stale
      && r.week_percent != null && (r.window_kind === "seven_day" || r.window_kind === ""))
    .sort((a, b) => Date.parse(a.fetched_at) - Date.parse(b.fetched_at));
}

/** The meter's whole-percent crossings in each period: "10 % → 11 %". */
export function meterCrossings(cov: MeterCoverage, steps: readonly MeterStep[]): Map<string, { from: number; to: number; reset: boolean }> {
  const own = steps
    .filter((s) => s.secret_key === cov.secret_key && s.secret_slot === cov.secret_slot && s.source === cov.source && s.window_kind === cov.window_kind)
    .sort((a, b) => Date.parse(a.period) - Date.parse(b.period));
  const out = new Map<string, { from: number; to: number; reset: boolean }>();
  let level = cov.baseline_percent;
  for (const s of own) {
    const reset = s.resets > 0;
    const from = reset ? 0 : level;
    const to = from + s.points;
    level = to;
    if (s.points > 0 || reset) out.set(s.period, { from, to, reset });
  }
  return out;
}

// --- The per-model table's totals ----------------------------------------------

/** The sum of one money source over the credentials that have it: the real
 *  cost and the bill are per credential, so the per-model table shows them in
 *  rows of their own, each beside the list price of the same credentials over
 *  the same span, never beside the models' totals. `of` counts the credentials
 *  the source applies to, `with` those that have a figure; a sum over fewer is
 *  not the total, and the table shows no number then. */
export type SourceTotal = {
  usd: number; low: number; high: number; below: boolean; with: number; of: number; pending: boolean;
  /** The error of the last bill sync where one failed, so a bill that stops
   *  short says why; null for the meter and for a sync that went through. */
  syncError: string | null;
  /** Claude Code's figure and covey's recomputed list over the span of this
   *  source, summed over the credentials with a figure; null where one of
   *  them has none, since a part is not the whole. The meter's real cost has
   *  no reported counterpart: only the bill carries one. */
  reported: number | null; recomputed: number | null;
  /** The span each credential's figure covers: the meter's baseline to its
   *  last reading, or the bill's window. */
  spans: { id: string; from: Date; to: Date }[];
};

/** The figure of a per-model source over the span of a per-credential one:
 *  Claude Code's own and covey's recomputed, null where not every credential
 *  has one. */
export function scopeFigure(total: SourceTotal, source: Source): number | null {
  return source === "reported" ? total.reported : source === "recomputed" ? total.recomputed : null;
}

/** Whether a credential is a subscription seat: one whose tile offers a real
 *  cost, whose card shows its meter and whose figure the seats' row sums. A
 *  credential of a kind a newer daemon reports is none of these until covey
 *  knows it; one of no reported kind counts where it has a meter. */
export function isSeat(f: Pick<CredentialFigures, "kind" | "meter">): boolean {
  return f.kind === "subscription" || (f.kind === "" && f.meter != null);
}

/** Real cost and bill over the whole organisation's credentials, as each tile
 *  shows them: a source is offered (non-null) only when some credential has a
 *  meter or a bill to read. */
export function perCredentialTotals(figures: readonly CredentialFigures[]): {
  real: SourceTotal | null; billed: SourceTotal | null;
  /** The counted usage charges of all credentials (0151), per currency in
   *  its own: in neither figure above and never added to them. Null when
   *  none counts in the window. */
  usage: UsageCurrency[] | null;
} {
  // The credentials whose tile offers a real cost: a credential of another
  // kind has none, so it is not one of the credentials the figure is "of".
  // A seat billed by usage (0151) is one of them: its real figure covers the
  // seat's part only, or is missing where the charges were all of its money,
  // and then the total says that some are without one.
  const subs = figures.filter((f) => f.secretKey != null && isSeat(f));
  const meters = subs.filter((f) => f.real != null || f.meter != null);
  let real: SourceTotal | null = null;
  if (meters.length > 0) {
    const priced = meters.filter((f) => f.real?.priced);
    real = {
      usd: priced.reduce((a, f) => a + f.real!.value, 0),
      low: priced.reduce((a, f) => a + f.real!.low, 0),
      high: priced.reduce((a, f) => a + f.real!.high, 0),
      below: priced.length > 0 && priced.every((f) => f.real!.below),
      with: priced.length, of: subs.length, pending: false, syncError: null,
      reported: null,
      // covey's list beside a meter that sessions outside covey move too
      // would read as covey's multiple over the whole movement (#120): none.
      // Nor beside a seat part of a meter the charges paid the rest of.
      recomputed: priced.length > 0 && priced.every((f) => f.aligned != null && f.split == null && !f.outsideUnreported
        && !(f.real!.usagePoints > 0) && !(f.real!.usageWeeks > 0))
        ? priced.reduce((a, f) => a + f.aligned!, 0) : null,
      spans: meters.filter((f) => f.meter != null).map((f) => ({ id: f.id, from: new Date(f.meter!.baseline_at), to: new Date(f.meter!.last_at) })),
    };
  }
  const counted = usageCurrencies(figures.flatMap((f) => f.usage?.charges ?? []));
  const keys = figures.filter((f) => f.billed != null);
  const measured = keys.filter((f) => f.billed!.state === "measured");
  let billed: SourceTotal | null = null;
  if (measured.length > 0) {
    billed = {
      usd: measured.reduce((a, f) => a + f.billed!.usd, 0), low: 0, high: 0, below: false,
      with: measured.length, of: keys.length, pending: measured.some((f) => f.billed!.status.state === "pending"),
      syncError: measured.find((f) => f.billed!.error)?.billed!.error || null,
      reported: measured.reduce((a, f) => a + f.billed!.reported, 0),
      recomputed: measured.reduce((a, f) => a + f.billed!.recomputed, 0),
      spans: measured.filter((f) => f.billed!.window != null).map((f) => ({ id: f.id, from: f.billed!.window!.start, to: f.billed!.window!.end })),
    };
  }
  return { real, billed, usage: counted.length > 0 ? counted : null };
}

// --- The dent: covey and the sessions outside it on one meter (#120) -------------

/** A seat's split totals, once a source names a meter (with or without a
 *  split step of its own). */
export function meterSplit(ext: ExternalUsage | null | undefined, key: string, slot: number): ExternalMeterTotal | null {
  return ext?.meter.credentials.find((c) => c.secret_key === key && c.secret_slot === slot) ?? null;
}

/** One period of a seat's real cost, split: covey's agents, the outside
 *  sessions, the unattributed movement (F3c: neither explains it, booked to
 *  nobody), and the movement of readings that were not split — the outside
 *  sessions had not reported them, or (unpricedSteps of them) their outside
 *  calls all had no price. */
export type SplitPeriod = { covey: number; outside: number; unattributed: number; unsplit: number; unsplitSteps: number; unpricedSteps: number };

export const EMPTY_SPLIT_PERIOD: SplitPeriod = { covey: 0, outside: 0, unattributed: 0, unsplit: 0, unsplitSteps: 0, unpricedSteps: 0 };

/** The split per chart period (keyed by the period's start in ms), summed
 *  from the server's steps, each at its reading — where the real bar
 *  (realPer) puts the same movement, at any bucket down to 10 minutes. */
export function splitPerPeriod(ext: ExternalUsage | null | undefined, key: string, slot: number, bucket: string): Map<number, SplitPeriod> {
  const out = new Map<number, SplitPeriod>();
  for (const st of ext?.meter.steps ?? []) {
    if (st.secret_key !== key || st.secret_slot !== slot || st.real_usd == null) continue;
    const k = bucketStart(Date.parse(st.at), bucket);
    const p = out.get(k) ?? { ...EMPTY_SPLIT_PERIOD };
    // A split step's chunk part, or the whole of one in a chunk with no list.
    p.unattributed += st.unattributed_real_usd ?? 0;
    if (st.split) {
      p.covey += st.covey_real_usd ?? 0;
      p.outside += st.external_real_usd ?? 0;
    } else if (st.unsplit_real_usd > 0) {
      p.unsplit += st.unsplit_real_usd;
      p.unsplitSteps += 1;
      if (st.reason === "outside_unpriced") p.unpricedSteps += 1;
    }
    out.set(k, p);
  }
  return out;
}

/** A multiple that is only known from below. */
export type FloorMultiple = Extract<Multiple, { kind: "floor" }>;

/** One consumer of the meters: its real cost, its share of the dent, its
 *  points (the steps' attributed points weighted by its share), its list
 *  price over the split steps, and list $ per real $ — a lower bound. */
export type DentPart = { real: number; share: number | null; points: number; list: number; multiple: FloorMultiple | null };

/** One seat in the dent. real is null when none of its movement has a plan
 *  price; split: it has a split step. pointsPerList: the seat's k (F3c),
 *  null when no stretch of adjacent complete chunks of split readings moved
 *  10 points; olderPlan: the plan price per point k is at when that is not
 *  the seat's latest one, else null. */
/** The real cost of some sources' parts; null where there are none. */
const partReal = (ps: readonly { real_usd: number }[]): number | null => (ps.length > 0 ? ps.reduce((a, p) => a + p.real_usd, 0) : null);

/** A seat's outside real cost per party, from its source_parts; null without them, or where they do not add up to the
 *  seat's own outside figure (a card or line that left money out would be wrong, F2). */
function seatOutsideParts(c: Pick<ExternalMeterTotal, "source_parts" | "external_real_usd">): DentSeat["outsideParts"] {
  const ps = c.source_parts ?? [];
  if (ps.length === 0 || Math.abs(ps.reduce((a, p) => a + p.real_usd, 0) - c.external_real_usd) >= 0.005) return null;
  return { operator: partReal(ps.filter((p) => isOperatorSource(p.source))), projects: partReal(ps.filter((p) => !isOperatorSource(p.source))) };
}

export type DentSeat = {
  key: string; slot: number; sources: string[];
  real: number | null; points: number; covey: number; outside: number; unattributed: number; unsplit: number;
  /** The seat's outside part (real cost) per party, from its source_parts: the covey operator and the other projects (#163);
   *  null where the server sent none (the seat has no split steps) or where the parts do not add up to `outside` (a share
   *  the sources do not carry: the seat's one outside figure then stands, #166 review, F2); a party is null where no source
   *  of it is on the seat. */
  outsideParts: { operator: number | null; projects: number | null } | null;
  unpricedPoints: number; split: boolean; pointsPerList: number | null; olderPlan: number | null;
  /** Points read while the seat was billed by usage (0151): not in the dent,
   *  not unpriced; usageSteps: those readings, idle ones included. */
  usagePoints: number;
  usageSteps: number;
  /** The seat's readings with a span in the range. */
  steps: number;
};

/** The whole dent in the subscription meters over the window, and its split
 *  (#120, F3c: per chunk of readings — at least 5 points, but for the last
 *  one and one closed early — by the chunk's list-price shares). The parts add up to `real`: covey's agents and the
 *  outside sessions over the readings the outside sessions reported
 *  (estimates, ≈), `unattributed` for the movement neither explains (each
 *  chunk's points beyond the rate k — a lower bound — and chunks that moved
 *  with no call at all — an estimate; booked to nobody), `unsplit` over the
 *  readings they did not report — never booked to covey whole. The three
 *  multiples (list price over real cost, from 3 points of attributed
 *  movement) are lower bounds: list / (real + slack), the low end of the
 *  range the whole percents and the meter's lag leave (the server's: 2
 *  points × ½·TV of each part's share per chunk, at each reading's own point
 *  price). Points without a plan
 *  price are not in it, and counted. */
export type SubscriptionDent = {
  /** null: no seat has a priced reading. */
  real: number | null;
  /** The dent in points; null when the seats' points are priced differently. */
  points: number | null;
  covey: DentPart;
  outside: DentPart;
  together: DentPart;
  /** unpricedSteps / unpricedReal: the part of steps / real not split
   *  because the outside calls of the span all had no price (never covey's);
   *  the rest the outside sessions had not reported. */
  unsplit: { real: number; share: number | null; steps: number; unpricedSteps: number; unpricedReal: number };
  /** The movement neither covey's agents nor the reported sessions explain,
   *  booked to nobody: `beyond` the rate (a lower bound) and `noCall`, the
   *  chunks that moved with no call at all (an estimate). */
  unattributed: { real: number; share: number | null; points: number; steps: number; beyond: number; noCall: number };
  /** Chunks whose use is not all in their list (outside calls without a
   *  price, covey bookings without a line): no rate, no unattributed part. */
  incompleteChunks: number;
  unpricedPoints: number;
  /** Points read while a seat was billed by usage (0151): their money is the
   *  usage charges, so they are not in the dent, and not unpriced either. */
  usagePoints: number;
  /** Outside calls with no price at all: left out of the split. */
  unpricedCalls: number;
  /** The outside calls' lead price in no reading's span: before the meters'
   *  first reading in the range (no meter data for that time, never a real
   *  share) and after their last one (pending). */
  unmeteredBefore: number;
  unmeteredAfter: number;
  /** Why there are no multiples: "movement" (< 3 points in the split readings), "split" (none was split),
   *  "usage" (every reading in the range was under the usage basis, 0151: there is no dent to split). */
  noMultiple: "movement" | "split" | "usage" | null;
  /** The outside sources that name a meter. */
  sources: string[];
  seats: DentSeat[];
};

/** The headline (#120, the human's core): all the work on the subscription
 *  meters at token prices — covey's agents and the sessions outside covey on
 *  those meters, recomputed from tokens (the primary figure) and as Claude
 *  Code reported it — against the ONE real cost, the meters' movement × the
 *  price per point, and one multiple. The per-party real shares are an
 *  estimate apart (SubscriptionDent).
 *
 *  list: the range's list price on the subscription meters (the tiles'
 *  figures, on these seats; per-use sessions are on no meter and apart);
 *  list = metered + unmetered + unpriced (seatLists): metered, the list
 *  within the meters' priced readings in the range — what the multiple
 *  divides; unmetered, after the last reading, no real cost yet; unpriced,
 *  in readings with no plan price. The multiple ≈ metered / real, at least
 *  metered / (real + slack), the slack meterSlack's: the whole range's
 *  truncation and lag, not per chunk. Use nobody reported only raises the
 *  multiple of the known work.
 *
 *  A seat's time on the usage basis (0151) is out of the real cost and of
 *  metered: its readings' list is the part `usage` (list = metered +
 *  unmetered + unpriced + usage), its points usagePoints, and the headline
 *  names the seats so left out. Their money is the usage charges, which
 *  stand beside it per currency, each in its own (usageCharges) — never
 *  added to the real cost, across currencies, or converted. */
export type Headline = {
  /** At the lead price (#120: reported where a call carries it, else list):
   *  list = coveyList + outsideList, and its parts below. */
  list: number; coveyList: number;
  /** null: the sessions on the meters had calls, none with a price of either kind — never $0. */
  outsideList: number | null;
  /** The outside calls priced at the list for want of a reported cost (marked). */
  outsideRecomputed: number;
  /** The same range at covey's price table (Recomputed), for the check. */
  table: number;
  reported: number; coveyReported: number; outsideReported: number | null;
  outsideCalls: number; outsideReportedCalls: number; outsideUnpricedTokens: number;
  /** The listed sources on these seats, with their lead price (list: null where none of its calls had a price;
   *  recomputed: calls priced at the list for want of a reported cost); before: listed only for a real part, its
   *  calls lie before the range. */
  sources: { source: string; list: number | null; reported: number | null; calls: number; before: boolean; recomputed: number; unpricedTokens: number }[];
  /** The sessions outside covey billed per use (no meter): not in list; null without such a source. */
  perUse: OutsideKpis | null;
  metered: number;
  /** unmetered = before + after: before the meters' first reading in the
   *  range (no meter data for that time, never a real cost) and after their
   *  last one (pending until they are read again). */
  unmetered: number;
  before: number;
  after: number;
  unpriced: number;
  /** null: no reading has a plan price. */
  real: number | null;
  /** null: seats at different point prices. */
  points: number | null;
  slack: number;
  /** The floor's numerator: the server's floor_lead_usd where it sends one (the metered lead without what began
   *  before since), else metered. */
  floorList: number;
  multiple: number | null;
  floor: number | null;
  /** "usage": no reading has a plan price because every one was taken while billed by usage (0151). */
  why: "movement" | "unpriced" | "list" | "usage" | null;
  unpricedPoints: number;
  /** The list of the readings taken while billed by usage (0151): in list, not in metered. */
  usage: number;
  /** Their points: not in real. */
  usagePoints: number;
  /** The seats with such readings in the range ("key #slot"): left out of real for that time. */
  usageSeats: string[];
  /** The counted usage charges of these seats, per currency in its own; null when none counts in the range. */
  usageCharges: UsageCurrency[] | null;
};

export function headline(cells: CostCells | null | undefined): Headline | null {
  const ext = cells?.external;
  const m = ext?.meter;
  if (!cells || !m || m.credentials.length === 0) return null;
  const seats = new Set(m.credentials.map((c) => credId(c.secret_key, c.secret_slot)));
  let coveyReported = 0;
  for (const c of cells.cells) {
    if (!seats.has(credId(c.secret_key, c.secret_slot))) continue;
    coveyReported += c.engine_usd ?? 0;
  }
  const realParts = sourceRealParts(ext);
  const listed = ext!.sources.filter((s) => listedSource(s, realParts)).sort((a, b) => a.source.localeCompare(b.source));
  const own = listed.filter((s) => s.meter_credential && seats.has(credId(s.meter_credential.secret_key, s.meter_credential.secret_slot)));
  const names = new Set(own.map((s) => s.source));
  let outsideReported: number | null = null, outsideCalls = 0, outsideReportedCalls = 0, outsideUnpricedTokens = 0;
  for (const s of own) {
    if (s.reported_usd != null) outsideReported = (outsideReported ?? 0) + s.reported_usd;
    outsideCalls += s.calls;
    outsideUnpricedTokens += s.unpriced_tokens;
  }
  for (const c of ext!.cells) if (names.has(c.source)) outsideReportedCalls += c.reported_calls;
  // The lead figure in its parts, per seat (seatLists): range = metered +
  // before + after + unpriced + usage, in each party and so in the whole.
  // covey's price table over the same range stands beside it as a check.
  let coveyList = 0, outsideList: number | null = null, metered = 0, before = 0, after = 0, unpriced = 0, usage = 0;
  let table = 0, outsideRecomputed = 0, outsideLeadUnpricedTokens = 0;
  for (const c of m.credentials) {
    const all = seatLists(cells, c);
    const l = all.lead;
    coveyList += l.covey.range;
    if (l.outside.range != null) outsideList = (outsideList ?? 0) + l.outside.range;
    metered += l.covey.metered + l.outside.metered;
    before += l.covey.before + l.outside.before;
    after += l.covey.after + l.outside.after;
    unpriced += l.covey.unpriced + l.outside.unpriced;
    usage += l.covey.usage + l.outside.usage;
    outsideRecomputed += l.outside.recomputed;
    outsideLeadUnpricedTokens += l.outside.unpricedTokens;
    table += all.covey.range + (all.outside.range ?? 0);
  }
  // The seats left out of the real cost while billed by usage, and the
  // charges that are that time's money: per currency, as the server counted
  // them, never summed across currencies.
  const usageSeats = m.credentials.filter((c) => (c.usage_steps ?? 0) > 0).map((c) => `${c.secret_key} #${c.secret_slot}`);
  const counted = usageCurrencies((cells.usage_charges ?? []).filter((c) => seats.has(credId(c.secret_key, c.secret_slot))));
  if (outsideList == null && outsideCalls === 0) outsideList = 0;
  const pu = listed.filter(perUseSource);
  const perUse = pu.length === 0 ? null : outsideKpis(ext, new Set(pu.map((s) => s.source)));
  // Each source on the seats at its lead price.
  const byLead = new Map(own.map((s) => [s.source, sourceLead(s, ext!.cells)]));
  const priced = m.credentials.some(meterPriced);
  const real = priced ? m.real_usd : null;
  const slack = meterSlack(m.credentials, m.steps, slackRate(cells.plans ?? [], m.point_rate), seatHasList(cells));
  const floorList = m.floor_lead_usd ?? metered;
  let why: Headline["why"] = null;
  if (real == null) why = m.unpriced_points === 0 && (m.usage_steps ?? 0) > 0 ? "usage" : "unpriced";
  else if (m.unpriced_points > 0) why = "unpriced";
  else if (m.points < 3 || m.point_rate_mixed && real / Math.max(m.point_rate, 1e-12) < 3) why = "movement";
  else if (!(metered >= 0.01) || !(real > 0)) why = "list";
  return {
    list: coveyList + (outsideList ?? 0), coveyList, outsideList, outsideRecomputed, table,
    reported: coveyReported + (outsideReported ?? 0), coveyReported, outsideReported, outsideCalls, outsideReportedCalls,
    outsideUnpricedTokens: outsideLeadUnpricedTokens,
    sources: own.map((s) => ({ source: s.source, list: byLead.get(s.source)?.lead ?? null, reported: s.reported_usd, calls: s.calls,
      before: beforeRange(s, realParts), recomputed: byLead.get(s.source)?.recomputed ?? 0,
      unpricedTokens: byLead.get(s.source)?.unpricedTokens ?? 0 })),
    perUse, metered, unmetered: before + after, before, after, unpriced, real, points: m.point_rate_mixed ? null : m.points, slack,
    multiple: why == null ? metered / real! : null,
    floorList, floor: why == null ? floorList / (real! + slack) : null,
    why, unpricedPoints: m.unpriced_points,
    usage, usagePoints: m.usage_points ?? 0, usageSeats, usageCharges: counted.length > 0 ? counted : null,
  };
}

/** A seat of the meter split has a reading in the dent: one neither at no
 *  plan price nor under the usage basis (0151), whose points are the usage
 *  charges'. */
export function meterPriced(c: Pick<ExternalMeterTotal, "step_count" | "unpriced_steps" | "usage_steps">): boolean {
  return c.step_count > c.unpriced_steps + (c.usage_steps ?? 0);
}

export function subscriptionDent(ext: ExternalUsage | null | undefined): SubscriptionDent | null {
  const m = ext?.meter;
  if (!m || m.credentials.length === 0) return null;
  const priced = m.credentials.some(meterPriced);
  const real = m.real_usd;
  const splitReal = m.covey_real_usd + m.external_real_usd;
  const share = (v: number) => (real > 0 ? v / real : null);
  let noMultiple: SubscriptionDent["noMultiple"] = null;
  // Every reading with a span under the usage basis: no dent at all, the
  // charges are the money (0151).
  const onUsage = !priced && (m.usage_steps ?? 0) > 0 && m.unpriced_steps === 0;
  if (onUsage) noMultiple = "usage";
  else if (!(splitReal > 0)) noMultiple = "split";
  // 3 points of attributed split movement; at several plan prices, the
  // dollars at the dearest point.
  else if (m.point_rate > 0 && splitReal / m.point_rate < 3 - 1e-9) noMultiple = "movement";
  // The meter is shared with whatever does not report (covey can never prove
  // every machine does): a part's real cost is at most r + slack, so its
  // multiple is at least list / (r + slack) — the low end of its range.
  const part = (r: number, list: number, points: number, slack: number): DentPart => {
    let mult: FloorMultiple | null = null;
    if (noMultiple == null && r > 0 && list >= 0.01) mult = { kind: "floor", low: list / (r + slack) };
    return { real: r, share: share(r), points, list, multiple: mult };
  };
  // Named only with calls in the range: a project folder with no usage
  // sends claim-only requests (0 calls), and the page never lists those.
  // A source with calls only before the range is named for its real part.
  const realParts = sourceRealParts(ext);
  const active = new Set((ext?.sources ?? []).filter((s) => listedSource(s, realParts)).map((s) => s.source));
  const sources = [...new Set(m.credentials.flatMap((c) => c.sources))].filter((s) => active.has(s)).sort();
  const coveyLead = m.split_covey_lead_usd ?? m.split_covey_list_usd, outsideLead = m.split_external_lead_usd ?? m.split_external_list_usd;
  return {
    real: priced ? real : null,
    points: m.point_rate_mixed ? null : m.points,
    // The parts' lists at the lead price (#120), an older server's at the list.
    covey: part(m.covey_real_usd, coveyLead, m.split_covey_points, m.split_covey_slack_usd),
    outside: part(m.external_real_usd, outsideLead, m.split_external_points, m.split_external_slack_usd),
    together: part(splitReal, coveyLead + outsideLead, m.split_covey_points + m.split_external_points, m.split_slack_usd),
    unsplit: { real: m.unsplit_real_usd, share: share(m.unsplit_real_usd), steps: m.unsplit_steps,
      unpricedSteps: m.outside_unpriced_steps, unpricedReal: m.outside_unpriced_real_usd },
    unattributed: { real: m.unattributed_real_usd ?? 0, share: share(m.unattributed_real_usd ?? 0),
      points: m.unattributed_points ?? 0, steps: m.unattributed_steps ?? 0,
      beyond: (m.unattributed_real_usd ?? 0) - (m.unattributed_no_call_real_usd ?? 0), noCall: m.unattributed_no_call_real_usd ?? 0 },
    incompleteChunks: m.incomplete_chunks ?? 0,
    unpricedPoints: m.unpriced_points,
    usagePoints: m.usage_points ?? 0,
    unpricedCalls: m.unpriced_calls,
    unmeteredBefore: m.unmetered_before_lead_usd ?? 0,
    unmeteredAfter: (m.unmetered_lead_usd ?? m.unmetered_list_usd) - (m.unmetered_before_lead_usd ?? 0),
    noMultiple,
    sources,
    seats: m.credentials.map((c) => ({
      key: c.secret_key, slot: c.secret_slot, sources: c.sources,
      real: meterPriced(c) ? c.real_usd : null, points: c.points,
      covey: c.covey_real_usd, outside: c.external_real_usd, unattributed: c.unattributed_real_usd ?? 0, unsplit: c.unsplit_real_usd,
      outsideParts: seatOutsideParts(c),
      unpricedPoints: c.unpriced_points, usagePoints: c.usage_points ?? 0, usageSteps: c.usage_steps ?? 0, split: c.split_steps > 0,
      pointsPerList: c.points_per_list_usd ?? null, olderPlan: c.points_per_list_older_plan_usd ?? null, steps: c.step_count,
    })),
  };
}

// --- The pay-per-token headline (0152) -------------------------------------------
//
// An organisation whose real money is per token — an API key's bill, or the
// usage charges of a credential billed by usage all through the range (by
// hand and from the monthly meter) — has no subscription meter to divide by.
// Its headline sets the lead figure (as reported by Claude Code: covey's
// agents and the outside sessions linked to the credential) over the spans
// that money covers against that money: in USD where all of it is, and then
// one multiple; per currency otherwise, with no multiple and why. A charge in
// another currency is in USD only at the rate a person entered with its date
// (the server's counted_usd): that is intended, and the rate stands beside
// the multiple; covey never converts at a rate of its own. Apart from the
// subscription headline and never added to a weekly meter's real cost; a
// credential the subscription headline covers (external.meter.credentials,
// whose list and usage charges it already shows) is not in it at all.

/** One credential whose money is per token. */
export type PayPerTokenCredential = {
  id: string;
  name: string;
  /** bill: Anthropic's bill (USD), over its window; charges: the counted usage
   *  charges, over the spans they cover. */
  money: "bill" | "charges";
  /** The lead figure over those spans: covey's agents, and the outside
   *  sessions linked to the credential (their calls' lead price). */
  coveyList: number;
  outsideList: number;
  billed: number | null;
  currencies: UsageCurrency[];
};

export type PayPerTokenHeadline = {
  credentials: PayPerTokenCredential[];
  list: number;
  coveyList: number;
  outsideList: number;
  /** The API keys' bills, USD; null without a measured bill among them. */
  billed: number | null;
  billedPending: boolean;
  /** The counted usage charges, per currency in its own, never added across currencies. */
  charges: UsageCurrency[];
  /** All the real money in USD: only where every part has a USD figure. */
  realUsd: number | null;
  /** list $ per real $; null with a reason. */
  multiple: number | null;
  /** "rate": a currency without a USD figure (no rate with its date); "list":
   *  no work at token prices over the spans; "money": no real money. */
  why: "rate" | "list" | "money" | null;
  /** The currencies with no USD figure. */
  noRate: string[];
  /** The rates the counted non-USD charges are in USD at: each one a person
   *  entered with its date, never one covey looked up. Shown beside the
   *  multiple. */
  rates: { currency: string; rate: number; date: string }[];
};

/** Counted charges of several credentials, per currency: summed within a
 *  currency only, a USD figure only where every part has one. */
export function mergeCurrencies(lists: readonly (readonly UsageCurrency[])[]): UsageCurrency[] {
  const by = new Map<string, UsageCurrency>();
  for (const l of lists) for (const c of l) {
    const u: UsageCurrency = by.get(c.currency) ?? { currency: c.currency, counted: 0, usd: 0, charges: 0 };
    u.counted += c.counted;
    u.usd = u.usd == null || c.usd == null ? null : u.usd + c.usd;
    u.charges += c.charges;
    if (c.meter) u.meter = (u.meter ?? 0) + c.meter;
    by.set(c.currency, u);
  }
  return [...by.values()].sort((a, b) => a.currency.localeCompare(b.currency));
}

/** Spans as one set: overlapping ones merged, so no time counts twice. */
function unionSpans(spans: readonly [number, number][]): [number, number][] {
  const out: [number, number][] = [];
  for (const [a, b] of [...spans].sort((x, y) => x[0] - y[0])) {
    const last = out.at(-1);
    if (last && a <= last[1]) last[1] = Math.max(last[1], b);
    else out.push([a, b]);
  }
  return out;
}

/** The pay-per-token headline over the organisation's credentials (the
 *  figures without an agent filter); null when none is paid per token. An
 *  API key with a measured bill counts by its bill (a charge entered beside
 *  it would be the same money); any other credential by its counted usage
 *  charges where it was billed by usage all through the range. */
export function payPerTokenHeadline(cells: CostCells | null | undefined, figures: readonly CredentialFigures[], bucketMs: number,
  windowEnd: Date): PayPerTokenHeadline | null {
  if (!cells) return null;
  const ext = cells.external;
  const realParts = sourceRealParts(ext);
  const from = windowStartOf(cells)?.getTime() ?? -Infinity, to = windowEnd.getTime();
  const creds: PayPerTokenCredential[] = [];
  const rates = new Map<string, { currency: string; rate: number; date: string }>();
  // The seats the subscription headline (headline()) shows, their list and
  // their usage charges with them: never a second time here.
  const covered = new Set((ext?.meter?.credentials ?? []).map((c) => credId(c.secret_key, c.secret_slot)));
  let pending = false;
  for (const f of figures) {
    if (f.secretKey == null || covered.has(f.id)) continue;
    const key = f.secretKey, slot = f.slot!;
    const linked = new Set((ext?.sources ?? []).filter((s) => listedSource(s, realParts)
      && s.meter_credential?.secret_key === key && s.meter_credential.secret_slot === slot).map((s) => s.source));
    const outCells = (ext?.cells ?? []).filter((c) => linked.has(c.source));
    const outside = (spans: [number, number][]) => listOverSpans(outCells, spans, bucketMs, from, to, (c) => cellLead(c).lead ?? 0);
    const name = `${key} #${slot}`;
    if (f.kind === "api_key" && f.billed?.state === "measured") {
      const w = f.billed.window;
      creds.push({ id: f.id, name, money: "bill", coveyList: f.billed.reported,
        outsideList: w ? outside([[w.start.getTime(), w.comparedUntil.getTime()]]) : 0, billed: f.billed.usd, currencies: [] });
      if (f.billed.status.state === "pending") pending = true;
      continue;
    }
    const u = f.usage;
    if (!u || u.currencies.length === 0 || !u.regimes.every((r) => r === "usage")) continue;
    const spans = unionSpans(countedSpans(u, cells.cost_bases, key, slot, from, to));
    const own = cells.cells.filter((c) => credId(c.secret_key, c.secret_slot) === f.id);
    creds.push({ id: f.id, name, money: "charges", coveyList: listOverSpans(own, spans, bucketMs, from, to, leadOf),
      outsideList: outside(spans), billed: null, currencies: u.currencies });
    for (const c of u.charges) {
      const cur = c.currency.toUpperCase();
      if (isExportCharge(c) || !(c.counted_amount > 0) || cur === "USD" || c.fx_to_usd == null || !c.fx_date) continue;
      rates.set(`${cur}/${c.fx_to_usd}/${c.fx_date}`, { currency: cur, rate: c.fx_to_usd, date: c.fx_date });
    }
  }
  if (creds.length === 0) return null;
  const bills = creds.filter((c) => c.billed != null);
  const billed = bills.length > 0 ? bills.reduce((a, c) => a + c.billed!, 0) : null;
  const charges = mergeCurrencies(creds.map((c) => c.currencies));
  const noRate = charges.filter((c) => c.usd == null).map((c) => c.currency);
  const realUsd = noRate.length > 0 ? null : (billed ?? 0) + charges.reduce((a, c) => a + c.usd!, 0);
  const coveyList = creds.reduce((a, c) => a + c.coveyList, 0), outsideList = creds.reduce((a, c) => a + c.outsideList, 0);
  const list = coveyList + outsideList;
  const why: PayPerTokenHeadline["why"] = noRate.length > 0 ? "rate" : !(realUsd! > 0) ? "money" : !(list >= 0.01) ? "list" : null;
  return {
    credentials: creds, list, coveyList, outsideList, billed, billedPending: pending, charges, realUsd,
    multiple: why == null ? list / realUsd! : null, why, noRate,
    rates: [...rates.values()].sort((a, b) => a.currency.localeCompare(b.currency) || a.date.localeCompare(b.date)),
  };
}
