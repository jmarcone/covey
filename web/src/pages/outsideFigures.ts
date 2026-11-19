import type { TFunction } from "i18next";
import type { CostCells, ExternalMeterStep, ExternalModelCell, ExternalSource, ExternalUsage } from "../api";
import { bucketStart, recomputedOf } from "./costFigures";

/* The figures of the Claude Code sessions outside covey (#120, part 3/3):
 * the overview's series and tile, and the detailed view's splits. Pure
 * functions over the server's answers — every dollar is the server's F1/F2,
 * every real share its F3 (external.meter); nothing here prices a token or
 * splits a meter again. */

// --- Ranges and buckets -------------------------------------------------------

/** The ranges of the cost pages, with the bucket each starts at. */
export const RANGES: { v: number; bucket: string }[] = [
  { v: 1, bucket: "hour" },
  { v: 2, bucket: "hour" },
  { v: 7, bucket: "day" },
  { v: 30, bucket: "day" },
  { v: 90, bucket: "week" },
];

/** Hourly buckets only up to this many days: 90 days by the hour are 2,160
 *  periods, more than a chart (or a table) can show. */
export const HOUR_MAX_DAYS = 7;
/** 10-minute buckets up to 2 days (288 periods; the server clamps there too). */
export const TEN_MIN_MAX_DAYS = 2;
/** 10-hour buckets up to 30 days (72 periods). */
export const TEN_HOUR_MAX_DAYS = 30;

export type BucketOption = { v: string; label: string; disabled?: boolean; title?: string };

/** The bucket switch of the cost pages: a bucket too fine for the range is
 *  disabled and says why. */
export function bucketOptions(t: TFunction, days: number): BucketOption[] {
  const max = (v: string, label: string, limit: number, reason: string): BucketOption =>
    days > limit ? { v, label, disabled: true, title: t(reason, { days: limit }) } : { v, label };
  return [
    max("10min", t("costs.outside.b10min"), TEN_MIN_MAX_DAYS, "costs.outside.tenMinTooLong"),
    max("hour", t("costs.bHour"), HOUR_MAX_DAYS, "costs.hourTooLong"),
    max("10hour", t("costs.outside.b10hour"), TEN_HOUR_MAX_DAYS, "costs.outside.tenHourTooLong"),
    { v: "day", label: t("costs.bDay") },
    { v: "week", label: t("costs.bWeek") },
    { v: "month", label: t("costs.bMonth") },
  ];
}

/** The range switch's labels. */
export function rangeOptions(t: TFunction) {
  return RANGES.map((r) => ({ v: r.v, label: r.v === 1 ? t("costs.range24h") : t("costs.rangeDays", { count: r.v }) }));
}

/** The meter and the bill are read per hour: no real or billed figure at 10 minutes. */
export const belowMeter = (bucket: string) => bucket === "10min";

// --- The overview -------------------------------------------------------------

// --- The meter's steps, as the page counts them ----------------------------------

/** The reasons that leave a reading's real cost split to nobody. An idle
 *  reading (nothing moved, nothing to split) and one at no plan price (out
 *  of the dent, counted apart) are not among them. */
export const UNSPLIT_REASONS = ["not_reported", "moved_without_reported_call", "outside_unpriced"] as const;
export type UnsplitReason = (typeof UNSPLIT_REASONS)[number];
/** Per reason, the readings not split and their real cost. */
export type Unsplit = Record<UnsplitReason, { steps: number; usd: number }>;
export const emptyUnsplit = (): Unsplit => ({
  not_reported: { steps: 0, usd: 0 },
  moved_without_reported_call: { steps: 0, usd: 0 },
  outside_unpriced: { steps: 0, usd: 0 },
});
export const unsplitSteps = (u: Unsplit) => UNSPLIT_REASONS.reduce((a, r) => a + u[r].steps, 0);

