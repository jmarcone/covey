import type { TFunction } from "i18next";
import type { CostCells, ExternalCell, ExternalMeterStep, ExternalMeterTotal, ExternalModelCell, ExternalSource, ExternalUsage } from "../api";
import { bucketStart, meterPriced, recomputedOf } from "./costFigures";

/* The figures of the Claude Code sessions outside covey (#120, part 3/3):
 * the overview's series and tile, and the detailed view's splits. Pure
 * functions over the server's answers — every dollar is the server's F1/F2,
 * every real share its F3c (external.meter, split per chunk of readings:
 * covey's agents, the outside sessions, the unattributed part booked to
 * nobody, the readings not split); nothing here prices a token or splits a
 * meter again. */

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


// --- The overview -------------------------------------------------------------

// --- The meter's steps, as the page counts them ----------------------------------

/** The reasons that leave a reading's real cost split to nobody (not split).
 *  An idle reading (nothing moved, nothing to split), one at no plan price
 *  (out of the dent, counted apart) and one that moved with no reported call
 *  on either side are not among them: since F3c such a reading is split with
 *  its chunk, or unattributed whole in a chunk with no list at all. */
export const UNSPLIT_REASONS = ["not_reported", "outside_unpriced"] as const;
export type UnsplitReason = (typeof UNSPLIT_REASONS)[number];
/** Per reason, the readings not split and their real cost. */
export type Unsplit = Record<UnsplitReason, { steps: number; usd: number }>;
export const emptyUnsplit = (): Unsplit => ({
  not_reported: { steps: 0, usd: 0 },
  outside_unpriced: { steps: 0, usd: 0 },
});
export const unsplitSteps = (u: Unsplit) => UNSPLIT_REASONS.reduce((a, r) => a + u[r].steps, 0);
export const unsplitUSD = (u: Unsplit) => UNSPLIT_REASONS.reduce((a, r) => a + u[r].usd, 0);

/** The unattributed part (F3c): movement neither covey's agents nor the
 *  reported sessions explain, booked to nobody, always as the server has
 *  it (each step's unattributed_real_usd — never inferred from a reason).
 *  beyond: the split readings' part, their chunk's points beyond the rate
 *  (a lower bound); noCall: the readings unattributed whole, in a chunk with
 *  no list at all (they moved with no call: the meter's lag or another
 *  machine; an estimate). usd = beyond + noCall.usd. */
export type Unattributed = { usd: number; beyond: number; noCall: { steps: number; usd: number } };
export const emptyUnattributed = (): Unattributed => ({ usd: 0, beyond: 0, noCall: { steps: 0, usd: 0 } });

/** How a step counts on the page: split (the parts set at its chunk's
 *  share, and the chunk's unattributed part beyond the rate — a reading
 *  that moved with no call and joined a chunk is split too, it keeps its
 *  reason); unattributed (not split, and the server books its movement to
 *  nobody: a chunk with no list; never $0 for either part); not split for
 *  one of UNSPLIT_REASONS (its real cost, never $0 for either part);
 *  unpriced (no plan price: out of the dent); usage (read while the seat was
 *  billed by usage, 0151: out of the dent, its money the usage charges); idle (it did not move: nothing
 *  to split — an unsplit step of 0 points, too, as the server's
 *  unsplit_steps does not count it either). */
export type StepKind = "split" | "unattributed" | UnsplitReason | "unpriced" | "usage" | "idle";
export function stepKind(s: ExternalMeterStep): StepKind {
  if (s.split) return "split";
  if (s.reason === "unpriced") return "unpriced";
  if (s.reason === "usage") return "usage";
  if ((s.unattributed_real_usd ?? 0) > 0) return "unattributed";
  if (s.reason === "idle" || s.reason === "moved_without_reported_call" || s.points === 0) return "idle";
  if (s.reason === "outside_unpriced") return s.reason;
  // not_reported, or a reason this page does not know yet: not split.
  return "not_reported";
}

/** Adds a step's unattributed part to a tally: a split step's to the part
 *  beyond the rate, an unattributed step's whole to noCall. */
