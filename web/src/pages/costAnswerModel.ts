import type { BilledWindow, ExternalMeterTotal, ExternalUsage, MeterCoverage } from "../api";
import { credId, pickMeter, type Headline, type SubscriptionDent } from "./costFigures";
import type { Basis, NoFigureReason } from "./costBasisSwitch";
import { isOperatorSource, listedSources, outsideKpis, perUseSource, sourceRealParts, type OutsideKpis } from "./outsideFigures";

/* The answer table of the Costs page (#195): one row per consumer of the
 * subscription — covey's agents, the covey operator, each other project — with
 * its work at token prices and its share of the real cost, a total and the
 * multiple. It adds no figure of its own: every number is one the headline
 * (costFigures.headline) and the dent (costFigures.subscriptionDent) already
 * hold, set out as rows. The remainder rows are the server's own parts
 * (unsplit, unattributed, the outside part no source names), not a
 * subtraction, so the real column adds up as the token-price column does. */

export type AnswerRow = {
  key: string;
  kind: "covey" | "operator" | "project" | "small" | "unsplit" | "unattributed" | "outsideRest";
  /** The outside source behind a row of an outside part; null for covey and the remainder rows. */
  source: string | null;
  /** The work at the lead price (reported where a call carries it, else recomputed); null: no call of it had a price. */
  list: number | null;
  /** Part of `list` was recomputed from tokens for want of a reported cost. */
  recomputed: boolean;
  /** Tokens of calls with neither a reported cost nor a list price: in no figure, said beside it. */
  unpricedTokens: number;
  /** Its calls lie before the range: listed for its real part only. */
  before: boolean;
  /** Its share of the meter's real cost (an estimate); null: no figure. */
  real: number | null;
  /** The share has no figure because no split step backs the part: its real cost is not 0, it is not known yet (#164). */
  notSplit: boolean;
  /** The share has a figure, but a seat behind it has no split step: it holds the split seats' part only. */
  partlySplit: boolean;
  /** All of its work at token prices came after the meter's last reading (none lies within a reading): its real
   *  share is not known yet, not 0. Shown as "pending", never as ≈ $0.00 (#164's rule, the human on #204). */
  pending: boolean;
  /** All of its work came before the first meter reading of the seats behind it: there is no meter data for that time,
   *  so it has no real share at all (not 0, not pending). The first reading's time; null otherwise. Shown as "before the
   *  first reading", never as ≈ $0.00 (cost home proposal, PR 1: the auction and ghostrobot rows read ≈ $0.00). */
  beforeMeter: string | null;
  /** kind "small": the sources folded into the row, each under SMALL_USD at token prices. */
  folded: string[];
};

/** Other projects below this at token prices are folded into one "small" row: a row of $0.12 is noise next to the answer. */
export const SMALL_USD = 1;

export type AnswerTable = {
  rows: AnswerRow[];
  /** The rows' list added up: with every source shown, the headline's `list`. */
  list: number;
  /** The meter's real cost over the rows shown; null where no reading has a plan price. */
  real: number | null;
  /** The meter's points over the whole range, and the plan's price of one; null at several prices. */
  points: number | null;
  rate: number | null;
  /** The points per meter week where the range spans a reset (one entry per week); else null. */
  weeks: number[] | null;
  /** Sources the user switched off: their rows are gone and so is their share of the real cost. */
  hidden: string[];
  /** list ÷ real over the metered readings, and its floor; null where a source is hidden or the headline has none. */
  multiple: number | null;
  /** The multiple's numerator: the work at token prices within the meter's readings (Headline.metered), which is less
   *  than `list` by what lies before, after, unpriced or on the usage basis. multiple = metered ÷ real. */
  metered: number;
  floor: number | null;
  why: Headline["why"];
  /** The sessions outside covey billed per use whose chips are on: on no meter, so in no row. */
  perUse: OutsideKpis | null;
  /** The chunks of readings the split holds approximately (spec/30 R4): their parts may hold use that is not theirs. */
  incompleteChunks: number;
  /** What the table answers for: the seats of the meter and the sources it has a row for. Anything else keeps its own figures. */
  covers: { seats: ReadonlySet<string>; sources: ReadonlySet<string> };
};

