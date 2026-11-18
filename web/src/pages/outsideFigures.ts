import type { TFunction } from "i18next";
import type { CostCells, ExternalModelCell, ExternalUsage } from "../api";
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

/** The "Outside covey" tile over the range. */
export type OutsideTotals = {
  /** Σ list_usd of the sources; null when no call was priced. */
  list: number | null;
  calls: number;
  unpricedTokens: number;
  /** Some source names the subscription meter it runs on. */
  linked: boolean;
  /** F3: the outside sessions' real share, and its part of the split steps' real cost. */
  real: number;
  share: number | null;
  /** Readings the outside sessions had not reported through: split to nobody. */
  notReported: number;
  notReportedUSD: number;
  /** Outside list price after the meter's last reading: no reading yet, no share. */
  unmeteredList: number;
};

export function outsideTotals(ext: ExternalUsage | null | undefined): OutsideTotals | null {
  if (!ext || ext.sources.length === 0) return null;
  let list: number | null = null;
  let calls = 0, unpricedTokens = 0;
  for (const s of ext.sources) {
    if (s.list_usd != null) list = (list ?? 0) + s.list_usd;
    calls += s.calls;
    unpricedTokens += s.unpriced_tokens;
  }
  const notReported = ext.meter.steps.filter((s) => s.reason === "not_reported");
  return {
    list, calls, unpricedTokens,
    linked: ext.sources.some((s) => s.meter_credential != null),
    real: ext.meter.external_real_usd,
    share: ext.meter.share,
    notReported: notReported.length,
    notReportedUSD: notReported.reduce((a, s) => a + s.unsplit_real_usd, 0),
    unmeteredList: ext.meter.unmetered_list_usd,
  };
}

/** The outside list price per period (keyed by the period's start in ms). */
export function outsidePerPeriod(ext: ExternalUsage | null | undefined): Map<number, { period: string; usd: number }> {
  const out = new Map<number, { period: string; usd: number }>();
  for (const c of ext?.cells ?? []) {
    const k = Date.parse(c.period);
    const e = out.get(k) ?? { period: c.period, usd: 0 };
    e.usd += c.list_usd ?? 0;
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
  tokens: Record<string, number>;
  typeUSD: Record<string, number>;
  unpricedTokens: number;
};

const emptyRow = (key: string): SplitRow => ({ key, calls: 0, list: null, reported: null, tokens: {}, typeUSD: {}, unpricedTokens: 0 });

function add(r: SplitRow, c: ExternalModelCell) {
  r.calls += c.calls;
  if (c.list_usd != null) r.list = (r.list ?? 0) + c.list_usd;
  if (c.reported_usd != null) r.reported = (r.reported ?? 0) + c.reported_usd;
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

/** Per period, the list $ of each key (model or origin), for the stacked chart. */
export function perPeriodBy(cells: readonly ExternalModelCell[], keyOf: (c: ExternalModelCell) => string) {
  const by = new Map<number, { period: string; v: Map<string, number> }>();
  for (const c of cells) {
    const p = Date.parse(c.period);
    const e = by.get(p) ?? { period: c.period, v: new Map<string, number>() };
    e.v.set(keyOf(c), (e.v.get(keyOf(c)) ?? 0) + (c.list_usd ?? 0));
    by.set(p, e);
  }
  return [...by.entries()].sort((a, b) => a[0] - b[0]).map((e) => e[1]);
}

/** One period of the meter for the chosen sources: the real cost of its
 *  steps, the outside part, covey's, and what was split to nobody; "none"
 *  when the outside sessions worked in it but no reading was placed there. */
export type MeterPeriod = {
  period: number;
  real: number | null;
  outside: number;
  covey: number;
  unsplit: number;
  notReported: number;
  noReading: boolean;
};

/** The meter per period of the chart, over the seats the chosen sources are
 *  linked to (every linked seat without a source). Steps are placed at their
 *  reading, as the overview's chart places them. */
export function meterPerPeriod(ext: { meter: ExternalUsage["meter"]; sources: ExternalUsage["sources"] },
  cells: readonly ExternalModelCell[], bucket: string, source: string | null): MeterPeriod[] {
  const seats = new Set(ext.sources.filter((s) => s.meter_credential && (source == null || s.source === source))
    .map((s) => `${s.meter_credential!.secret_key}#${s.meter_credential!.secret_slot}`));
  const by = new Map<number, MeterPeriod>();
  const at = (p: number) => {
    let e = by.get(p);
    if (!e) {
      e = { period: p, real: null, outside: 0, covey: 0, unsplit: 0, notReported: 0, noReading: false };
      by.set(p, e);
    }
    return e;
  };
  for (const s of ext.meter.steps) {
    if (!seats.has(`${s.secret_key}#${s.secret_slot}`)) continue;
    const e = at(bucketStart(Date.parse(s.at), bucket));
    if (s.real_usd != null) e.real = (e.real ?? 0) + s.real_usd;
    e.outside += s.external_real_usd ?? 0;
    e.covey += s.covey_real_usd ?? 0;
    e.unsplit += s.unsplit_real_usd;
    if (s.reason === "not_reported") e.notReported += 1;
  }
  if (seats.size > 0) {
    for (const c of cells) {
      if (c.calls === 0) continue;
      const linked = ext.sources.find((s) => s.source === c.source)?.meter_credential;
      if (!linked) continue;
      const p = bucketStart(Date.parse(c.period), bucket);
      if (!by.has(p)) at(p).noReading = true;
    }
  }
  return [...by.values()].sort((a, b) => a.period - b.period);
}