function addUnattributed(u: Unattributed, s: ExternalMeterStep, k: StepKind) {
  const v = s.unattributed_real_usd ?? 0;
  u.usd += v;
  if (k === "unattributed") {
    u.noCall.steps += 1;
    u.noCall.usd += v;
  } else {
    u.beyond += v;
  }
}

const seatOf = (s: { secret_key: string; secret_slot: number }) => `${s.secret_key}#${s.secret_slot}`;

/** The seats the chosen sources name (every source's without one). */
export function linkedSeats(sources: readonly ExternalSource[], source: string | null = null): Set<string> {
  return new Set(sources.filter((s) => s.meter_credential && (source == null || s.source === source))
    .map((s) => seatOf(s.meter_credential!)));
}

/** The unsplit readings of the given seats, per reason, their unattributed
 *  part, the readings at no plan price and those under the usage basis. */
export function unsplitOf(steps: readonly ExternalMeterStep[], seats: Set<string>):
  { unsplit: Unsplit; unattributed: Unattributed; unpricedSteps: number; usageSteps: number } {
  const unsplit = emptyUnsplit();
  const unattributed = emptyUnattributed();
  let unpricedSteps = 0, usageSteps = 0;
  for (const s of steps) {
    if (!seats.has(seatOf(s))) continue;
    const k = stepKind(s);
    addUnattributed(unattributed, s, k);
    if (k === "unpriced") unpricedSteps += 1;
    else if (k === "usage") usageSteps += 1;
    else if (k === "not_reported" || k === "outside_unpriced") {
      unsplit[k].steps += 1;
      unsplit[k].usd += s.unsplit_real_usd;
    }
  }
  return { unsplit, unattributed, unpricedSteps, usageSteps };
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
  /** F3c over the seats a source is linked to: the outside sessions' real
   *  share (an estimate), its part of those seats' dent (null: none split),
   *  and the unattributed part in its two halves. */
  meter: MeterShare;
  /** The readings of those seats not split, per reason: split to nobody. */
  unsplit: Unsplit;
  /** Their readings the server books to nobody whole (a chunk with no list). */
  noCall: Unattributed["noCall"];
  /** The readings of those seats at no plan price: out of the real cost. */
  unpricedSteps: number;
  /** Their readings while billed by usage (0151): no dent, the usage charges are the money. */
  usageSteps: number;
  /** Outside list price in no reading's span: no share. unmeteredBefore:
   *  the part before the meter's first reading in the range (no meter data
   *  for that time, never a share); the rest is after its last reading
   *  (pending until the next one). */
  unmeteredList: number;
  unmeteredBefore: number;
};

/** The outside sessions' part of the given seats' meters (F3c). real: their
 *  real cost, an estimate (≈); dent: the seats' real cost over their priced
 *  readings; share: real ÷ dent, null while no reading was split (never
 *  0 %); unattributed: the server's sum, booked to nobody, and its halves —
 *  noCall, the chunks with no list (an estimate), and beyond, the rest:
 *  chunks' points beyond the rate (a lower bound); incomplete: chunks with
 *  use not in their list price, whose parts are approximate. */
export type MeterShare = {
  real: number; dent: number; share: number | null; splitSteps: number;
  unattributed: number; beyond: number; noCall: number; incomplete: number;
};