/** How a step counts on the page: split (both parts set); not split for one
 *  of UNSPLIT_REASONS (its real cost, never $0 for either part); unpriced (no
 *  plan price: out of the dent); idle (it did not move: nothing to split —
 *  an unsplit step of 0 points, too, as the server's unsplit_steps does not
 *  count it either). */
export type StepKind = "split" | UnsplitReason | "unpriced" | "idle";
export function stepKind(s: ExternalMeterStep): StepKind {
  if (s.split) return "split";
  if (s.reason === "unpriced") return "unpriced";
  if (s.reason === "idle" || s.points === 0) return "idle";
  if (s.reason === "moved_without_reported_call" || s.reason === "outside_unpriced") return s.reason;
  // not_reported, or a reason this page does not know yet: not split.
  return "not_reported";
}

const seatOf = (s: { secret_key: string; secret_slot: number }) => `${s.secret_key}#${s.secret_slot}`;

/** The seats the chosen sources name (every source's without one). */
export function linkedSeats(sources: readonly ExternalSource[], source: string | null = null): Set<string> {
  return new Set(sources.filter((s) => s.meter_credential && (source == null || s.source === source))
    .map((s) => seatOf(s.meter_credential!)));
}

/** The unsplit readings of the given seats, per reason, and the readings at
 *  no plan price. */
export function unsplitOf(steps: readonly ExternalMeterStep[], seats: Set<string>): { unsplit: Unsplit; unpricedSteps: number } {
  const unsplit = emptyUnsplit();
  let unpricedSteps = 0;
  for (const s of steps) {
    if (!seats.has(seatOf(s))) continue;
    const k = stepKind(s);
    if (k === "unpriced") unpricedSteps += 1;
    else if (k !== "split" && k !== "idle") {
      unsplit[k].steps += 1;
      unsplit[k].usd += s.unsplit_real_usd;
    }
  }
  return { unsplit, unpricedSteps };
}

/** The "Outside covey" tile over the range. */
export type OutsideTotals = {
  /** Σ list_usd of the sources; null when no call was priced. */
  list: number | null;
  calls: number;
  /** Σ reported_usd of the sources (null: none reported a cost), and the
   *  calls that carried their own reported cost. */
  reported: number | null;
  reportedCalls: number;
  unpricedTokens: number;
  /** Some source names the subscription meter it runs on. */
  linked: boolean;
  /** F3 over the seats a source is linked to: the outside sessions' real
   *  share, and its part of those seats' split real cost (null: none split). */
  real: number;
  share: number | null;
  /** The readings of those seats not split, per reason: split to nobody. */
  unsplit: Unsplit;
  /** The readings of those seats at no plan price: out of the real cost. */
  unpricedSteps: number;
  /** Outside list price after the meter's last reading: no reading yet, no share. */
  unmeteredList: number;
};

export function outsideTotals(ext: ExternalUsage | null | undefined): OutsideTotals | null {
  if (!ext || ext.sources.length === 0) return null;
  let list: number | null = null, reported: number | null = null;
  let calls = 0, unpricedTokens = 0;
  for (const s of ext.sources) {
    if (s.list_usd != null) list = (list ?? 0) + s.list_usd;
    if (s.reported_usd != null) reported = (reported ?? 0) + s.reported_usd;
    calls += s.calls;
    unpricedTokens += s.unpriced_tokens;
  }
  // Only the seats a source names: a seat no source is linked to is all
  // covey's (L_out = 0) and says nothing about the outside sessions.
  const seats = linkedSeats(ext.sources);
  const creds = ext.meter.credentials.filter((c) => seats.has(seatOf(c)));
  const real = creds.reduce((a, c) => a + c.external_real_usd, 0);
  const splitReal = creds.reduce((a, c) => a + c.external_real_usd + c.covey_real_usd, 0);
  const splitSteps = creds.reduce((a, c) => a + c.split_steps, 0);
  const { unsplit, unpricedSteps } = unsplitOf(ext.meter.steps, seats);
  return {
    list, calls, reported, unpricedTokens,
    reportedCalls: ext.cells.reduce((a, c) => a + c.reported_calls, 0),
    linked: seats.size > 0,
    real,
    share: splitSteps > 0 && splitReal > 0 ? real / splitReal : null,
    unsplit,
    unpricedSteps,
    unmeteredList: creds.reduce((a, c) => a + c.unmetered_list_usd, 0),
  };
}

