import type { ExternalMeterTotal, ExternalUsage, MeterCoverage } from "../api";
import { credId, pickMeter, type Headline, type SubscriptionDent } from "./costFigures";
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
  kind: "covey" | "operator" | "project" | "unsplit" | "unattributed" | "outsideRest";
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
};

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
  // A source no seat names (its calls lie before the range) keeps the meter-wide rule: any seat that names a source.
  const namedSeats = m.credentials.filter((c) => c.sources.length > 0);
  const sourceState = (xs: readonly string[]) => {
    const cs = m.credentials.filter((c) => c.sources.some((x) => xs.includes(x)));
    return state(cs.length > 0 ? cs : namedSeats);
  };
  const outsideSplit = namedSeats.some((c) => c.split_steps > 0);
  const flags = (st: "all" | "some" | "none") => ({ notSplit: priced && st === "none", partlySplit: priced && st === "some" });
  const real = (xs: typeof shown) => (priced && sourceState(xs.map((s) => s.source)) !== "none" ? xs.reduce((a, s) => a + (parts.get(s.source) ?? 0), 0) : null);
  const row = (r: Pick<AnswerRow, "key" | "kind"> & Partial<AnswerRow>): AnswerRow => ({
    source: null, list: null, recomputed: false, unpricedTokens: 0, before: false, real: null, notSplit: false, partlySplit: false, ...r,
  });
  const rows: AnswerRow[] = [row({ key: "covey", kind: "covey", list: h.coveyList, real: priced && coveyState !== "none" ? d.covey.real : null,
    ...flags(coveyState) })];
  const operator = shown.filter((s) => isOperatorSource(s.source));
  if (operator.length > 0) {
    const st = sourceState(operator.map((s) => s.source));
    rows.push(row({ key: "operator", kind: "operator", list: lead(operator), recomputed: operator.some((s) => s.recomputed > 0),
      unpricedTokens: operator.reduce((a, s) => a + s.unpricedTokens, 0), before: operator.every((s) => s.before), real: real(operator),
      ...flags(st) }));
  }
  for (const s of shown.filter((x) => !isOperatorSource(x.source))) {
    rows.push(row({ key: `project:${s.source}`, kind: "project", source: s.source, list: s.list, recomputed: s.recomputed > 0,
      unpricedTokens: s.unpricedTokens, before: s.before, real: real([s]), ...flags(sourceState([s.source])) }));
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