/** What the table answers for; see AnswerTable.covers. */
export type AnswerCovers = AnswerTable["covers"];

/** The table answers for a seat's own real cost and multiple only with one seat: with two or more it adds them up, and
 *  the per-seat figures are its parts, so they stay on the seat's card and tiles. */
export function answersSeat(covers: AnswerCovers | undefined, id: string): boolean {
  return covers != null && covers.seats.size === 1 && covers.seats.has(id);
}

/** The answer table from the headline and the dent. off: the outside sources
 *  switched off in the chart's chips (empty without them). */
export function answerTable(h: Headline, d: SubscriptionDent, ext: ExternalUsage, coverage: readonly MeterCoverage[],
  off: ReadonlySet<string>): AnswerTable {
  const parts = sourceRealParts(ext);
  const priced = d.real != null && h.real != null;
  const shown = h.sources.filter((s) => !off.has(s.source));
  const hidden = h.sources.filter((s) => off.has(s.source)).map((s) => s.source);
  const lead = (xs: typeof shown) => {
    const priced = xs.filter((s) => s.list != null);
    return priced.length > 0 ? priced.reduce((a, s) => a + s.list!, 0) : null;
  };
  // A part no split step backs has a real cost of 0 because nothing was split, not because it cost nothing: no figure
  // (#164, the same rule as the dent's). Decided per row from the seats behind it: covey's part from the seats with
  // covey work, a source's from the seats that name it. Every such seat with a split step: the figure; none: "not
  // split yet"; some: the figure of the split seats, said to be partial.
  const m = ext.meter;
  const state = (cs: readonly ExternalMeterTotal[]): "all" | "some" | "none" => {
    const n = cs.filter((c) => c.split_steps > 0).length;
    return n === 0 ? "none" : n === cs.length ? "all" : "some";
  };
  const coveyAt = new Set(m.steps.filter((x) => x.covey_list_usd > 0).map((x) => credId(x.secret_key, x.secret_slot)));
  const coveySeats = m.credentials.filter((c) => c.split_covey_list_usd > 0 || coveyAt.has(credId(c.secret_key, c.secret_slot)));
  const coveyState = state(coveySeats.length > 0 ? coveySeats : m.credentials);
  // A source no seat names (its calls lie before the range) keeps the meter-wide rule: any seat that names a source has a
  // split step. No seat is behind such a source, so it is never "some": that row says "partly not split yet: a seat behind
  // it", a claim about a seat that does not exist. It has its figure where any seat is split, none where no seat is.
  const namedSeats = m.credentials.filter((c) => c.sources.length > 0);
  const sourceState = (xs: readonly string[]) => {
    const cs = m.credentials.filter((c) => c.sources.some((x) => xs.includes(x)));
    if (cs.length > 0) return state(cs);
    return namedSeats.some((c) => c.split_steps > 0) ? "all" : "none";
  };
  const outsideSplit = namedSeats.some((c) => c.split_steps > 0);
  const flags = (st: "all" | "some" | "none") => ({ notSplit: priced && st === "none", partlySplit: priced && st === "some" });
  const real = (xs: typeof shown) => (priced && sourceState(xs.map((s) => s.source)) !== "none" ? xs.reduce((a, s) => a + (parts.get(s.source) ?? 0), 0) : null);
  const row = (r: Pick<AnswerRow, "key" | "kind"> & Partial<AnswerRow>): AnswerRow => ({
    source: null, list: null, recomputed: false, unpricedTokens: 0, before: false, real: null, notSplit: false, partlySplit: false,
    pending: false, beforeMeter: null, folded: [], ...r,
  });
  // A row whose every call came after the meter's last reading has no share yet: it is pending, not 0 (its real cost
  // follows at the next reading). Decided from the calls' times, never from a part of 0: a part can be 0 for other
  // reasons (no split step: "not split yet").
  // The last reading is the seat's own: seats are read at different times, and a source's share comes only from the
  // steps of the seats that name it. A source no seat names keeps the meter's last reading.
  const lastAt = new Map<string, number>();
  const firstFrom = new Map<string, number>();
  for (const x of m.steps) {
    const id = credId(x.secret_key, x.secret_slot);
    lastAt.set(id, Math.max(lastAt.get(id) ?? -Infinity, Date.parse(x.at)));
    // A reading's span starts at its `from` (the reading before it); the first reading's `from` is the meter's baseline.
    firstFrom.set(id, Math.min(firstFrom.get(id) ?? Infinity, Date.parse(x.from ?? x.at)));
  }
  const meterLast = Math.max(-Infinity, ...lastAt.values());
  const lastReadingOf = (source: string) => {
    const cs = m.credentials.filter((c) => c.sources.includes(source));
    return cs.length > 0 ? Math.max(-Infinity, ...cs.map((c) => lastAt.get(credId(c.secret_key, c.secret_slot)) ?? -Infinity)) : meterLast;
  };
  const firstAt = new Map(ext.sources.map((x) => [x.source, x.first_at]));
  const lastCall = new Map(ext.sources.map((x) => [x.source, x.last_at]));
  // The first reading of the seats that name a source: the earliest start of any of their readings.
  const firstReadingOf = (source: string) => {
    const cs = m.credentials.filter((c) => c.sources.includes(source));
    const ids = cs.length > 0 ? cs.map((c) => credId(c.secret_key, c.secret_slot)) : [...firstFrom.keys()];
    return Math.min(Infinity, ...ids.map((id) => firstFrom.get(id) ?? Infinity));
  };
  const sourceBeforeMeter = (s: (typeof shown)[number]) => {
    const l = lastCall.get(s.source), first = firstReadingOf(s.source);
    return l != null && Number.isFinite(first) && Date.parse(l) < first;
  };
  // A row all of whose work lies before the first reading has no real share at all: named, never ≈ $0.00.
  const withBefore = (r: AnswerRow, xs: typeof shown): AnswerRow => {
    if (!priced || r.pending || xs.length === 0 || !xs.every(sourceBeforeMeter)) return r;
    const first = Math.min(...xs.map((s) => firstReadingOf(s.source)));
    return { ...r, real: null, notSplit: false, partlySplit: false, beforeMeter: new Date(first).toISOString() };
  };
  // The list each source has within the split readings (the server's source_parts). A row none of whose work lies in a
  // split reading has no share yet (its work is before, after or between readings, or in a reading not split so far):
  // "not assigned yet", never ≈ $0.00. This also covers work on both sides of the readings and none inside them.
  const partList = new Map<string, number>();
  for (const p of m.source_parts ?? m.credentials.flatMap((c) => c.source_parts ?? [])) partList.set(p.source, (partList.get(p.source) ?? 0) + p.list_usd);
  const noSplitWork = (r: AnswerRow, xs: typeof shown): AnswerRow =>
    priced && r.real != null && r.real < 0.005 && xs.length > 0 && xs.every((s) => !((partList.get(s.source) ?? 0) > 0)) &&
      ((r.list ?? 0) >= 0.005 || r.unpricedTokens > 0)
      ? { ...r, real: null, notSplit: true, partlySplit: false } : r;
  // A row of several sources where most of its work (by token price) lies in no split reading: its figure holds only the
  // sources with a part, so it says it may be too low instead of standing as a bare ≈ $0.00 (#212 verify).
  const mostlyUnassigned = (r: AnswerRow, xs: typeof shown): AnswerRow => {
    if (r.real == null || r.partlySplit || xs.length < 2) return r;
    const all = xs.reduce((a, s) => a + (s.list ?? 0), 0);
    const assigned = xs.filter((s) => (partList.get(s.source) ?? 0) > 0).reduce((a, s) => a + (s.list ?? 0), 0);
    return all >= 0.005 && assigned < all / 2 ? { ...r, partlySplit: true } : r;
  };
  const finish = (r: AnswerRow, xs: typeof shown): AnswerRow => {
    const out = withBefore(withPending(r, xs), xs);
    return out.pending || out.beforeMeter ? out : mostlyUnassigned(noSplitWork(out, xs), xs);
  };
  const sourcePending = (s: (typeof shown)[number]) => {
    const f = firstAt.get(s.source), last = lastReadingOf(s.source);
    return f != null && Number.isFinite(last) && Date.parse(f) > last;
  };
  // Something to show: a price, or tokens with none (a row of unpriced calls is pending too, never "≈ $0.00").
  const pendingOf = (xs: typeof shown) => priced && xs.length > 0 && xs.every(sourcePending) &&
    (xs.reduce((a, s) => a + (s.list ?? 0), 0) >= 0.005 || xs.some((s) => s.unpricedTokens > 0));
  const withPending = (r: AnswerRow, xs: typeof shown): AnswerRow =>
    pendingOf(xs) ? { ...r, real: null, notSplit: false, partlySplit: false, pending: true } : r;
  const rows: AnswerRow[] = [row({ key: "covey", kind: "covey", list: h.coveyList, real: priced && coveyState !== "none" ? d.covey.real : null,
    ...flags(coveyState) })];
  const operator = shown.filter((s) => isOperatorSource(s.source));
  if (operator.length > 0) {
    const st = sourceState(operator.map((s) => s.source));
    rows.push(finish(row({ key: "operator", kind: "operator", list: lead(operator), recomputed: operator.some((s) => s.recomputed > 0),
      unpricedTokens: operator.reduce((a, s) => a + s.unpricedTokens, 0), before: operator.every((s) => s.before), real: real(operator),
      ...flags(st) }), operator));
  }
  // Other projects: each its own row, except the small ones (under SMALL_USD, with a price, not before the range, not pending),
  // which share one row. Folding changes no total: the row's list and real are its sources' sums.
  const projects = shown.filter((x) => !isOperatorSource(x.source));
  // A pending project keeps its own row: folded, its "pending" would be lost in the row's partial real figure.
  const isSmall = (s: (typeof shown)[number]) => s.list != null && s.list < SMALL_USD && !s.before && s.unpricedTokens === 0 && !pendingOf([s]);
  const small = projects.filter(isSmall);
  for (const s of projects.filter((x) => small.length < 2 || !isSmall(x))) {
    rows.push(finish(row({ key: `project:${s.source}`, kind: "project", source: s.source, list: s.list, recomputed: s.recomputed > 0,
      unpricedTokens: s.unpricedTokens, before: s.before, real: real([s]), ...flags(sourceState([s.source])) }), [s]));
  }
  if (small.length >= 2) {
    rows.push(finish(row({ key: "small", kind: "small", folded: small.map((s) => s.source), list: lead(small),
      recomputed: small.some((s) => s.recomputed > 0), real: real(small), ...flags(sourceState(small.map((s) => s.source))) }), small));
  }
  // What a hidden source took of the meter is not the table's: the total follows the rows shown.
  const hiddenReal = priced ? h.sources.filter((s) => off.has(s.source)).reduce((a, s) => a + (parts.get(s.source) ?? 0), 0) : 0;
  const total = priced ? h.real! - hiddenReal : null;
  // The rest of the real cost, as the server splits it: readings not split yet, movement no session accounts for, and
  // the outside part no source names. The server's identity: real = covey + outside + unattributed + unsplit.
  if (priced) {
    const named = [...parts.values()].reduce((a, v) => a + v, 0);
    const outsideRest = outsideSplit ? d.outside.real - named : 0;
    for (const [key, kind, v] of [["unsplit", "unsplit", d.unsplit.real], ["unattributed", "unattributed", d.unattributed.real],
      ["outsideRest", "outsideRest", outsideRest]] as const) {
      if (v >= 0.005) rows.push(row({ key, kind, real: v }));
    }
    // The sources' parts over-carry the outside part (the server's rounding, a part counted on two seats): a signed row,
    // so the real column still adds up to the total.
    if (outsideRest <= -0.005) rows.push(row({ key: "outsideRest", kind: "outsideRest", real: outsideRest }));
  }
  const rate = h.points != null && m.point_rate > 0 && !m.point_rate_mixed ? m.point_rate : null;
  // One seat whose weekly window spans a reset: its points per week, as the coverage reads them.
  let weeks: number[] | null = null;
  if (m.credentials.length === 1 && h.points != null) {
    const c = m.credentials[0];
    const segs = pickMeter(coverage, c.secret_key, c.secret_slot)?.segments ?? [];
    if (segs.length > 1 && Math.abs(segs.reduce((a, s) => a + s.points, 0) - h.points) < 0.5) weeks = segs.map((s) => s.points);
  }
  // The per-use sessions on no meter: a line, not a row, and only those whose chip is on.
  const perUseOn = listedSources(ext).filter((s) => perUseSource(s) && !off.has(s.source));
  const perUse = perUseOn.length === 0 ? null : outsideKpis(ext, new Set(perUseOn.map((s) => s.source)));
  return {
    rows, list: rows.reduce((a, r) => a + (r.list ?? 0), 0), real: total, points: h.points, rate, weeks, hidden,
    multiple: hidden.length === 0 ? h.multiple : null, metered: h.metered, incompleteChunks: d.incompleteChunks, floor: hidden.length === 0 ? h.floor : null, why: h.why, perUse,
    covers: { seats: new Set(m.credentials.map((c) => credId(c.secret_key, c.secret_slot))), sources: new Set(h.sources.map((s) => s.source)) },
  };
}