/** The outside list price per period (keyed by the period's start in ms);
 *  usd is null for a period none of whose calls had a list price — never 0. */
export function outsidePerPeriod(ext: ExternalUsage | null | undefined): Map<number, { period: string; usd: number | null }> {
  const out = new Map<number, { period: string; usd: number | null }>();
  for (const c of ext?.cells ?? []) {
    const k = Date.parse(c.period);
    const e = out.get(k) ?? { period: c.period, usd: null };
    if (c.list_usd != null) e.usd = (e.usd ?? 0) + c.list_usd;
    out.set(k, e);
  }
  return out;
}

/** The agents' recomputed list price per period, narrowed by the agent filter. */
export function agentsPerPeriod(data: CostCells | undefined, selected: Set<string>): Map<number, { period: string; usd: number }> {
  const out = new Map<number, { period: string; usd: number }>();
  for (const c of data?.cells ?? []) {
    if (selected.size > 0 && !selected.has(c.agent_id)) continue;
    const k = Date.parse(c.period);
    const e = out.get(k) ?? { period: c.period, usd: 0 };
    e.usd += recomputedOf(c);
    out.set(k, e);
  }
  return out;
}

/** The series is on by default in the org-wide view, off once the page is
 *  narrowed to agents; the user's own choice wins in either. */
export const outsideDefaultOn = (filtered: boolean) => !filtered;

// --- The detailed view ----------------------------------------------------------

export const TOKEN_KINDS = ["input", "output", "cache_read", "cache_write_5m", "cache_write_1h"] as const;
export const ORIGINS = ["main", "subagent", "workflow", "unknown"] as const;
export type Origin = (typeof ORIGINS)[number];
export const originOf = (c: { origin: string | null }): Origin =>
  c.origin === "main" || c.origin === "subagent" || c.origin === "workflow" ? c.origin : "unknown";

/** One row of a split: calls, tokens and their $ per type, list $ (null when
 *  none of its calls was priced), unpriced tokens. */
export type SplitRow = {
  key: string;
  calls: number;
  list: number | null;
  reported: number | null;
  /** The calls that carried their own reported cost. */
  reportedCalls: number;
  tokens: Record<string, number>;
  typeUSD: Record<string, number>;
  unpricedTokens: number;
};

const emptyRow = (key: string): SplitRow => ({ key, calls: 0, list: null, reported: null, reportedCalls: 0, tokens: {}, typeUSD: {}, unpricedTokens: 0 });

function add(r: SplitRow, c: ExternalModelCell) {
  r.calls += c.calls;
  if (c.list_usd != null) r.list = (r.list ?? 0) + c.list_usd;
  if (c.reported_usd != null) r.reported = (r.reported ?? 0) + c.reported_usd;
  r.reportedCalls += c.reported_calls;
  for (const k of TOKEN_KINDS) {
    r.tokens[k] = (r.tokens[k] ?? 0) + (c.tokens[k] ?? 0);
    if (c.type_usd[k] != null) r.typeUSD[k] = (r.typeUSD[k] ?? 0) + c.type_usd[k];
  }
  r.unpricedTokens += c.unpriced_tokens;
}

/** The cells summed per key, the dearest first; and the total over all of them. */
export function splitBy(cells: readonly ExternalModelCell[], keyOf: (c: ExternalModelCell) => string): { rows: SplitRow[]; total: SplitRow } {
  const by = new Map<string, SplitRow>();
  const total = emptyRow("total");
  for (const c of cells) {
    const k = keyOf(c);
    const r = by.get(k) ?? emptyRow(k);
    add(r, c);
    add(total, c);
    by.set(k, r);
  }
  const rows = [...by.values()].sort((a, b) => (b.list ?? -1) - (a.list ?? -1) || a.key.localeCompare(b.key));
  return { rows, total };
}