export function meterShare(creds: readonly ExternalMeterTotal[], wholeDent?: number): MeterShare {
  const sum = (f: (c: ExternalMeterTotal) => number) => creds.reduce((a, c) => a + f(c), 0);
  const real = sum((c) => c.external_real_usd);
  // The overview's share is of the whole dent, as the headline's is (#120
  // review); the detailed view's per-seat share is of its seat's.
  const dent = wholeDent ?? sum((c) => (meterPriced(c) ? c.real_usd : 0));
  const splitSteps = sum((c) => c.split_steps);
  const unattributed = sum((c) => c.unattributed_real_usd ?? 0);
  const noCall = sum((c) => c.unattributed_no_call_real_usd ?? 0);
  return {
    real, dent, share: splitSteps > 0 && dent > 0 ? real / dent : null, splitSteps,
    unattributed, noCall, beyond: Math.max(0, unattributed - noCall), incomplete: sum((c) => c.incomplete_chunks ?? 0),
  };
}

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
  const { unsplit, unattributed, unpricedSteps, usageSteps } = unsplitOf(ext.meter.steps, seats);
  return {
    list, calls, reported, unpricedTokens,
    reportedCalls: ext.cells.reduce((a, c) => a + c.reported_calls, 0),
    linked: seats.size > 0,
    meter: meterShare(creds, ext.meter.credentials.reduce((a, c) => a + (meterPriced(c) ? c.real_usd : 0), 0)),
    unsplit,
    noCall: unattributed.noCall,
    unpricedSteps,
    usageSteps,
    unmeteredList: creds.reduce((a, c) => a + c.unmetered_list_usd, 0),
    unmeteredBefore: creds.reduce((a, c) => a + (c.unmetered_before_list_usd ?? 0), 0),
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

// --- Included on the Costs page, by default and always labelled ----------------
//
// The human's question (#120): if all the work on the subscription is
// counted for the period — covey's agents and the operator's own sessions —
// what would it have cost by token consumption and by Claude Code's own
// reports, against what the subscription really cost? So the page's tiles,
// cost chart and tables include the sessions outside covey by default,
// each figure labelled with its two parts; "covey only" shows the agents
// alone, as before. The agents' own figures (/cost/cells' cells, /cost/models)
// are never changed: the outside part is added on the page from the
// `external` field and the external-usage endpoint, and named wherever it
// is added.

/** Each source's real part of the meters (the server's source_parts over the
 *  whole meter, else summed over the seats). */
export function sourceRealParts(ext: Pick<ExternalUsage, "meter"> | null | undefined): Map<string, number> {
  const out = new Map<string, number>();
  const m = ext?.meter;
  const parts = m?.source_parts ?? (m?.credentials ?? []).flatMap((c) => c.source_parts ?? []);
  for (const p of parts) out.set(p.source, (out.get(p.source) ?? 0) + p.real_usd);
  return out;
}

/** A source the page lists: one with calls (or list price) in the range, or
 *  with a real part of the meter — its calls lie before the range, in a
 *  reading's span that straddles its start, and its part must be shown
 *  where the parts are added up (#120 review). The uploader pushes one
 *  source per project folder, and a folder with transcripts but no usage
 *  sends claim-only requests: a source of 0 calls and no part is in the API,
 *  never in a sub-line, a series, a row or a source list. */
export const listedSource = (s: Pick<ExternalSource, "source" | "calls" | "list_usd">, real?: ReadonlyMap<string, number>) =>
  s.calls > 0 || s.list_usd != null || (real?.get(s.source) ?? 0) > 0;

/** Listed only for its real part: its calls lie before the range. */
export const beforeRange = (s: Pick<ExternalSource, "source" | "calls" | "list_usd">, real?: ReadonlyMap<string, number>) =>
  s.calls === 0 && s.list_usd == null && (real?.get(s.source) ?? 0) > 0;

/** The sources the page lists, by name. */
export const listedSources = (ext: ExternalUsage | null | undefined): ExternalSource[] => {
  const real = sourceRealParts(ext);
  return (ext?.sources ?? []).filter((s) => listedSource(s, real)).sort((a, b) => a.source.localeCompare(b.source));
};

/** What changes when an outside call lands: the sources' calls, list price
 *  and last call. A read that only depends on them is fetched again when
 *  this changes, not on every poll (#120 review, L4). */
export const externalStamp = (ext: ExternalUsage | null | undefined): string =>
  (ext?.sources ?? []).map((s) => `${s.source}:${s.calls}:${s.list_usd ?? "-"}:${s.last_at ?? "-"}`).join("|");

// --- One pattern per source -------------------------------------------------------

/** The hatch indices (HATCH_ANGLES in StackedCostChart) the sources take:
 *  135°, 90°, 0° and 45° — four angles that read apart. 22° and 158° did not
 *  (#120 review, M5). */
export const SOURCE_HATCHES = [1, 2, 3, 0] as const;
/** The inks a source's stripes are drawn in (styles.css, each at least 3:1
 *  against the card in both schemes): angle and ink change together from one
 *  source to the next, twelve combinations before one repeats. */
export const SOURCE_INKS = ["var(--cost-source-1)", "var(--cost-source-2)", "var(--cost-source-3)"] as const;
/** The dotted pattern of "other sources" (HATCH_ANGLES' OTHER_HATCH). */
export const OTHER_SOURCES_HATCH = 5;
/** Sources drawn as series of their own; beyond it the rest share one,
 *  "other sources", whose members the tooltip names. */
export const SOURCE_SERIES_MAX = 8;
/** The series key of the folded sources. */
export const OTHER_SOURCES = "\u0000other";

export type SourceStyle = { hatch: number; ink: string | undefined };
const OTHER_STYLE: SourceStyle = { hatch: OTHER_SOURCES_HATCH, ink: undefined };

/** Every listed source's pattern, computed once per answer: the dearest
 *  SOURCE_SERIES_MAX (or, with more, one fewer) have one of their own, in
 *  name order; the rest — and a source the page does not list — are drawn
 *  as "other sources", never in another source's pattern. */
export type SourceStyles = {
  style: (source: string) => SourceStyle;
  /** The series a source is drawn in: its name, or OTHER_SOURCES. */
  series: (source: string) => string;
  /** The folded sources, by name. */
  folded: string[];
};

export function sourceStyles(ext: ExternalUsage | null | undefined): SourceStyles {
  const listed = listedSources(ext);
  const fold = listed.length > SOURCE_SERIES_MAX;
  const keep = new Set((fold
    ? [...listed].sort((a, b) => (b.list_usd ?? -1) - (a.list_usd ?? -1) || a.source.localeCompare(b.source)).slice(0, SOURCE_SERIES_MAX - 1)
    : listed).map((s) => s.source));
  const styles = new Map<string, SourceStyle>();
  let i = 0;
  for (const s of listed) {
    if (!keep.has(s.source)) continue;
    styles.set(s.source, { hatch: SOURCE_HATCHES[i % SOURCE_HATCHES.length], ink: SOURCE_INKS[i % SOURCE_INKS.length] });
    i++;
  }
  return {
    style: (source) => styles.get(source) ?? OTHER_STYLE,
    series: (source) => (styles.has(source) ? source : OTHER_SOURCES),
    folded: listed.filter((s) => !keep.has(s.source)).map((s) => s.source),
  };
}

/** The URL parameter of the "covey only" choice: ?only=covey. */
export const COVEY_ONLY_PARAM = "only";
export const COVEY_ONLY_VALUE = "covey";

/** The URL parameter of the full view of covey's price table (#120): with
 *  ?prices=recomputed, Reported by Claude Code and Recomputed from tokens
 *  stand apart again as tiles, series and columns; without it one lead
 *  figure stands, with the check under it. */
export const PRICES_PARAM = "prices";
export const PRICES_RECOMPUTED = "recomputed";

/** The outside sessions' figures beside the agents' in the tiles: list
 *  price (null when no call was priced — never $0), Claude Code's reported
 *  cost (null when no call carried one) and the calls that did, tokens per
 *  type over every call, priced or not, and the tokens of models with no
 *  list price. Over every source, or the given ones. */
export type OutsideKpis = {
  sources: string[];
  calls: number;
  list: number | null;
  reported: number | null;
  reportedCalls: number;
  tokens: Record<string, number>;
  unpricedTokens: number;
  /** At the lead price (cellLead): null when no call had a price of either
   *  kind; leadRecomputed, the calls priced at the list for want of a
   *  reported cost (marked wherever the figure stands); leadUnpriced*, the
   *  calls with neither. Optional for a part built by hand (kpiLead). */
  lead?: number | null;
  leadRecomputed?: number;
  leadUnpricedCalls?: number;
  leadUnpricedTokens?: number;
};

/** Adds a lead price to a part's figures. */
function addLead(k: OutsideKpis, l: ReturnType<typeof cellLead>) {
  if (l.lead != null) k.lead = (k.lead ?? 0) + l.lead;
  k.leadRecomputed = (k.leadRecomputed ?? 0) + l.recomputed;
  k.leadUnpricedCalls = (k.leadUnpricedCalls ?? 0) + l.unpricedCalls;
  k.leadUnpricedTokens = (k.leadUnpricedTokens ?? 0) + l.unpricedTokens;
}

/** One source over the range at the lead price: the sum of its cells'
 *  (cellLead); a source the answer carries no cell of (a hand-made one)
 *  at its own lead_usd, else its list price. */
export function sourceLead(s: Pick<ExternalSource, "source" | "calls" | "list_usd" | "unpriced_calls" | "unpriced_tokens" | "lead_usd"
  | "lead_unpriced_calls" | "lead_unpriced_tokens">, cells: readonly ExternalCell[]): ReturnType<typeof cellLead> {
  const out: ReturnType<typeof cellLead> = { lead: null, recomputed: 0, unpricedCalls: 0, unpricedTokens: 0 };
  let any = false;
  for (const c of cells) {
    if (c.source !== s.source) continue;
    any = true;
    const l = cellLead(c);
    if (l.lead != null) out.lead = (out.lead ?? 0) + l.lead;
    out.recomputed += l.recomputed;
    out.unpricedCalls += l.unpricedCalls;
    out.unpricedTokens += l.unpricedTokens;
  }
  if (any || s.calls === 0) return out;
  if (s.lead_usd !== undefined) {
    return { lead: s.lead_usd, recomputed: 0, unpricedCalls: s.lead_unpriced_calls ?? 0, unpricedTokens: s.lead_unpriced_tokens ?? 0 };
  }
  return { lead: s.list_usd, recomputed: s.list_usd != null ? s.calls - s.unpriced_calls : 0, unpricedCalls: s.unpriced_calls,
    unpricedTokens: s.unpriced_tokens };
}

/** A part's lead price, its recomputed calls and its calls with neither
 *  price: its own where it has them (outsideKpis), else read from its
 *  reported and list figures as an older server's cell is (cellLead). */
export function kpiLead(o: Pick<OutsideKpis, "calls" | "list" | "reported" | "reportedCalls" | "unpricedTokens"> & Partial<Pick<OutsideKpis,
  "lead" | "leadRecomputed" | "leadUnpricedCalls" | "leadUnpricedTokens">>): { lead: number | null; recomputed: number; unpricedCalls: number; unpricedTokens: number } {
  if (o.lead !== undefined) {
    return { lead: o.lead, recomputed: o.leadRecomputed ?? 0, unpricedCalls: o.leadUnpricedCalls ?? 0, unpricedTokens: o.leadUnpricedTokens ?? 0 };
  }
  if (o.reported != null && o.reportedCalls >= o.calls) return { lead: o.reported, recomputed: 0, unpricedCalls: 0, unpricedTokens: 0 };
  return { lead: o.list, recomputed: o.list != null ? o.calls : 0, unpricedCalls: o.unpricedTokens > 0 ? 1 : 0, unpricedTokens: o.unpricedTokens };
}

export function outsideKpis(ext: ExternalUsage | null | undefined, only?: ReadonlySet<string>): OutsideKpis | null {
  const sources = listedSources(ext).filter((s) => !only || only.has(s.source));
  if (!ext || sources.length === 0) return null;
  const names = new Set(sources.map((s) => s.source));
  const out: OutsideKpis = { sources: [...names].sort(), calls: 0, list: null, reported: null, reportedCalls: 0, tokens: {}, unpricedTokens: 0,
    lead: null, leadRecomputed: 0, leadUnpricedCalls: 0, leadUnpricedTokens: 0 };
  for (const s of sources) {
    out.calls += s.calls;
    if (s.list_usd != null) out.list = (out.list ?? 0) + s.list_usd;
    if (s.reported_usd != null) out.reported = (out.reported ?? 0) + s.reported_usd;
    out.unpricedTokens += s.unpriced_tokens;
    addLead(out, sourceLead(s, ext.cells));
  }
  for (const c of ext.cells) {
    if (!names.has(c.source)) continue;
    out.reportedCalls += c.reported_calls;
    for (const [k, v] of Object.entries(c.tokens ?? {})) out.tokens[k] = (out.tokens[k] ?? 0) + v;
  }
  return out;
}

/** Every listed source's figures, in one pass over the cells (computed once
 *  per answer, not per row and render: #120 review, L4). */
export function outsideKpisBySource(ext: ExternalUsage | null | undefined): Map<string, OutsideKpis> {
  const out = new Map<string, OutsideKpis>();
  if (!ext) return out;
  for (const s of listedSources(ext)) {
    const l = sourceLead(s, ext.cells);
    out.set(s.source, { sources: [s.source], calls: s.calls, list: s.list_usd, reported: s.reported_usd, reportedCalls: 0, tokens: {},
      unpricedTokens: s.unpriced_tokens, lead: l.lead, leadRecomputed: l.recomputed, leadUnpricedCalls: l.unpricedCalls, leadUnpricedTokens: l.unpricedTokens });
  }
  for (const c of ext.cells) {
    const k = out.get(c.source);
    if (!k) continue;
    k.reportedCalls += c.reported_calls;
    for (const [t, v] of Object.entries(c.tokens ?? {})) k.tokens[t] = (k.tokens[t] ?? 0) + v;
  }
  return out;
}

/** The listed sources split by where they are counted (#120 review, M6):
 *  on a subscription meter (a seat they name), or billed per use (none). */
export function sourcesByMeter(ext: ExternalUsage | null | undefined): { meter: Set<string>; perUse: Set<string> } {
  const meter = new Set<string>(), perUse = new Set<string>();
  for (const s of listedSources(ext)) (s.meter_credential ? meter : perUse).add(s.source);
  return { meter, perUse };
}

/** One source in one chart period: list price (null when none of its calls
 *  had one), reported cost (null when no call carried one), its calls, and
 *  the list price per cost type, as the agents' cells carry it. lead: the
 *  lead price (cellLead), with the calls it took at their list price
 *  (leadRecomputed) and those with neither price (leadUnpriced). */
export type SourcePeriod = {
  period: string; calls: number; list: number | null; reported: number | null; measured: Record<string, number>; tokens: Record<string, number>;
  lead: number | null; leadRecomputed: number; leadUnpricedCalls: number; leadUnpricedTokens: number;
};

/** One outside cell at the lead price (#120, the human's rule): each call's
 *  reported cost where it carried one, else its list price; null when no
 *  call had either. recomputed: the calls priced at the list for want of a
 *  reported cost; unpricedCalls / unpricedTokens: those with neither, in no
 *  figure. A server without lead_usd (older) is read from what it sends: a
 *  cell all of whose calls reported leads with that, any other with its
 *  list price. */
export function cellLead(c: Pick<ExternalCell, "calls" | "list_usd" | "reported_usd" | "reported_calls" | "unpriced_calls" | "unpriced_tokens"
  | "lead_usd" | "lead_unpriced_calls" | "lead_unpriced_tokens">): { lead: number | null; recomputed: number; unpricedCalls: number; unpricedTokens: number } {
  if (c.lead_usd !== undefined) {
    const unpricedCalls = c.lead_unpriced_calls ?? 0;
    return { lead: c.lead_usd, recomputed: Math.max(0, c.calls - c.reported_calls - unpricedCalls), unpricedCalls,
      unpricedTokens: c.lead_unpriced_tokens ?? 0 };
  }
  if (c.reported_usd != null && c.reported_calls >= c.calls) return { lead: c.reported_usd, recomputed: 0, unpricedCalls: 0, unpricedTokens: 0 };
  return { lead: c.list_usd, recomputed: Math.max(0, c.calls - c.unpriced_calls), unpricedCalls: c.unpriced_calls, unpricedTokens: c.unpriced_tokens };
}

/** Per source, per chart period (keyed by the period's start in ms): the
 *  cells of /cost/cells' external field, which are cut at the chart's bucket. */
export function sourcesPerPeriod(ext: ExternalUsage | null | undefined): Map<string, Map<number, SourcePeriod>> {
  const out = new Map<string, Map<number, SourcePeriod>>();
  for (const c of ext?.cells ?? []) {
    let by = out.get(c.source);
    if (!by) out.set(c.source, (by = new Map()));
    const k = Date.parse(c.period);
    const e = by.get(k) ?? { period: c.period, calls: 0, list: null, reported: null, measured: {}, tokens: {},
      lead: null, leadRecomputed: 0, leadUnpricedCalls: 0, leadUnpricedTokens: 0 };
    e.calls += c.calls;
    if (c.list_usd != null) e.list = (e.list ?? 0) + c.list_usd;
    if (c.reported_usd != null) e.reported = (e.reported ?? 0) + c.reported_usd;
    const l = cellLead(c);
    if (l.lead != null) e.lead = (e.lead ?? 0) + l.lead;
    e.leadRecomputed += l.recomputed;
    e.leadUnpricedCalls += l.unpricedCalls;
    e.leadUnpricedTokens += l.unpricedTokens;
    for (const [t, v] of Object.entries(c.measured ?? {})) e.measured[t] = (e.measured[t] ?? 0) + v;
    for (const [t, v] of Object.entries(c.tokens ?? {})) e.tokens[t] = (e.tokens[t] ?? 0) + v;
    by.set(k, e);
  }
  return out;
}

/** The sources that name this seat's meter, by name. */
export function seatSources(ext: ExternalUsage | null | undefined, key: string, slot: number): ExternalSource[] {
  return listedSources(ext).filter((s) => s.meter_credential?.secret_key === key && s.meter_credential.secret_slot === slot);
}

/** The sources whose seat has no block of its own on the page (`seats`: the
 *  blocks' ids, key#slot): billed per use, or linked to a seat the window
 *  has neither lines nor readings for. */
export function sourcesWithoutSeat(ext: ExternalUsage | null | undefined, seats: ReadonlySet<string>): ExternalSource[] {
  return listedSources(ext).filter((s) => !s.meter_credential || !seats.has(seatOf(s.meter_credential)));
}

/** The outside part of a seat's real cost per chart period, per source (the
 *  server's source_parts of each split step, placed at its reading). Empty
 *  while the server sends no parts. */
export function sourceRealPerPeriod(ext: ExternalUsage | null | undefined, key: string, slot: number, bucket: string): Map<number, Map<string, number>> {
  const out = new Map<number, Map<string, number>>();
  for (const st of ext?.meter.steps ?? []) {
    if (st.secret_key !== key || st.secret_slot !== slot || !st.split || !st.source_parts) continue;
    const k = bucketStart(Date.parse(st.at), bucket);
    const by = out.get(k) ?? new Map<string, number>();
    for (const p of st.source_parts) by.set(p.source, (by.get(p.source) ?? 0) + p.real_usd);
    out.set(k, by);
  }
  return out;
}

/** A machine of a seat's sources that had not reported through the seat's
 *  latest reading left unsplit as not reported: through when it last said
 *  (null: it never did). */
export type HostBehind = { source: string; host: string; through: string | null };

export function hostsBehind(ext: Pick<ExternalUsage, "sources" | "meter"> | null | undefined, key: string, slot: number): HostBehind[] {
  let at = -Infinity;
  for (const st of ext?.meter.steps ?? []) {
    if (st.secret_key === key && st.secret_slot === slot && !st.split && st.reason === "not_reported") at = Math.max(at, Date.parse(st.at));
  }
  if (at === -Infinity) return [];
  const out: HostBehind[] = [];
  const real = sourceRealParts(ext);
  for (const s of ext!.sources) {
    if (!listedSource(s, real) ||s.meter_credential?.secret_key !== key || s.meter_credential.secret_slot !== slot) continue;
    for (const h of s.hosts ?? []) {
      if (h.reported_through == null || Date.parse(h.reported_through) < at) out.push({ source: s.source, host: h.host, through: h.reported_through });
    }
  }
  return out.sort((a, b) => a.source.localeCompare(b.source) || a.host.localeCompare(b.host));
}

/** One row per source and model for the per-model table, from the
 *  external-usage endpoint's cells. */
export type OutsideModelRow = {
  source: string; model: string; calls: number; list: number | null; reported: number | null;
  output: number; cacheRead: number; cacheWrite5m: number; cacheWrite1h: number; unpricedTokens: number;
};

export function outsideModelRows(cells: readonly ExternalModelCell[]): OutsideModelRow[] {
  const by = new Map<string, OutsideModelRow>();
  for (const c of cells) {
    const k = `${c.source}\u0000${c.model}`;
    const r = by.get(k) ?? { source: c.source, model: c.model, calls: 0, list: null, reported: null, output: 0, cacheRead: 0,
      cacheWrite5m: 0, cacheWrite1h: 0, unpricedTokens: 0 };
    r.calls += c.calls;
    if (c.list_usd != null) r.list = (r.list ?? 0) + c.list_usd;
    if (c.reported_usd != null) r.reported = (r.reported ?? 0) + c.reported_usd;
    r.output += c.tokens.output ?? 0;
    r.cacheRead += c.tokens.cache_read ?? 0;
    r.cacheWrite5m += c.tokens.cache_write_5m ?? 0;
    r.cacheWrite1h += c.tokens.cache_write_1h ?? 0;
    r.unpricedTokens += c.unpriced_tokens;
    by.set(k, r);
  }
  return [...by.values()].sort((a, b) => a.source.localeCompare(b.source) || (b.list ?? -1) - (a.list ?? -1) || a.model.localeCompare(b.model));
}

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
 *  parts of its split steps (estimates, at their chunk's share), a figure
 *  only while splitSteps > 0. unattributed: the split steps' part beyond the
 *  rate (a lower bound) and the steps the server books to nobody whole (an
 *  estimate), each a figure only while such a step is there; rated: a split
 *  step of it is on a seat with a rate (k), where the part beyond the rate
 *  is measured (without one it is $0 and no bound). Then the steps not split, per reason, with their
 *  real cost; the steps at no plan price; the idle ones. noReading: the
 *  outside sessions worked in it on a seat that has no reading placed there
 *  (another seat's may be). outside + covey + unattributed + Σ unsplit =
 *  real. */
export type MeterPeriod = {
  period: number;
  real: number | null;
  splitSteps: number;
  rated: boolean;
  outside: number;
  covey: number;
  unattributed: Unattributed;
  unsplit: Unsplit;
  unpricedSteps: number;
  /** Steps under the usage basis (0151): out of the dent, the usage charges are their money. */
  usageSteps: number;
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
      e = { period: p, real: null, splitSteps: 0, rated: false, outside: 0, covey: 0, unattributed: emptyUnattributed(), unsplit: emptyUnsplit(),
        unpricedSteps: 0, usageSteps: 0, idleSteps: 0, noReading: false };
      by.set(p, e);
    }
    return e;
  };
  // The periods each seat has a reading in: a source's work is placed on
  // its own seat's readings, so another seat's reading in the same period
  // does not stand in for it.
  const read = new Set<string>();
  const rated = new Set(ext.meter.credentials.filter((c) => c.points_per_list_usd != null).map(seatOf));
  for (const s of ext.meter.steps) {
    if (!seats.has(seatOf(s))) continue;
    const p = bucketStart(Date.parse(s.at), bucket);
    read.add(`${seatOf(s)}@${p}`);
    const e = at(p);
    if (s.real_usd != null) e.real = (e.real ?? 0) + s.real_usd;
    const k = stepKind(s);
    addUnattributed(e.unattributed, s, k);
    if (k === "split") {
      e.splitSteps += 1;
      e.rated ||= rated.has(seatOf(s));
      e.outside += s.external_real_usd ?? 0;
      e.covey += s.covey_real_usd ?? 0;
    } else if (k === "unpriced") {
      e.unpricedSteps += 1;
    } else if (k === "usage") {
      e.usageSteps += 1;
    } else if (k === "idle") {
      e.idleSteps += 1;
    } else if (k !== "unattributed") {
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