/** The rows for the rest of the real cost: they hold no work of their own, so no token price and no party. */
export const isRemainderRow = (r: Pick<AnswerRow, "kind">) => r.kind === "unsplit" || r.kind === "unattributed" || r.kind === "outsideRest";

/** A parts-table money cell under a basis (cost home PR 9): a figure (≈ where an estimate), no figure with its reason
 *  (never $0.00), or no cell at all (a remainder row holds no work at token prices). Capacity is each row's server
 *  figure (covey_real_usd, source_parts[].real_usd, the server's remainder parts), so the rows add up to the real
 *  total; a bill exists per API key, never per party. */
export type PartsMoney = { usd: number; estimate: boolean } | { none: NoFigureReason } | null;

export function partsMoney(r: AnswerRow, basis: Basis): PartsMoney {
  const remainder = isRemainderRow(r);
  if (basis === "billed") return remainder ? null : { none: "notAtThisLevel" };
  if (basis === "capacity") {
    if (r.pending) return { none: "pending" };
    if (r.beforeMeter) return { none: "beforeMeter" };
    if (r.real == null) return { none: r.notSplit ? "notSplit" : "noData" };
    return { usd: r.real, estimate: true };
  }
  if (remainder) return null;
  return r.list == null ? { none: "unpricedExcluded" } : { usd: r.list, estimate: false };
}

/** What Anthropic billed the mapped pay-per-token API keys over the bill's window of whole buckets (B1): the sum of
 *  the credentials' totals, each a decimal string. null where no key has a bill (no window, no totals, none billed):
 *  never $0. Whole keys, not only covey's use of them, and not seats: never part of the answer. */
export function billedKeys(w: BilledWindow | null | undefined): { usd: number; until: string } | null {
  const billed = (w?.totals ?? []).filter((x) => x.has_billed);
  if (!w || billed.length === 0) return null;
  let usd = 0;
  for (const x of billed) {
    const v = Number.parseFloat(x.billed_usd);
    if (Number.isFinite(v)) usd += v;
  }
  return { usd, until: w.compared_until || w.end };
}
