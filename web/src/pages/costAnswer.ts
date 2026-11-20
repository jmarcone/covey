import type { ExternalUsage, MeterCoverage } from "../api";
import { pickMeter, type Headline, type SubscriptionDent } from "./costFigures";
import { isOperatorSource, sourceRealParts } from "./outsideFigures";

/* The answer table of the Costs page (#195): one row per consumer of the
 * subscription — covey's agents, the covey operator, each other project — with
 * its work at token prices and its share of the real cost, a total and the
 * multiple. It adds no figure of its own: every number is one the headline
 * (costFigures.headline) and the dent (costFigures.subscriptionDent) already
 * hold, set out as rows. The one thing computed here is the remainder row
 * (the meter's movement no source explains), a subtraction, so that the real
 * column adds up as the token-price column does. */

export type AnswerRow = {
  key: string;
  kind: "covey" | "operator" | "project" | "rest";
  /** The outside source behind a row of an outside part; null for covey and the rest. */
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
  floor: number | null;
  why: Headline["why"];
};

/** The answer table from the headline and the dent. off: the outside sources
 *  switched off in the chart's chips. */
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
  const real = (xs: typeof shown) => (priced ? xs.reduce((a, s) => a + (parts.get(s.source) ?? 0), 0) : null);
  const rows: AnswerRow[] = [{ key: "covey", kind: "covey", source: null, list: h.coveyList, recomputed: false, unpricedTokens: 0, before: false,
    real: priced ? d.covey.real : null }];
  const operator = shown.filter((s) => isOperatorSource(s.source));
  if (operator.length > 0) {
    rows.push({ key: "operator", kind: "operator", source: null, list: lead(operator), recomputed: operator.some((s) => s.recomputed > 0),
      unpricedTokens: operator.reduce((a, s) => a + s.unpricedTokens, 0), before: operator.every((s) => s.before), real: real(operator) });
  }
  for (const s of shown.filter((x) => !isOperatorSource(x.source))) {
    rows.push({ key: `project:${s.source}`, kind: "project", source: s.source, list: s.list, recomputed: s.recomputed > 0,
      unpricedTokens: s.unpricedTokens, before: s.before, real: real([s]) });
  }
  // What a hidden source took of the meter is not the table's: the total follows the rows shown.
  const hiddenReal = priced ? h.sources.filter((s) => off.has(s.source)).reduce((a, s) => a + (parts.get(s.source) ?? 0), 0) : 0;
  const total = priced ? h.real! - hiddenReal : null;
  if (total != null) {
    const rest = total - rows.reduce((a, r) => a + (r.real ?? 0), 0);
    if (rest >= 0.005) rows.push({ key: "rest", kind: "rest", source: null, list: null, recomputed: false, unpricedTokens: 0, before: false, real: rest });
  }
  const rate = h.points != null && ext.meter.point_rate > 0 && !ext.meter.point_rate_mixed ? ext.meter.point_rate : null;
  // One seat whose weekly window spans a reset: its points per week, as the coverage reads them.
  let weeks: number[] | null = null;
  if (ext.meter.credentials.length === 1 && h.points != null) {
    const c = ext.meter.credentials[0];
    const segs = pickMeter(coverage, c.secret_key, c.secret_slot)?.segments ?? [];
    if (segs.length > 1 && Math.abs(segs.reduce((a, s) => a + s.points, 0) - h.points) < 0.5) weeks = segs.map((s) => s.points);
  }
  return {
    rows, list: rows.reduce((a, r) => a + (r.list ?? 0), 0), real: total, points: h.points, rate, weeks, hidden,
    multiple: hidden.length === 0 ? h.multiple : null, floor: hidden.length === 0 ? h.floor : null, why: h.why,
  };
}