/** Per period, the list $ of each key (model or origin), for the stacked
 *  chart: only the priced cells; priced is false for a period none of whose
 *  calls had a list price, which then has no bar (never one of $0). */
export function perPeriodBy(cells: readonly ExternalModelCell[], keyOf: (c: ExternalModelCell) => string) {
  const by = new Map<number, { period: string; v: Map<string, number>; priced: boolean }>();
  for (const c of cells) {
    const p = Date.parse(c.period);
    const e = by.get(p) ?? { period: c.period, v: new Map<string, number>(), priced: false };
    if (c.list_usd != null) {
      e.v.set(keyOf(c), (e.v.get(keyOf(c)) ?? 0) + c.list_usd);
      e.priced = true;
    }
    by.set(p, e);
  }
  return [...by.entries()].sort((a, b) => a[0] - b[0]).map((e) => e[1]);
}

/** One period of the meter for the chosen sources. real: the real cost of
 *  its priced steps (null: none had a plan price). outside and covey: the
 *  parts of its split steps, a figure only while splitSteps > 0. Then the
 *  steps not split, per reason, with their real cost; the steps at no plan
 *  price; the idle ones. noReading: the outside sessions worked in it on a
 *  seat that has no reading placed there (another seat's may be). */
export type MeterPeriod = {
  period: number;
  real: number | null;
  splitSteps: number;
  outside: number;
  covey: number;
  unsplit: Unsplit;
  unpricedSteps: number;
  idleSteps: number;
  noReading: boolean;
};

/** The meter per period of the chart, over the seats the chosen sources are
 *  linked to (every linked seat without a source). Steps are placed at their
 *  reading, as the overview's chart places them. */
export function meterPerPeriod(ext: { meter: ExternalUsage["meter"]; sources: ExternalUsage["sources"] },
  cells: readonly ExternalModelCell[], bucket: string, source: string | null): MeterPeriod[] {
  const seats = linkedSeats(ext.sources, source);
  const by = new Map<number, MeterPeriod>();
  const at = (p: number) => {
    let e = by.get(p);
    if (!e) {
      e = { period: p, real: null, splitSteps: 0, outside: 0, covey: 0, unsplit: emptyUnsplit(), unpricedSteps: 0, idleSteps: 0, noReading: false };
      by.set(p, e);
    }
    return e;
  };
  // The periods each seat has a reading in: a source's work is placed on
  // its own seat's readings, so another seat's reading in the same period
  // does not stand in for it.
  const read = new Set<string>();
  for (const s of ext.meter.steps) {
    if (!seats.has(seatOf(s))) continue;
    const p = bucketStart(Date.parse(s.at), bucket);
    read.add(`${seatOf(s)}@${p}`);
    const e = at(p);
    if (s.real_usd != null) e.real = (e.real ?? 0) + s.real_usd;
    const k = stepKind(s);
    if (k === "split") {
      e.splitSteps += 1;
      e.outside += s.external_real_usd ?? 0;
      e.covey += s.covey_real_usd ?? 0;
    } else if (k === "unpriced") {
      e.unpricedSteps += 1;
    } else if (k === "idle") {
      e.idleSteps += 1;
    } else {
      e.unsplit[k].steps += 1;
      e.unsplit[k].usd += s.unsplit_real_usd;
    }
  }
  if (seats.size > 0) {
    for (const c of cells) {
      if (c.calls === 0) continue;
      const linked = ext.sources.find((s) => s.source === c.source)?.meter_credential;
      if (!linked || !seats.has(seatOf(linked))) continue;
      const p = bucketStart(Date.parse(c.period), bucket);
      if (!read.has(`${seatOf(linked)}@${p}`)) at(p).noReading = true;
    }
  }
  return [...by.values()].sort((a, b) => a.period - b.period);
}
