import type {
  BilledReport,
  BookingBasis,
  CellAgent,
  CostCell,
  CostCells,
  CredentialPlan,
  ExternalMeterTotal,
  ExternalUsage,
  MeterCoverage,
  MeterStep,
  UsageReading,
} from "../api";
import type { Source } from "./costSources";

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
};

export type RealFigure = RealCost & {
  /** Points read while no plan was valid: not in the value. */
  unpricedPoints: number;
  /** The weeks the window spans: each leaves ±1 point. */
  weeks: number;
  /** Every point and every week's ±1 point has a plan price. Otherwise the
   *  value is only part of the movement, and no real figure (and no
   *  multiple) may be shown — "no plan price for N of M points" instead. */
  priced: boolean;
};

/** Real cost over the weeks of a window: every point as the server priced
 *  it, every week ±1 point (whole percents) at the dearest rate of that
 *  week, so the range is never narrower than the truth. */
export function realFromWeeks(weeks: readonly MeterWeek[]): RealFigure {
  let value = 0, low = 0, high = 0, points = 0, unpriced = 0, priced = true;
  for (const w of weeks) {
    const top = w.rate ?? 0;
    if (w.rate == null) priced = false;
    value += w.usd;
    points += w.points;
    unpriced += w.unpricedPoints;
    // A week without movement still leaves "less than one point".
    low += w.points >= 1 ? Math.max(w.usd - top, 0) : 0;
    high += w.usd + top;
  }
  if (unpriced > 0) priced = false;
  return { value, low, high, points, below: points === 0, unpricedPoints: unpriced, weeks: weeks.length, priced };
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

/** A step's points that have no plan price: what the server says, and all of
 *  them when it priced none (usd null). */
export function stepUnpriced(st: Pick<MeterStep, "points" | "usd" | "unpriced_points">): number {
  return st.usd == null ? st.points : st.unpriced_points;
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
export function meterWeeks(cov: MeterCoverage, steps: readonly MeterStep[], plans: readonly CredentialPlan[], bucketMs: number): MeterWeek[] {
  const segs = cov.segments && cov.segments.length > 0
    ? cov.segments
    : [{ start_at: cov.baseline_at, end_at: cov.last_at, from_percent: cov.baseline_percent, to_percent: cov.last_percent, reset: false, points: cov.points }];
  const own = steps.filter((s) => s.secret_key === cov.secret_key && s.secret_slot === cov.secret_slot
    && s.source === cov.source && s.window_kind === cov.window_kind);
  const weeks: MeterWeek[] = segs.map((s) => ({ points: s.points, usd: 0, unpricedPoints: 0, rate: null }));
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
    w.usd += st.usd ?? 0;
    if (st.points - unpriced > 0 && st.usd != null) w.rate = Math.max(w.rate ?? 0, st.usd / (st.points - unpriced));
  }
  // Movement that no step carries is movement without a price.
  const missing = segs.reduce((a, s) => a + s.points, 0) - stepped;
  if (missing > 0) weeks[weeks.length - 1].unpricedPoints += missing;
  segs.forEach((s, i) => {
    const w = weeks[i];
    // A week without a priced step still leaves its ±1 point, at the plan
    // valid at its last reading.
    if (w.rate == null) {
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
 *  - measured:     a figure — $0.00 only when configured, mapped and complete.
 *  Only "measured" carries a dollar figure; every other state is "no figure",
 *  never "$0.00". */
export type BilledState = "unavailable" | "unconfigured" | "unmapped" | "failed" | "not_landed" | "measured";

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
 *  before the window. */
export function billedFetchBucket(days: number, bucket: string): "hour" | "day" {
  return bucket === "hour" || days <= 2 ? "hour" : "day";
}

/** The bill of one credential per chart period (keyed by the period's start
 *  in ms): only the points inside the window the billed tile covers, summed
 *  into the chart's buckets — so the bars cover the same window and buckets
 *  as the rest of the chart, and their total is the tile's figure. */
export function billedPerPeriod(report: BilledReport | null | undefined, key: string, slot: number, bucket: string): Map<number, number> {
  const out = new Map<number, number>();
  const w = report?.window;
  if (!w) return out;
  const start = Date.parse(w.start), end = Date.parse(w.end);
  for (const p of report?.points ?? []) {
    if (p.secret_key !== key || p.secret_slot !== slot) continue;
    const at = Date.parse(p.bucket);
    if (at < start || at >= end) continue;
    const k = bucketStart(at, bucket);
    out.set(k, (out.get(k) ?? 0) + (Number(p.billed_usd) || 0));
  }
  return out;
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
};

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
  noMultiple: "agents" | "movement" | "meter" | "unpriced" | "outside" | null;
  /** The meter's real cost split between covey and the sessions outside it
   *  (#120), per chunk of readings by the chunk's list-price share; null
   *  until a source names a meter, and for a seat with no split step. */
  split: ExternalMeterTotal | null;
  /** Sources name a meter, but no source names this seat, or its machines
   *  have not reported through any of its readings: its meter may carry use
   *  outside covey, so covey's figures on it are bounds. */
  outsideUnreported: boolean;
  billed: BilledFigure | null;
  /** Bookings of an older daemon's running totals (legacy, cumulative). */
  bookingCheck: BookingBasis[];
  agents: AgentFigure[];
};

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
    // A seat no source names has nobody to share it with: the server books
    // its movement to covey (L_out = 0), but the meter is account-wide and
    // may still carry use outside covey, so covey's figures stay bounds.
    // A linked seat whose only movement is unattributed (chunks that moved
    // with no call, F3c) is split too: nobody's, never covey's.
    const split = seatSplit && seatSplit.sources.length > 0
      && (seatSplit.split_steps > 0 || (seatSplit.unattributed_steps ?? 0) > 0) ? seatSplit : null;
    const outsideUnreported = seatSplit != null && split == null;
    let cov: MeterCoverage | null = null, real: RealFigure | null = null, aligned: number | null = null;
    let mult: Multiple | null = null;
    let noMultiple: CredentialFigures["noMultiple"] = null;
    if (a.key != null && kind !== "api_key") {
      cov = pickMeter(meter, a.key, a.slot!);
      if (cov) {
        real = realFromWeeks(meterWeeks(cov, steps, plans, bucketMs));
        aligned = cov.measured_usd ?? null;
      }
      if (sel.size > 0 || input.layout === "agents") noMultiple = "agents";
      else if (split) noMultiple = "outside";
      else if (!cov || !real || aligned == null) noMultiple = "meter";
      else if (!real.priced) noMultiple = "unpriced";
      else if (real.points < 3) noMultiple = "movement";
      else mult = multipleOf(aligned, real);
      // covey's list over a movement that may hold unreported outside use:
      // covey's real is at most the whole, so its multiple at least this.
      if (mult && outsideUnreported) mult = { kind: "floor", low: mult.low };
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
      };
    }).sort((x, y) => y.recomputed - x.recomputed);

    const bookingCheck = (data.bookings ?? []).filter((b) => credId(b.secret_key, b.secret_slot) === id && SUSPECT_BASES.includes(b.basis));
    out.push({
      id, secretKey: a.key, slot: a.slot, kind, plan,
      reported, recomputed,
      check: reported > 0 ? ((recomputed - reported) / reported) * 100 : null,
      bookedWithoutLine, meter: cov, real, aligned, multiple: mult, noMultiple, split, outsideUnreported, billed, bookingCheck, agents,
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
export function perCredentialTotals(figures: readonly CredentialFigures[]): { real: SourceTotal | null; billed: SourceTotal | null } {
  // The credentials whose tile offers a real cost: a credential of another
  // kind has none, so it is not one of the credentials the figure is "of".
  const subs = figures.filter((f) => f.secretKey != null && isSeat(f));
  const meters = subs.filter((f) => f.real != null);
  let real: SourceTotal | null = null;
  if (meters.length > 0) {
    const priced = meters.filter((f) => f.real!.priced);
    real = {
      usd: priced.reduce((a, f) => a + f.real!.value, 0),
      low: priced.reduce((a, f) => a + f.real!.low, 0),
      high: priced.reduce((a, f) => a + f.real!.high, 0),
      below: priced.length > 0 && priced.every((f) => f.real!.below),
      with: priced.length, of: subs.length, pending: false, syncError: null,
      reported: null,
      // covey's list beside a meter that sessions outside covey move too
      // would read as covey's multiple over the whole movement (#120): none.
      recomputed: priced.length > 0 && priced.every((f) => f.aligned != null && f.split == null && !f.outsideUnreported)
        ? priced.reduce((a, f) => a + f.aligned!, 0) : null,
      spans: meters.filter((f) => f.meter != null).map((f) => ({ id: f.id, from: new Date(f.meter!.baseline_at), to: new Date(f.meter!.last_at) })),
    };
  }
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
  return { real, billed };
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
 *  null when no complete chunk of split readings moved 10 points. */
export type DentSeat = {
  key: string; slot: number; sources: string[];
  real: number | null; points: number; covey: number; outside: number; unattributed: number; unsplit: number;
  unpricedPoints: number; split: boolean; pointsPerList: number | null;
  /** The seat's readings with a span in the range. */
  steps: number;
};

/** The whole dent in the subscription meters over the window, and its split
 *  (#120, F3c: per chunk of at least 5 points of readings, by the chunk's
 *  list-price shares). The parts add up to `real`: covey's agents and the
 *  outside sessions over the readings the outside sessions reported
 *  (estimates, ≈), `unattributed` for the movement neither explains (each
 *  chunk's points beyond the rate k — a lower bound — and chunks that moved
 *  with no call at all — an estimate; booked to nobody), `unsplit` over the
 *  readings they did not report — never booked to covey whole. The three
 *  multiples (list price over real cost, from 3 points of attributed
 *  movement) are lower bounds: list / (real + slack), the low end of the
 *  range the whole percents leave (the server's: ±½·TV of each part's share
 *  per chunk, at each reading's own point price). Points without a plan
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
  /** Outside calls with no price at all: left out of the split. */
  unpricedCalls: number;
  /** The outside calls' list price after the last reading: no real share yet. */
  unmeteredList: number;
  /** Why there are no multiples: "movement" (< 3 points in the split readings), "split" (none was split). */
  noMultiple: "movement" | "split" | null;
  /** The outside sources that name a meter. */
  sources: string[];
  seats: DentSeat[];
};

export function subscriptionDent(ext: ExternalUsage | null | undefined): SubscriptionDent | null {
  const m = ext?.meter;
  if (!m || m.credentials.length === 0) return null;
  const priced = m.credentials.some((c) => c.step_count > c.unpriced_steps);
  const real = m.real_usd;
  const splitReal = m.covey_real_usd + m.external_real_usd;
  const share = (v: number) => (real > 0 ? v / real : null);
  let noMultiple: SubscriptionDent["noMultiple"] = null;
  if (!(splitReal > 0)) noMultiple = "split";
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
  const sources = [...new Set(m.credentials.flatMap((c) => c.sources))].sort();
  const pricedSeat = (c: ExternalMeterTotal) => c.step_count > c.unpriced_steps;
  return {
    real: priced ? real : null,
    points: m.point_rate_mixed ? null : m.points,
    covey: part(m.covey_real_usd, m.split_covey_list_usd, m.split_covey_points, m.split_covey_slack_usd),
    outside: part(m.external_real_usd, m.split_external_list_usd, m.split_external_points, m.split_external_slack_usd),
    together: part(splitReal, m.split_covey_list_usd + m.split_external_list_usd, m.split_covey_points + m.split_external_points, m.split_slack_usd),
    unsplit: { real: m.unsplit_real_usd, share: share(m.unsplit_real_usd), steps: m.unsplit_steps,
      unpricedSteps: m.outside_unpriced_steps, unpricedReal: m.outside_unpriced_real_usd },
    unattributed: { real: m.unattributed_real_usd ?? 0, share: share(m.unattributed_real_usd ?? 0),
      points: m.unattributed_points ?? 0, steps: m.unattributed_steps ?? 0,
      beyond: (m.unattributed_real_usd ?? 0) - (m.unattributed_no_call_real_usd ?? 0), noCall: m.unattributed_no_call_real_usd ?? 0 },
    incompleteChunks: m.incomplete_chunks ?? 0,
    unpricedPoints: m.unpriced_points,
    unpricedCalls: m.unpriced_calls,
    unmeteredList: m.unmetered_list_usd,
    noMultiple,
    sources,
    seats: m.credentials.map((c) => ({
      key: c.secret_key, slot: c.secret_slot, sources: c.sources,
      real: pricedSeat(c) ? c.real_usd : null, points: c.points,
      covey: c.covey_real_usd, outside: c.external_real_usd, unattributed: c.unattributed_real_usd ?? 0, unsplit: c.unsplit_real_usd,
      unpricedPoints: c.unpriced_points, split: c.split_steps > 0, pointsPerList: c.points_per_list_usd ?? null, steps: c.step_count,
    })),
  };
}
